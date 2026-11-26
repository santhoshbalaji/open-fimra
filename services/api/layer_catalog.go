package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (a api) layerCatalog(w http.ResponseWriter, r *http.Request) {
	workspace, ok := a.layerAdmin(w, r)
	if !ok {
		return
	}
	var catalog json.RawMessage
	err := a.pool.QueryRow(r.Context(), `
		WITH versions AS NOT MATERIALIZED (
			SELECT v.id, v.layer_id, v.version, v.created_at, v.published_at, v.retired_at, v.import_settings,
				v.status AS version_status, j.layer_version_id IS NOT NULL AS has_import,
				GREATEST(v.published_at, v.retired_at, v.overlay_from, j.finished_at, v.created_at) AS changed_at,
				jsonb_build_object('versionId', v.id, 'layerSlug', l.slug, 'layerName', l.name, 'version', v.version,
					'sourceFile', v.source_file, 'createdAt', v.created_at,
					'depth', (v.import_settings->>'depth')::smallint, 'overlayKind', v.overlay_kind,
					'status', CASE WHEN v.status = 'draft' THEN j.status ELSE v.status END,
					'blocksPublish', EXISTS (SELECT 1 FROM layer_import_problems p
						WHERE p.layer_version_id = v.id AND p.blocks_publish),
					'hasImport', j.layer_version_id IS NOT NULL,
					'fields', jsonb_strip_nulls(jsonb_build_object('nameField', v.import_settings->>'nameField',
						'codeField', v.import_settings->>'codeField', 'parentCodeField', v.import_settings->>'parentCodeField',
						'sourceLayer', v.import_settings->>'sourceLayer'))) AS summary
			FROM layers l JOIN layer_versions v ON v.layer_id = l.id
			LEFT JOIN geo_jobs j ON j.layer_version_id = v.id
			WHERE l.workspace_id = $1
		)
		SELECT jsonb_build_object(
			'layers', COALESCE((SELECT jsonb_agg(jsonb_build_object(
				'slug', l.slug, 'name', l.name, 'role', l.role, 'hierarchy', h.slug,
				'depth', (live.import_settings->>'depth')::smallint,
				'published', live.summary, 'draft', draft.summary,
				'lastChangedAt', GREATEST(l.created_at, live.changed_at, draft.changed_at),
				'previous', COALESCE((
					SELECT jsonb_agg(p.summary ORDER BY p.version DESC, p.id) FROM versions p
					WHERE (p.layer_id = l.id AND p.version_status = 'retired' AND p.retired_at = live.published_at)
						OR p.id IN (
							SELECT u.layer_version_id FROM units u
							WHERE u.hierarchy_id = l.hierarchy_id AND u.valid_to = live.published_at
								AND u.depth = (live.import_settings->>'depth')::smallint
						)
				), '[]'::jsonb)
			) ORDER BY l.name, l.slug) FROM layers l
			LEFT JOIN hierarchies h ON h.id = l.hierarchy_id
			LEFT JOIN versions live ON live.layer_id = l.id AND live.version_status = 'published'
			LEFT JOIN LATERAL (
				SELECT v.summary, v.changed_at FROM versions v
				WHERE v.layer_id = l.id AND v.version_status = 'draft' AND v.version > COALESCE(live.version, 0)
				ORDER BY v.version DESC LIMIT 1
			) draft ON TRUE
			WHERE l.workspace_id = $1), '[]'::jsonb),
			'hierarchies', COALESCE((SELECT jsonb_agg(jsonb_build_object(
				'slug', h.slug, 'name', h.name, 'levels', (SELECT jsonb_agg(jsonb_build_object(
					'depth', hl.depth, 'name', hl.labels->>'en') ORDER BY hl.depth)
					FROM hierarchy_levels hl WHERE hl.hierarchy_id = h.id)
			) ORDER BY h.name, h.slug) FROM hierarchies h WHERE h.workspace_id = $1), '[]'::jsonb),
			'uploads', COALESCE((SELECT jsonb_agg(d.summary ORDER BY d.created_at DESC, d.id)
				FROM (SELECT id, created_at, summary FROM versions
					WHERE has_import ORDER BY created_at DESC, id LIMIT 100) d), '[]'::jsonb))
	`, workspace).Scan(&catalog)
	if err != nil {
		a.layerImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, catalog)
}

// Marking a published version starts its overlay validity without changing
// any previous observation. Later publications of this layer inherit the kind.
func (a api) markLayerOverlay(w http.ResponseWriter, r *http.Request) {
	workspace, ok := a.layerAdmin(w, r)
	if !ok {
		return
	}
	version, ok := layerVersion(w, r)
	if !ok {
		return
	}
	var request struct {
		Kind string `json:"kind"`
	}
	if !decodeJSON(w, r, 1024, &request) {
		return
	}
	if !slices.Contains([]string{"protected_area", "civil_district", "civil_block", "civil_village", "community", "neighbouring_state"}, request.Kind) {
		writeError(w, http.StatusBadRequest, "Choose an overlay kind.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	caller := ctx.Value(identityKey{}).(identity)
	err := pgx.BeginFunc(ctx, a.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, detectionWriteLock); err != nil {
			return err
		}
		var permitted pgtype.UUID
		err := tx.QueryRow(ctx, layerAdminSQL, chi.URLParam(r, "slug"), caller.Subject).Scan(&permitted)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && permitted != workspace) {
			return publishProblem{http.StatusForbidden, "A state administrator role at the workspace root is required."}
		}
		if err != nil {
			return err
		}

		var role, status string
		var kind *string
		err = tx.QueryRow(ctx, `
			SELECT l.role, v.status, v.overlay_kind FROM layer_versions v JOIN layers l ON l.id = v.layer_id
			WHERE v.id = $1 AND l.workspace_id = $2 FOR UPDATE OF v
		`, version, workspace).Scan(&role, &status, &kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return publishProblem{http.StatusNotFound, "The layer version was not found."}
		}
		if err != nil {
			return err
		}
		if status != "published" || role == "hierarchy" {
			return publishProblem{http.StatusConflict, "Choose a published layer with map features."}
		}
		if kind != nil {
			if *kind == request.Kind {
				return nil
			}
			return publishProblem{http.StatusConflict, "This version already has an overlay kind."}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE layer_versions SET overlay_kind = $2, overlay_from = clock_timestamp() WHERE id = $1
		`, version, request.Kind); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
			VALUES ($1, 'user', $2, 'layer.overlay', 'layer_version', $3, jsonb_build_object('kind', $4::text))
		`, workspace, caller.Subject, version.String(), request.Kind)
		return err
	})
	var problem publishProblem
	switch {
	case errors.As(err, &problem):
		writeError(w, problem.status, problem.message)
	case err != nil:
		a.layerImportError(w, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
