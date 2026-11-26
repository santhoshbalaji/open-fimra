package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
)

//go:embed layer_preview.sql
var previewSQL string

const defaultLayerDays = 30
const maxLayerDays = 90

type layerPublishRequest struct {
	Days              int    `json:"days"`
	GrantConfirmation string `json:"grantConfirmation"`
}

type layerDraftState struct {
	layerID     pgtype.UUID
	hierarchyID pgtype.UUID
	depth       *int16
	status      string
	blocked     string
}

type brokenGrant struct {
	UserID    string    `json:"userId"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	UnitPath  string    `json:"unitPath"`
	GrantedAt time.Time `json:"grantedAt"`
}

func layerVersion(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	var id pgtype.UUID
	err := id.Scan(chi.URLParam(r, "version"))
	if err != nil || !id.Valid {
		writeError(w, http.StatusNotFound, "The layer version was not found.")
		return id, false
	}
	return id, true
}

func readLayerDraft(ctx context.Context, tx pgx.Tx, workspace, version pgtype.UUID) (layerDraftState, error) {
	var draft layerDraftState
	err := tx.QueryRow(ctx, `
		WITH draft AS (
			SELECT l.id AS layer_id, l.hierarchy_id, v.version, j.status,
				(v.import_settings->>'depth')::smallint AS depth,
				(v.import_settings->>'parentVersionId')::uuid AS parent_version
			FROM layer_versions v JOIN layers l ON l.id = v.layer_id
			JOIN geo_jobs j ON j.layer_version_id = v.id
			WHERE v.id = $1 AND l.workspace_id = $2 AND v.status = 'draft'
		),
		previous AS (
			SELECT v.* FROM draft d JOIN layer_versions v ON v.layer_id = d.layer_id AND v.status = 'published'
		)
		SELECT d.layer_id, d.hierarchy_id, d.depth, d.status,
			CASE
			WHEN d.status <> 'ready' THEN 'Wait for a successful import before publishing.'
			WHEN EXISTS (SELECT 1 FROM layer_import_problems WHERE layer_version_id = $1 AND blocks_publish)
				THEN 'Fix the blocking problems and upload a new draft before publishing.'
			WHEN EXISTS (SELECT 1 FROM previous p WHERE p.version >= d.version)
				THEN 'A newer version of this layer is already published.'
			WHEN d.hierarchy_id IS NULL THEN ''
			WHEN d.depth IS NULL OR NOT EXISTS (SELECT 1 FROM draft_units WHERE layer_version_id = $1)
				THEN 'The hierarchy draft must contain units at one level.'
			WHEN d.depth = 0 AND EXISTS (
				SELECT 1 FROM units root WHERE root.hierarchy_id = d.hierarchy_id AND root.depth = 0
					AND root.valid_to IS NULL AND root.layer_version_id IS NULL AND root.geom IS NULL
					AND EXISTS (SELECT 1 FROM draft_units u WHERE u.layer_version_id = $1
						AND u.path IS DISTINCT FROM root.current_path)
			) THEN 'The state code in this file must match the state code chosen during setup. Correct the code and upload again.'
			WHEN EXISTS (SELECT 1 FROM previous p WHERE p.import_settings ? 'depth'
				AND (p.import_settings->>'depth')::smallint <> d.depth)
				THEN 'Use this layer for the same hierarchy level as the published version.'
			WHEN EXISTS (
				SELECT 1 FROM units u JOIN layer_versions v ON v.id = u.layer_version_id
				WHERE u.hierarchy_id = d.hierarchy_id AND u.depth = d.depth AND u.valid_to IS NULL
					AND v.layer_id <> d.layer_id AND v.import_settings ? 'depth'
			) THEN 'Upload a new version of the layer that already owns this hierarchy level.'
			WHEN d.parent_version IS NOT NULL AND NOT EXISTS (
				SELECT 1 FROM layer_versions WHERE id = d.parent_version AND status = 'published'
			) THEN 'Publish the referenced parent draft first, or reimport using the current parents.'
			WHEN EXISTS (
				SELECT 1 FROM draft_units u WHERE u.layer_version_id = $1 AND (
					u.path IS NULL OR u.depth <> d.depth OR (d.depth > 0 AND NOT EXISTS (
						SELECT 1 FROM units p WHERE p.hierarchy_id = d.hierarchy_id
							AND p.current_path = subpath(u.path, 0, d.depth)
							AND (d.parent_version IS NULL OR p.layer_version_id = d.parent_version)
					))
				)
			) THEN 'Some units have no current parent. Check the mapping and upload a new draft.'
			WHEN EXISTS (
				SELECT 1 FROM units child WHERE child.hierarchy_id = d.hierarchy_id
					AND child.valid_to IS NULL AND child.depth = d.depth + 1 AND NOT EXISTS (
						SELECT 1 FROM draft_units u WHERE u.layer_version_id = $1
							AND u.path = subpath(child.path, 0, child.depth)
					)
			) THEN 'The draft would leave current child units without a parent.'
			ELSE '' END
		FROM draft d
	`, version, workspace).Scan(&draft.layerID, &draft.hierarchyID, &draft.depth, &draft.status, &draft.blocked)
	return draft, err
}

func layerBrokenGrants(ctx context.Context, tx pgx.Tx, draft layerDraftState, version pgtype.UUID) ([]brokenGrant, error) {
	rows, err := tx.Query(ctx, `
		SELECT a.user_id::text, usr.username, a.role_slug, a.unit_path::text, a.granted_at
		FROM role_assignments a JOIN users usr ON usr.id = a.user_id
		JOIN units u ON u.hierarchy_id = a.hierarchy_id AND u.current_path = a.unit_path
		WHERE a.hierarchy_id = $1 AND u.depth = $2
			AND NOT EXISTS (SELECT 1 FROM draft_units d WHERE d.layer_version_id = $3 AND d.path = a.unit_path)
		ORDER BY a.user_id, a.role_slug, a.unit_path
	`, draft.hierarchyID, draft.depth, version)
	if err != nil {
		return nil, err
	}
	grants, err := pgx.CollectRows(rows, pgx.RowToStructByPos[brokenGrant])
	if grants == nil {
		grants = []brokenGrant{}
	}
	return grants, err
}

func grantConfirmation(version pgtype.UUID, grants []brokenGrant) string {
	if len(grants) == 0 {
		return ""
	}
	payload, _ := json.Marshal(grants)
	digest := sha256.Sum256(append([]byte(version.String()), payload...))
	return hex.EncodeToString(digest[:])
}

func (a api) layerPreview(w http.ResponseWriter, r *http.Request) {
	workspace, ok := a.layerAdmin(w, r)
	if !ok {
		return
	}
	version, ok := layerVersion(w, r)
	if !ok {
		return
	}
	days := defaultLayerDays
	if raw := r.URL.Query().Get("days"); raw != "" {
		days, _ = strconv.Atoi(raw)
	}
	if days < 1 || days > maxLayerDays {
		writeError(w, http.StatusBadRequest, "Choose a window of 1 to 90 days.")
		return
	}

	start := time.Now().UTC()
	deadline := start.Add(time.Minute)
	_ = http.NewResponseController(w).SetWriteDeadline(deadline)
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		a.layerPublishError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	draft, err := readLayerDraft(ctx, tx, workspace, version)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "The draft was not found.")
		return
	}
	if err != nil {
		a.layerPublishError(w, err)
		return
	}
	if draft.status != "ready" {
		writeError(w, http.StatusConflict, "Wait for a successful import before previewing.")
		return
	}
	grants, err := layerBrokenGrants(ctx, tx, draft, version)
	if err != nil {
		a.layerPublishError(w, err)
		return
	}

	var scanned int64
	var changes json.RawMessage
	err = tx.QueryRow(ctx, previewSQL, workspace, version, start.AddDate(0, 0, -days), start).Scan(&scanned, &changes)
	if err != nil {
		a.layerPublishError(w, err)
		return
	}
	// A legacy hierarchy layer can have supplied this level under a different layer name.
	var previous json.RawMessage
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(jsonb_agg(jsonb_build_object('layerName', l.name, 'version', v.version)
			ORDER BY l.name, v.version), '[]'::jsonb)
		FROM layer_versions v JOIN layers l ON l.id = v.layer_id
		WHERE (v.layer_id = $1 AND v.status = 'published') OR v.id IN (
			SELECT u.layer_version_id FROM units u
			WHERE u.hierarchy_id = $2 AND u.depth = $3 AND u.valid_to IS NULL
		)
	`, draft.layerID, draft.hierarchyID, draft.depth).Scan(&previous)
	if err != nil {
		a.layerPublishError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"days": days, "scanned": scanned, "changes": changes, "blocked": draft.blocked,
		"brokenGrants": grants, "grantConfirmation": grantConfirmation(version, grants), "previous": previous,
	})
}

func (a api) layerPublish(w http.ResponseWriter, r *http.Request) {
	workspace, ok := a.layerAdmin(w, r)
	if !ok {
		return
	}
	version, ok := layerVersion(w, r)
	if !ok {
		return
	}

	request := layerPublishRequest{Days: defaultLayerDays}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "Send a publish request as JSON.")
		return
	}
	if request.Days < 1 || request.Days > maxLayerDays {
		writeError(w, http.StatusBadRequest, "Choose a window of 1 to 90 days.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	caller := ctx.Value(identityKey{}).(identity)
	err := pgx.BeginFunc(ctx, a.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, detectionWriteLock); err != nil {
			return fmt.Errorf("lock detection writes: %w", err)
		}
		var permitted pgtype.UUID
		err := tx.QueryRow(ctx, layerAdminSQL, chi.URLParam(r, "slug"), caller.Subject).Scan(&permitted)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && permitted != workspace) {
			return publishProblem{http.StatusForbidden, "A state administrator role at the workspace root is required."}
		}
		if err != nil {
			return fmt.Errorf("check layer publish permission: %w", err)
		}
		draft, err := readLayerDraft(ctx, tx, workspace, version)
		if errors.Is(err, pgx.ErrNoRows) {
			return publishProblem{http.StatusNotFound, "The draft was not found."}
		}
		if err != nil {
			return fmt.Errorf("load draft for publish: %w", err)
		}
		if draft.blocked != "" {
			return publishProblem{http.StatusConflict, draft.blocked}
		}
		grants, err := layerBrokenGrants(ctx, tx, draft, version)
		if err != nil {
			return fmt.Errorf("check grants before publish: %w", err)
		}
		if confirmation := grantConfirmation(version, grants); confirmation != request.GrantConfirmation {
			return publishProblem{http.StatusConflict, "Review the preview and confirm the current list of grants to remove."}
		}
		return a.commitLayerPublish(ctx, tx, workspace, version, draft, request.Days, grants, caller.Subject)
	})
	if err != nil {
		var problem publishProblem
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &problem):
			writeError(w, problem.status, problem.message)
		case errors.As(err, &pgErr) && pgErr.ConstraintName == "role_assignments_current_unit_fkey":
			a.logger.Warn("A grant changed during layer publishing", "error", err)
			writeError(w, http.StatusConflict, "Grants changed during publishing. Run the preview again.")
		default:
			a.layerPublishError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"versionId": version, "status": "published"})
}

type publishProblem struct {
	status  int
	message string
}

func (p publishProblem) Error() string { return p.message }

func (a api) commitLayerPublish(ctx context.Context, tx pgx.Tx, workspace, version pgtype.UUID, draft layerDraftState,
	days int, grants []brokenGrant, actor string,
) error {
	// Set the effective time after acquiring the write lock. A waiting
	// transaction may have begun before the preceding version was published.
	var effective time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&effective); err != nil {
		return err
	}
	if len(grants) != 0 {
		removed, err := tx.Exec(ctx, `
			DELETE FROM role_assignments a USING jsonb_to_recordset($2::jsonb) AS g(
				"userId" uuid, role text, "unitPath" ltree, "grantedAt" timestamptz)
			WHERE a.hierarchy_id = $1 AND a.user_id = g."userId" AND a.role_slug = g.role
				AND a.unit_path = g."unitPath" AND a.granted_at = g."grantedAt"
		`, draft.hierarchyID, grants)
		if err != nil {
			return fmt.Errorf("remove confirmed grants: %w", err)
		}
		if removed.RowsAffected() != int64(len(grants)) {
			return publishProblem{http.StatusConflict, "Grants changed during publishing. Run the preview again."}
		}
	}
	if draft.hierarchyID.Valid {
		_, err := tx.Exec(ctx, `
			UPDATE units SET valid_to = $3 WHERE hierarchy_id = $1 AND depth = $2 AND valid_to IS NULL
		`, draft.hierarchyID, draft.depth, effective)
		if err != nil {
			return fmt.Errorf("close previous units: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO units (hierarchy_id, depth, code, name, path, geom, layer_version_id, valid_from)
			SELECT $2, depth, code, name, path, geom, layer_version_id, $3
			FROM draft_units WHERE layer_version_id = $1
		`, version, draft.hierarchyID, effective)
		if err != nil {
			return fmt.Errorf("open new units: %w", err)
		}
		// The top level of the hierarchy the wizard built is the workspace
		// boundary, so detections are matched inside the published state.
		_, err = tx.Exec(ctx, `
			UPDATE workspaces w SET boundary = shape.geom, updated_at = now()
			FROM (SELECT ST_Multi(ST_Union(geom)) AS geom FROM draft_units WHERE layer_version_id = $1) shape
			WHERE w.id = $2 AND $4 = 0 AND shape.geom IS NOT NULL
				AND ST_GeometryType(shape.geom) = 'ST_MultiPolygon'
				AND EXISTS (SELECT 1 FROM workspace_setup s WHERE s.workspace_id = w.id AND s.hierarchy_id = $3)
		`, version, workspace, draft.hierarchyID, draft.depth)
		if err != nil {
			return fmt.Errorf("set the workspace boundary: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE layer_versions SET status = 'retired', retired_at = $2 WHERE layer_id = $1 AND status = 'published'
	`, draft.layerID, effective); err != nil {
		return fmt.Errorf("retire previous layer version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE layer_versions SET status = 'published', published_at = $2 WHERE id = $1
	`, version, effective); err != nil {
		return fmt.Errorf("publish draft: %w", err)
	}
	job, err := a.jobs.InsertTx(ctx, tx, rematchArgs{VersionID: version.String()}, &river.InsertOpts{Queue: rematchQueue})
	if err != nil {
		return fmt.Errorf("queue re-match: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO layer_rematches (layer_version_id, river_job_id, window_start, window_end)
		VALUES ($1, $2, $3, $4)
	`, version, job.Job.ID, effective.UTC().AddDate(0, 0, -days), effective); err != nil {
		return fmt.Errorf("save the re-match window: %w", err)
	}
	details := map[string]any{"removedGrants": grants, "replayDays": days, "replayJobId": job.Job.ID}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
		VALUES ($1, 'user', $2, 'layer.publish', 'layer_version', $3, $4)
	`, workspace, actor, version.String(), details)
	if err != nil {
		return fmt.Errorf("audit layer publish: %w", err)
	}
	return nil
}

func (a api) layerPublishError(w http.ResponseWriter, err error) {
	a.logger.Error("Could not process layer publishing", "error", err)
	writeError(w, http.StatusInternalServerError, "Could not process layer publishing.")
}
