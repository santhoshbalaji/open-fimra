package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const maxLayerFile = 32 << 20

var layerSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)

type importSettings struct {
	NameField         string      `json:"nameField,omitempty"`
	CodeField         string      `json:"codeField,omitempty"`
	ParentCodeField   string      `json:"parentCodeField,omitempty"`
	SourceLayer       string      `json:"sourceLayer,omitempty"`
	Depth             *int16      `json:"depth,omitempty"`
	ParentVersionID   pgtype.UUID `json:"parentVersionId"`
	CoverageVersionID pgtype.UUID `json:"coverageVersionId"`
}

type importProblem struct {
	Kind          string  `json:"kind"`
	FeatureRef    *string `json:"featureRef"`
	Message       string  `json:"message"`
	BlocksPublish bool    `json:"blocksPublish"`
}

type rematchProgress struct {
	Status         string `json:"status"`
	Processed      int64  `json:"processed"`
	MatchesWritten int64  `json:"matchesWritten"`
	AlertsCreated  int64  `json:"alertsCreated"`
}

type draftResponse struct {
	Rematch      *rematchProgress `json:"rematch,omitempty"`
	VersionID    pgtype.UUID      `json:"versionId"`
	Status       string           `json:"status"`
	Role         string           `json:"role"`
	SourceCRS    *string          `json:"sourceCrs"`
	FeatureCount int64            `json:"featureCount"`
	UnitCount    int64            `json:"unitCount"`
	Problems     []importProblem  `json:"problems"`
}

const layerAdminSQL = `
		SELECT w.id FROM workspaces w WHERE w.slug = $1 AND EXISTS (
			SELECT 1 FROM role_assignments a
			JOIN users u ON u.id = a.user_id
			JOIN units root ON root.hierarchy_id = a.hierarchy_id AND root.current_path = a.unit_path
			WHERE a.workspace_id = w.id AND a.role_slug = 'state_admin'
			  AND root.depth = 0 AND u.oidc_subject = $2
		)`

func (a api) layerAdmin(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	w.Header().Set("Cache-Control", "private, no-store")
	caller := r.Context().Value(identityKey{}).(identity)
	var workspaceID pgtype.UUID
	err := a.pool.QueryRow(r.Context(), layerAdminSQL, chi.URLParam(r, "slug"), caller.Subject).Scan(&workspaceID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusForbidden, "A state administrator role at the workspace root is required.")
	case err != nil:
		a.layerImportError(w, err)
	}
	return workspaceID, err == nil
}

func (a api) layerImportError(w http.ResponseWriter, err error) {
	a.logger.Error("Could not handle the layer import", "error", err)
	writeError(w, http.StatusInternalServerError, "Could not handle the layer import.")
}

func (a api) uploadLayer(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := a.layerAdmin(w, r)
	if !ok {
		return
	}
	deadline := time.Now().Add(2 * time.Minute)
	transport := http.NewResponseController(w)
	_ = transport.SetReadDeadline(deadline)
	_ = transport.SetWriteDeadline(deadline)
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, maxLayerFile+(64<<10))
	if err := r.ParseMultipartForm(64 << 10); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "Upload at most 32 MiB.")
		} else {
			writeError(w, http.StatusBadRequest, "Send a multipart form with one file.")
		}
		return
	}
	defer r.MultipartForm.RemoveAll()

	if len(r.MultipartForm.File["file"]) != 1 || len(r.MultipartForm.File) != 1 {
		writeError(w, http.StatusBadRequest, "Send exactly one file.")
		return
	}
	file := r.MultipartForm.File["file"][0]
	filename := filepath.Base(strings.ReplaceAll(file.Filename, "\\", "/"))
	if !utf8.ValidString(filename) || strings.ContainsRune(filename, 0) || len(filename) > 255 {
		writeError(w, http.StatusBadRequest, "Use a file name of up to 255 bytes.")
		return
	}
	extension := strings.ToLower(filepath.Ext(filename))
	switch extension {
	case ".gpkg", ".zip", ".geojson", ".kml", ".kmz":
	default:
		writeError(w, http.StatusBadRequest, "Use GeoPackage, zipped Shapefile, GeoJSON, KML or KMZ.")
		return
	}
	if file.Size == 0 || file.Size > maxLayerFile {
		if file.Size == 0 {
			writeError(w, http.StatusBadRequest, "The uploaded file is empty.")
		} else {
			writeError(w, http.StatusRequestEntityTooLarge, "Upload at most 32 MiB.")
		}
		return
	}

	slug, name, role := r.PostFormValue("layerSlug"), strings.TrimSpace(r.PostFormValue("layerName")), r.PostFormValue("role")
	if !layerSlug.MatchString(slug) || name == "" || len(name) > 200 || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		writeError(w, http.StatusBadRequest, "Supply a layer slug and a layer name of up to 200 bytes.")
		return
	}
	var settings importSettings
	if raw := r.PostFormValue("settings"); raw != "" {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&settings); err != nil || decoder.Decode(new(any)) != io.EOF {
			writeError(w, http.StatusBadRequest, "The import settings are not valid.")
			return
		}
	}

	for _, field := range []string{settings.NameField, settings.CodeField, settings.ParentCodeField, settings.SourceLayer} {
		if len(field) > 255 || strings.ContainsRune(field, 0) {
			writeError(w, http.StatusBadRequest, "Use field and source layer names of up to 255 bytes.")
			return
		}
	}

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.layerImportError(w, err)
		return
	}
	defer tx.Rollback(ctx)

	var hierarchyID pgtype.UUID
	hierarchy := r.PostFormValue("hierarchy")
	switch role {
	case "hierarchy":
		if settings.Depth == nil || settings.NameField == "" || settings.CoverageVersionID.Valid ||
			(*settings.Depth == 0 && settings.ParentVersionID.Valid) {
			writeError(w, http.StatusBadRequest, "Supply a level depth and name field for a hierarchy. Parent drafts require a child level.")
			return
		}
		err := tx.QueryRow(ctx, `
			SELECT h.id FROM hierarchies h JOIN hierarchy_levels hl ON hl.hierarchy_id = h.id
			WHERE h.workspace_id = $1 AND h.slug = $2 AND hl.depth = $3
		`, workspaceID, hierarchy, settings.Depth).Scan(&hierarchyID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "The hierarchy and level must exist in this workspace.")
			return
		}
		if err != nil {
			a.layerImportError(w, err)
			return
		}
	case "forest", "exclusion", "analysis":
		if hierarchy != "" || settings.Depth != nil || settings.ParentVersionID.Valid || settings.ParentCodeField != "" ||
			(role != "forest" && settings.CoverageVersionID.Valid) {
			writeError(w, http.StatusBadRequest, "These hierarchy or coverage settings do not apply to the selected role.")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "Select a role of forest, hierarchy, exclusion or analysis.")
		return
	}

	if settings.ParentVersionID.Valid || settings.CoverageVersionID.Valid {
		referenceID := settings.ParentVersionID
		var depth *int16
		if settings.ParentVersionID.Valid {
			depth = settings.Depth
		} else {
			referenceID = settings.CoverageVersionID
		}
		var valid bool
		err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM layer_versions v JOIN layers l ON l.id = v.layer_id
				JOIN geo_jobs j ON j.layer_version_id = v.id
				WHERE v.id = $1 AND v.status = 'draft' AND j.status = 'ready'
				  AND l.workspace_id = $2 AND l.role = 'hierarchy'
				  AND ($3::uuid IS NULL OR (l.hierarchy_id = $3 AND hierarchy_parent_allowed($3, $4, (v.import_settings->>'depth')::smallint)))
			)
		`, referenceID, workspaceID, hierarchyID, depth).Scan(&valid)
		if err != nil {
			a.layerImportError(w, err)
			return
		}
		if !valid {
			writeError(w, http.StatusBadRequest, "Choose a ready hierarchy draft in this workspace at the required level.")
			return
		}
	}

	var layerID, versionID pgtype.UUID
	// The upsert locks this layer until the new version number is saved.
	err = tx.QueryRow(ctx, `
		INSERT INTO layers (workspace_id, slug, name, role, hierarchy_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (workspace_id, slug) DO UPDATE SET slug = EXCLUDED.slug
		WHERE layers.role = EXCLUDED.role AND layers.hierarchy_id IS NOT DISTINCT FROM EXCLUDED.hierarchy_id
		RETURNING id
	`, workspaceID, slug, name, role, hierarchyID).Scan(&layerID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "This layer slug is already used for a different role or hierarchy.")
		return
	}
	if err != nil {
		a.layerImportError(w, err)
		return
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO layer_versions (layer_id, version, source_file, import_settings)
		SELECT $1, COALESCE(max(version), 0) + 1, $2, $3 FROM layer_versions WHERE layer_id = $1
		RETURNING id
	`, layerID, filename, settings).Scan(&versionID)
	if err != nil {
		a.layerImportError(w, err)
		return
	}

	fileKey := versionID.String() + extension
	path, err := saveLayerFile(a.uploadDir, fileKey, file.Open)
	if err != nil {
		a.layerImportError(w, err)
		return
	}
	caller := ctx.Value(identityKey{}).(identity)
	_, err = tx.Exec(ctx, "INSERT INTO geo_jobs (layer_version_id, file_key) VALUES ($1, $2)", versionID, fileKey)
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id)
			VALUES ($1, 'user', $2, 'layer.upload', 'layer_version', $3)
		`, workspaceID, caller.Subject, versionID.String())
	}
	if err != nil {
		a.removeLayerFile(path)
		a.layerImportError(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		// Keep the file if the connection died while committing. The job may exist.
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			a.removeLayerFile(path)
		}
		a.layerImportError(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/workspaces/"+chi.URLParam(r, "slug")+"/layer-imports/"+versionID.String())
	writeJSON(w, http.StatusAccepted, draftResponse{
		VersionID: versionID, Status: "pending", Role: role, Problems: []importProblem{},
	})
}

func (a api) removeLayerFile(path string) {
	if err := os.Remove(path); err != nil {
		a.logger.Error("Could not remove the failed upload", "error", err)
	}
}

func (a api) layerDraft(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := a.layerAdmin(w, r)
	if !ok {
		return
	}
	versionID, ok := layerVersion(w, r)
	if !ok {
		return
	}

	response := draftResponse{VersionID: versionID}
	err := a.pool.QueryRow(r.Context(), `
		SELECT CASE WHEN v.status = 'draft' THEN j.status ELSE v.status END, l.role, j.source_crs,
			(SELECT count(*) FROM layer_features f WHERE f.layer_version_id = v.id),
			(SELECT count(*) FROM draft_units u WHERE u.layer_version_id = v.id),
			COALESCE((SELECT jsonb_agg(jsonb_build_object(
				'kind', p.kind, 'featureRef', p.feature_ref, 'message', p.message,
				'blocksPublish', p.blocks_publish) ORDER BY p.id)
				FROM layer_import_problems p WHERE p.layer_version_id = v.id), '[]'::jsonb),
			CASE WHEN rm.layer_version_id IS NOT NULL THEN jsonb_build_object(
				'status', CASE WHEN rm.finished_at IS NOT NULL THEN 'completed'
					WHEN rj.state IN ('discarded', 'cancelled') OR rj.id IS NULL THEN 'failed'
					WHEN rj.state = 'running' THEN 'running' ELSE 'pending' END,
				'processed', rm.processed, 'matchesWritten', rm.matches_written, 'alertsCreated', rm.alerts_created
			) END
		FROM layer_versions v JOIN layers l ON l.id = v.layer_id
		JOIN geo_jobs j ON j.layer_version_id = v.id
		LEFT JOIN layer_rematches rm ON rm.layer_version_id = v.id
		LEFT JOIN river_job rj ON rj.id = rm.river_job_id
		WHERE v.id = $1 AND l.workspace_id = $2
	`, versionID, workspaceID).Scan(&response.Status, &response.Role, &response.SourceCRS,
		&response.FeatureCount, &response.UnitCount, &response.Problems, &response.Rematch)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "The draft was not found.")
		return
	}
	if err != nil {
		a.layerImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func saveLayerFile(dir, key string, open func() (multipart.File, error)) (string, error) {
	source, err := open()
	if err != nil {
		return "", err
	}
	defer source.Close()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, key)
	dest, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(dest, source)
	if copyErr == nil {
		copyErr = dest.Sync()
	}
	if err := errors.Join(copyErr, dest.Close()); err != nil {
		return "", errors.Join(err, os.Remove(path))
	}
	return path, nil
}
