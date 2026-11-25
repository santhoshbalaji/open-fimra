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
		SELECT jsonb_build_object(
			'layers', COALESCE((SELECT jsonb_agg(jsonb_build_object(
				'slug', l.slug, 'name', l.name, 'role', l.role, 'hierarchy', h.slug,
				'depth', (v.import_settings->>'depth')::smallint
			) ORDER BY l.name, l.slug) FROM layers l
			LEFT JOIN hierarchies h ON h.id = l.hierarchy_id
			LEFT JOIN layer_versions v ON v.layer_id = l.id AND v.status = 'published'
			WHERE l.workspace_id = $1), '[]'::jsonb),
			'hierarchies', COALESCE((SELECT jsonb_agg(jsonb_build_object(
				'slug', h.slug, 'name', h.name, 'levels', (SELECT jsonb_agg(jsonb_build_object(
					'depth', hl.depth, 'name', hl.labels->>'en') ORDER BY hl.depth)
					FROM hierarchy_levels hl WHERE hl.hierarchy_id = h.id)
			) ORDER BY h.name, h.slug) FROM hierarchies h WHERE h.workspace_id = $1), '[]'::jsonb),
			'uploads', COALESCE((SELECT jsonb_agg(to_jsonb(d) ORDER BY d."createdAt" DESC, d."versionId")
				FROM (SELECT v.id AS "versionId", l.name AS "layerName", v.version,
					v.source_file AS "sourceFile", v.created_at AS "createdAt",
					CASE WHEN v.status = 'draft' THEN j.status ELSE v.status END AS status
					FROM layer_versions v JOIN layers l ON l.id = v.layer_id
					JOIN geo_jobs j ON j.layer_version_id = v.id
					WHERE l.workspace_id = $1 ORDER BY v.created_at DESC, v.id
					LIMIT 100) d), '[]'::jsonb))
	`, workspace).Scan(&catalog)
	if err != nil {
		a.layerImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, catalog)
}
