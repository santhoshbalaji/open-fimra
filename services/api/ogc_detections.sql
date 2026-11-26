-- One page of the GIS link's detections collection. The grant rule is the one
-- in tiles.sql: the alert_grants view and match_under_grant() on the match's
-- paths. A grant at a hierarchy root covers every match in the workspace.
-- $1 subject, $2 workspace ID, $3 main hierarchy slug, $4-$7 bounding box
-- (west, south, east, north, or all NULL), $8 start and $9 end of the time
-- window (or NULL), $10 forest classes (or NULL for all), $11 one detection
-- ID (or NULL), $12 and $13 the time and ID of the last row on the previous
-- page (or NULL), $14 rows to return.
WITH grants AS MATERIALIZED (
    SELECT * FROM alert_grants WHERE oidc_subject = $1 AND workspace_id = $2
)
SELECT d.id, d.observed_at, d.source, d.product, d.satellite, m.forest_class,
       COALESCE(m.triage_reason, '') AS triage, f.code AS forest_feature_code, d.frp_mw, d.confidence,
       m.hierarchy -> $3::text AS main, COALESCE(line.hierarchy, '') AS place_hierarchy,
       COALESCE(line.levels::text, '') AS places, ST_AsGeoJSON(d.geom, 7) AS geometry
FROM detection_matches m
JOIN detections d ON (d.id, d.observed_at) = (m.detection_id, m.observed_at)
LEFT JOIN alerts routed ON (routed.workspace_id, routed.detection_id) = (m.workspace_id, m.detection_id)
LEFT JOIN layer_features f ON f.id = m.forest_feature_id
LEFT JOIN LATERAL (
    SELECT key AS hierarchy, value AS levels FROM jsonb_each(m.hierarchy) ORDER BY key LIMIT 1
) line ON true
WHERE m.workspace_id = $2
  AND ($4::float8 IS NULL OR d.geom && ST_MakeEnvelope($4, $5::float8, $6::float8, $7::float8, 4326))
  AND ($8::timestamptz IS NULL OR (m.observed_at >= $8 AND d.observed_at >= $8))
  AND ($9::timestamptz IS NULL OR (m.observed_at <= $9 AND d.observed_at <= $9))
  AND ($10::text[] IS NULL OR m.forest_class = ANY($10))
  AND ($11::bigint IS NULL OR m.detection_id = $11)
  AND ($12::timestamptz IS NULL OR (m.observed_at, m.detection_id) < ($12, $13::bigint))
  AND EXISTS (
      SELECT 1 FROM grants g
      WHERE (match_under_grant(m.hierarchy_unit_paths, g.hierarchy_id, g.unit_path, g.depth)
                  OR dispatch_detection_visible(routed, g.can_review, g.hierarchy_id, g.unit_path, g.depth))
  )
ORDER BY m.observed_at DESC, m.detection_id DESC
LIMIT $14
