package main

import (
	"encoding/json"
	"net/http"
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
				GREATEST(v.published_at, v.retired_at, j.finished_at, v.created_at) AS changed_at,
				jsonb_build_object('versionId', v.id, 'layerSlug', l.slug, 'layerName', l.name, 'version', v.version,
					'sourceFile', v.source_file, 'createdAt', v.created_at,
					'depth', (v.import_settings->>'depth')::smallint,
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
