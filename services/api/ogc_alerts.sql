-- One page of the GIS link's alerts collection. The grant rule is the one in
-- alerts.sql: alert_path_visible() on the alert's own routing snapshot, so the
-- review queues need a grant at division level or above.
-- $1 subject, $2 workspace ID, $3 main hierarchy slug, $4-$7 bounding box
-- (west, south, east, north, or all NULL), $8 start and $9 end of the time
-- window (or NULL), $10 forest classes (or NULL for all), $11 one alert ID
-- (or NULL), $12 and $13 the time and ID of the last row on the previous page
-- (or NULL), $14 rows to return, $15 queue ('' for all), $16 workflow step
-- ('' for all).
WITH grants AS MATERIALIZED (
    SELECT * FROM alert_grants WHERE oidc_subject = $1 AND workspace_id = $2
)
SELECT a.id, a.detection_id, a.queue, i.current_step AS step, s.label AS step_label, i.entered_at AS step_time,
       EXISTS (SELECT FROM workflow_transitions t WHERE t.template_id = i.template_id
               AND t.from_step = i.current_step AND now() > i.entered_at + t.time_limit) AS overdue,
       a.created_at, a.observed_at, d.source, d.product, d.satellite, a.forest_class,
       CASE WHEN a.queue = 'triage' THEN COALESCE(a.triage_reason, 'no_unit') ELSE '' END AS triage,
       d.frp_mw, d.confidence, a.hierarchy -> $3::text AS main,
       COALESCE(line.hierarchy, '') AS place_hierarchy, COALESCE(line.levels::text, '') AS places,
       ST_AsGeoJSON(d.geom, 7) AS geometry
FROM alerts a
JOIN workflow_instances i ON i.alert_id = a.id
JOIN workflow_steps s ON (s.template_id, s.slug) = (i.template_id, i.current_step)
JOIN detections d ON (d.id, d.observed_at) = (a.detection_id, a.observed_at)
LEFT JOIN hierarchies h ON h.id = a.hierarchy_id
LEFT JOIN LATERAL (
    SELECT key AS hierarchy, value AS levels FROM jsonb_each(a.hierarchy)
    ORDER BY (key = h.slug) DESC NULLS LAST, (value ? 'beat') DESC, (value <> '{}'::jsonb) DESC, key LIMIT 1
) line ON true
WHERE a.workspace_id = $2 AND d.source <> 'lsasaf'
  AND ($4::float8 IS NULL OR d.geom && ST_MakeEnvelope($4, $5::float8, $6::float8, $7::float8, 4326))
  AND ($8::timestamptz IS NULL OR (a.observed_at >= $8 AND d.observed_at >= $8))
  AND ($9::timestamptz IS NULL OR (a.observed_at <= $9 AND d.observed_at <= $9))
  AND ($10::text[] IS NULL OR a.forest_class = ANY($10))
  AND ($11::bigint IS NULL OR a.id = $11)
  AND ($12::timestamptz IS NULL OR (a.observed_at, a.id) < ($12, $13::bigint))
  AND ($15::text = '' OR a.queue = $15)
  AND ($16::text = '' OR i.current_step = $16)
  AND EXISTS (
      SELECT 1 FROM grants g
      WHERE alert_path_visible(a.queue, a.hierarchy_unit_paths, g.can_review, g.hierarchy_id, g.unit_path, g.depth)
  )
ORDER BY a.observed_at DESC, a.id DESC
LIMIT $14
