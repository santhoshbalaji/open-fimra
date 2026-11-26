package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Read the checked geometry saved by the worker. Limit the preview separately
// from the import so a large layer stays usable on a slow connection.
func (a api) layerPreviewMap(w http.ResponseWriter, r *http.Request) {
	workspace, ok := a.layerAdmin(w, r)
	if !ok {
		return
	}
	version, ok := layerVersion(w, r)
	if !ok {
		return
	}
	var preview json.RawMessage
	err := a.pool.QueryRow(r.Context(), `
		WITH draft AS (
			SELECT v.id, w.boundary FROM layer_versions v
			JOIN layers l ON l.id = v.layer_id JOIN workspaces w ON w.id = l.workspace_id
			JOIN geo_jobs j ON j.layer_version_id = v.id
			WHERE v.id = $1 AND w.id = $2 AND v.status = 'draft' AND j.status = 'ready'
		), shapes AS (
			SELECT f.id AS position, f.geom FROM draft d JOIN layer_features f ON f.layer_version_id = d.id
			UNION ALL
			SELECT u.feature_number, u.geom FROM draft d JOIN draft_units u ON u.layer_version_id = d.id
		), sample AS (
			SELECT position, geom FROM shapes ORDER BY position LIMIT 251
		), simplified AS (
			SELECT position, ST_SimplifyPreserveTopology(geom, 0.0001) AS geom
			FROM sample ORDER BY position LIMIT 250
		), bounded AS (
			SELECT position, geom, SUM(ST_NPoints(geom)) OVER (ORDER BY position) AS points
			FROM simplified WHERE ST_NPoints(geom) <= 10000
		)
		SELECT jsonb_build_object(
			'boundary', CASE WHEN d.boundary IS NOT NULL THEN
				ST_AsGeoJSON(ST_SimplifyPreserveTopology(d.boundary, 0.001), 6)::jsonb END,
			'truncated', (SELECT count(*) FROM sample) > count(b.position),
			'data', jsonb_build_object('type', 'FeatureCollection', 'features',
				COALESCE(jsonb_agg(jsonb_build_object('type', 'Feature', 'properties', '{}'::jsonb,
					'geometry', ST_AsGeoJSON(b.geom, 6)::jsonb) ORDER BY b.position)
					FILTER (WHERE b.position IS NOT NULL), '[]'::jsonb))
		)
		FROM draft d LEFT JOIN bounded b ON b.points <= 30000
		GROUP BY d.id, d.boundary
	`, version, workspace).Scan(&preview)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "No checked draft geometry is available for this version.")
		return
	}
	if err != nil {
		a.layerImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}
