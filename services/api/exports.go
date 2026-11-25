package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed exports.sql
var exportsSQL string

const (
	// maxExportRows keeps one export to a size the worker can write quickly.
	maxExportRows = 100_000
	// defaultExportTTL is how long a finished file can be downloaded.
	defaultExportTTL = 24 * time.Hour
	// exportCleanupInterval is how often expired files are removed.
	exportCleanupInterval = 10 * time.Minute
)

// exportFormats maps each format to the file the worker writes and its media type.
var exportFormats = map[string]struct{ extension, mediaType string }{
	"gpkg":       {".gpkg", "application/geopackage+sqlite3"},
	"shapefile":  {".zip", "application/zip"},
	"kml":        {".kmz", "application/vnd.google-earth.kmz"},
	"geojson":    {".zip", "application/zip"},
	"flatgeobuf": {".zip", "application/zip"},
	"csv_wkt":    {".zip", "application/zip"},
	"csv_lonlat": {".zip", "application/zip"},
	"xlsx":       {".xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
}

var exportFileKey = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.(gpkg|zip|kmz|xlsx)$`)

// exportSettings holds where export files live and how long they last.
type exportSettings struct {
	dir string
	ttl time.Duration
}

// withExports turns on the export endpoints.
func withExports(dir string, ttl time.Duration) func(*api) {
	return func(a *api) { a.exports = exportSettings{dir: dir, ttl: ttl} }
}

type exportRequest struct {
	Kind          string   `json:"kind"`
	Format        string   `json:"format"`
	Hours         *int     `json:"hours"`
	ForestClasses []string `json:"forestClasses"`
	Queue         string   `json:"queue"`
	UnitPath      *string  `json:"unitPath"`
	// Step is a workflow step, such as new or acknowledged. It is the status
	// filter of the alert queue.
	Step string `json:"step"`
}

type exportStatus struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Format       string          `json:"format"`
	Status       string          `json:"status"`
	Filters      json.RawMessage `json:"filters"`
	RowCount     int             `json:"rowCount"`
	FeatureCount *int            `json:"featureCount"`
	FileBytes    *int64          `json:"fileBytes"`
	FileName     string          `json:"fileName,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
	FinishedAt   *time.Time      `json:"finishedAt"`
	ExpiresAt    time.Time       `json:"expiresAt"`
	Error        *string         `json:"error"`
}

// readExportRequest checks the request body. The filters are the ones the
// alert queue and the detection tiles accept.
func readExportRequest(body io.Reader) (exportRequest, error) {
	var req exportRequest
	decoder := json.NewDecoder(io.LimitReader(body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(new(any)) != io.EOF {
		return req, errors.New("Send the export request as one JSON object.")
	}
	if req.Kind != "detections" && req.Kind != "alerts" {
		return req, errors.New("kind must be detections or alerts.")
	}
	if _, ok := exportFormats[req.Format]; !ok {
		return req, errors.New("format must be gpkg, shapefile, kml, geojson, flatgeobuf, csv_wkt, csv_lonlat or xlsx.")
	}
	if req.Hours == nil {
		hours := 168
		req.Hours = &hours
	} else if *req.Hours < 1 || *req.Hours > 720 {
		return req, errors.New("hours must be an integer from 1 to 720.")
	}
	all := []string{"inside", "near_edge", "outside", "fixed_heat_source"}
	if req.ForestClasses == nil {
		// The same defaults as the map tiles and the alert queue.
		req.ForestClasses = []string{"inside", "near_edge", "fixed_heat_source"}
		if req.Kind == "alerts" {
			req.ForestClasses = all
		}
	}
	if len(req.ForestClasses) == 0 {
		return req, errors.New("Choose at least one forest class.")
	}
	for _, class := range req.ForestClasses {
		if !slices.Contains(all, class) {
			return req, errors.New("Unknown forest class.")
		}
	}
	slices.Sort(req.ForestClasses)
	req.ForestClasses = slices.Compact(req.ForestClasses)
	if req.Kind == "detections" && (req.Queue != "" || req.Step != "") {
		return req, errors.New("queue and step apply only to alert exports.")
	}
	if !slices.Contains([]string{"", "unit", "edge_review", "triage"}, req.Queue) || (req.Step != "" && !stepSlug.MatchString(req.Step)) {
		return req, errors.New("Unknown queue or workflow step.")
	}
	if req.UnitPath != nil && !unitPrefix.MatchString(*req.UnitPath) {
		return req, errors.New("Invalid unit path.")
	}
	return req, nil
}

func (a api) exportError(w http.ResponseWriter, message string, err error) {
	a.logger.Error(message, "error", err)
	writeError(w, http.StatusInternalServerError, "Could not handle the export.")
}

func (a api) createExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if a.exports.dir == "" {
		writeError(w, http.StatusServiceUnavailable, "Exports are not set up on this server.")
		return
	}
	req, err := readExportRequest(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	caller := r.Context().Value(identityKey{}).(identity)
	slug := chi.URLParam(r, "slug")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.exportError(w, "Could not start the export transaction", err)
		return
	}
	defer tx.Rollback(ctx)
	var allowed, knownStep bool
	var total int
	var id pgtype.UUID
	var filters json.RawMessage
	var frozen int64
	err = tx.QueryRow(ctx, exportsSQL, caller.Subject, slug, req.Kind, req.Format, *req.Hours,
		req.ForestClasses, req.Queue, req.UnitPath, req.Step, maxExportRows,
		pgtype.Interval{Microseconds: a.exports.ttl.Microseconds(), Valid: true},
	).Scan(&allowed, &knownStep, &total, &id, &filters, &frozen)
	if err != nil {
		a.exportError(w, "Could not freeze the export rows", err)
		return
	}
	switch {
	case !allowed:
		writeError(w, http.StatusForbidden, "A role in this workspace is required.")
		return
	case !knownStep:
		writeError(w, http.StatusBadRequest, "Unknown workflow step.")
		return
	case !id.Valid:
		writeError(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("This export has %d rows. Narrow the filters to at most %d rows.", total, maxExportRows))
		return
	case frozen != int64(total):
		a.exportError(w, "Could not freeze the export rows", fmt.Errorf("froze %d of %d rows", frozen, total))
		return
	}
	details, err := json.Marshal(map[string]any{
		"kind": req.Kind, "format": req.Format, "filters": filters, "rowCount": total,
	})
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
			SELECT workspace_id, 'user', $2, 'export.create', 'export', id::text, $3 FROM exports WHERE id = $1
		`, id, caller.Subject, details)
	}
	if err != nil {
		a.exportError(w, "Could not audit the export", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		a.exportError(w, "Could not save the export", err)
		return
	}
	status, err := a.readExport(r.Context(), slug, id, caller.Subject)
	if err != nil {
		a.exportError(w, "Could not read the new export", err)
		return
	}
	w.Header().Set("Location", "/api/v1/workspaces/"+slug+"/exports/"+id.String())
	writeJSON(w, http.StatusAccepted, status)
}

// readExport returns the caller's own export. Other users' exports are not found.
func (a api) readExport(ctx context.Context, slug string, id pgtype.UUID, subject string) (exportStatus, error) {
	var s exportStatus
	var fileKey *string
	var workspace string
	err := a.pool.QueryRow(ctx, `
		SELECT e.id::text, e.kind, e.format, e.status, e.filters, e.row_count, e.feature_count, e.file_bytes,
		       e.file_key, e.created_at, e.finished_at, e.expires_at, e.error_message, w.slug
		FROM exports e JOIN workspaces w ON w.id = e.workspace_id
		WHERE e.id = $1 AND w.slug = $2 AND e.requested_by = $3
	`, id, slug, subject).Scan(&s.ID, &s.Kind, &s.Format, &s.Status, &s.Filters, &s.RowCount, &s.FeatureCount,
		&s.FileBytes, &fileKey, &s.CreatedAt, &s.FinishedAt, &s.ExpiresAt, &s.Error, &workspace)
	if err != nil {
		return s, err
	}
	if s.Status == "ready" && fileKey != nil {
		// Zipped formats name the format, so two downloads do not look alike.
		format := ""
		if filepath.Ext(*fileKey) == ".zip" {
			format = "-" + strings.ReplaceAll(s.Format, "_", "-")
		}
		s.FileName = fmt.Sprintf("open-fimra-%s-%s%s-%s%s", workspace, s.Kind, format,
			s.CreatedAt.UTC().Format("20060102T1504Z"), filepath.Ext(*fileKey))
	}
	return s, nil
}

// ownExport finds the caller's export, and checks that the caller still has a
// role in the workspace. It writes the error response when it returns false.
func (a api) ownExport(w http.ResponseWriter, r *http.Request) (exportStatus, pgtype.UUID, bool) {
	w.Header().Set("Cache-Control", "private, no-store")
	caller := r.Context().Value(identityKey{}).(identity)
	slug := chi.URLParam(r, "slug")
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusNotFound, "The export was not found.")
		return exportStatus{}, id, false
	}
	var member bool
	err := a.pool.QueryRow(r.Context(), `
		SELECT EXISTS (SELECT 1 FROM role_assignments a JOIN users u ON u.id = a.user_id
		               JOIN workspaces w ON w.id = a.workspace_id
		               WHERE u.oidc_subject = $1 AND w.slug = $2)
	`, caller.Subject, slug).Scan(&member)
	if err != nil {
		a.exportError(w, "Could not check the export grant", err)
		return exportStatus{}, id, false
	}
	if !member {
		writeError(w, http.StatusForbidden, "A role in this workspace is required.")
		return exportStatus{}, id, false
	}
	status, err := a.readExport(r.Context(), slug, id, caller.Subject)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "The export was not found.")
		return status, id, false
	}
	if err != nil {
		a.exportError(w, "Could not read the export", err)
		return status, id, false
	}
	return status, id, true
}

func (a api) exportStatus(w http.ResponseWriter, r *http.Request) {
	if status, _, ok := a.ownExport(w, r); ok {
		writeJSON(w, http.StatusOK, status)
	}
}

func (a api) exportFile(w http.ResponseWriter, r *http.Request) {
	status, id, ok := a.ownExport(w, r)
	if !ok {
		return
	}
	switch status.Status {
	case "expired":
		writeError(w, http.StatusGone, "This export has expired. Request it again.")
		return
	case "pending":
		writeError(w, http.StatusConflict, "This export is still being prepared.")
		return
	case "failed":
		writeError(w, http.StatusConflict, "This export failed. Request it again.")
		return
	}
	if !time.Now().Before(status.ExpiresAt) {
		writeError(w, http.StatusGone, "This export has expired. Request it again.")
		return
	}
	var fileKey string
	if err := a.pool.QueryRow(r.Context(), "SELECT file_key FROM exports WHERE id = $1", id).Scan(&fileKey); err != nil {
		a.exportError(w, "Could not read the export file name", err)
		return
	}
	if !exportFileKey.MatchString(fileKey) {
		a.exportError(w, "The export file name is not valid", fmt.Errorf("file key %q", fileKey))
		return
	}
	file, err := os.Open(filepath.Join(a.exports.dir, fileKey))
	if err != nil {
		a.exportError(w, "Could not open the export file", err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		a.exportError(w, "Could not read the export file", err)
		return
	}
	caller := r.Context().Value(identityKey{}).(identity)
	details, _ := json.Marshal(map[string]any{"format": status.Format, "rowCount": status.FeatureCount})
	if _, err := a.pool.Exec(r.Context(), `
		INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
		SELECT workspace_id, 'user', $2, 'export.download', 'export', id::text, $3 FROM exports WHERE id = $1
	`, id, caller.Subject, details); err != nil {
		a.exportError(w, "Could not audit the export download", err)
		return
	}
	// Large files on slow links need longer than the server's default write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute))
	w.Header().Set("Content-Type", exportFormats[status.Format].mediaType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+status.FileName+`"`)
	http.ServeContent(w, r, status.FileName, info.ModTime(), file)
}

// expireExports marks exports past their expiry time as expired, removes their
// frozen rows and files, and removes stray files older than an hour that
// belong to no ready export.
func expireExports(ctx context.Context, pool *pgxpool.Pool, dir string) (int, error) {
	rows, err := pool.Query(ctx, `
		WITH due AS (
			SELECT id, file_key FROM exports
			WHERE status IN ('pending', 'ready', 'failed') AND expires_at <= now()
			FOR UPDATE SKIP LOCKED
		),
		marked AS (
			UPDATE exports e SET status = 'expired', file_key = NULL, file_bytes = NULL, feature_count = NULL,
			       error_message = NULL, finished_at = COALESCE(e.finished_at, now())
			FROM due WHERE e.id = due.id RETURNING e.id
		),
		removed AS (DELETE FROM export_rows r USING due WHERE r.export_id = due.id)
		SELECT due.file_key FROM due JOIN marked USING (id)
	`)
	if err != nil {
		return 0, fmt.Errorf("mark expired exports: %w", err)
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[*string])
	if err != nil {
		return 0, fmt.Errorf("mark expired exports: %w", err)
	}
	var problems []error
	for _, key := range keys {
		if key != nil && exportFileKey.MatchString(*key) {
			if err := os.Remove(filepath.Join(dir, *key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				problems = append(problems, err)
			}
		}
	}

	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return len(keys), errors.Join(problems...)
	}
	if err != nil {
		return len(keys), errors.Join(append(problems, fmt.Errorf("list export files: %w", err))...)
	}
	for _, entry := range entries {
		name := entry.Name()
		stray := strings.HasSuffix(name, ".part")
		if !stray && !exportFileKey.MatchString(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) < time.Hour {
			continue
		}
		if !stray {
			var ready bool
			id := strings.TrimSuffix(name, filepath.Ext(name))
			if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM exports WHERE id = $1::uuid AND status = 'ready')", id).Scan(&ready); err != nil {
				problems = append(problems, fmt.Errorf("check export file %s: %w", name, err))
				continue
			}
			if ready {
				continue
			}
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			problems = append(problems, err)
		}
	}
	return len(keys), errors.Join(problems...)
}

// runExportCleanup removes expired export files until ctx ends.
func runExportCleanup(ctx context.Context, pool *pgxpool.Pool, dir string, logger *slog.Logger) {
	ticker := time.NewTicker(exportCleanupInterval)
	defer ticker.Stop()
	for {
		count, err := expireExports(ctx, pool, dir)
		if err != nil {
			logger.Error("Could not clean up expired exports", "error", err)
		} else if count > 0 {
			logger.Info("Expired exports removed", "count", count)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
