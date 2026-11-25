WITH batch AS MATERIALIZED (
    SELECT w.id AS workspace_id, d.id, d.observed_at, d.geom
    FROM layer_rematches r
    JOIN layer_versions v ON v.id = r.layer_version_id
    JOIN layers l ON l.id = v.layer_id
    JOIN workspaces w ON w.id = l.workspace_id
    JOIN matchable_detections d ON d.observed_at BETWEEN COALESCE(r.cursor_time, r.window_start) AND r.window_end
    WHERE r.layer_version_id = $1 AND r.finished_at IS NULL
      AND (r.cursor_time IS NULL OR (d.observed_at, d.id) > (r.cursor_time, r.cursor_id))
      AND detection_in_workspace(w, d.geom)
      AND (d.workspace_id IS NULL OR d.workspace_id = w.id)
    ORDER BY d.observed_at, d.id
    LIMIT 250
),
matched AS (
    INSERT INTO detection_matches (workspace_id, detection_id, observed_at, forest_class,
                                   forest_distance_m, forest_feature_id, unit_paths, hierarchy,
                                   hierarchy_unit_paths, triage_reason, layer_version_ids, matched_at)
    SELECT m.workspace_id, m.detection_id, m.observed_at, m.forest_class,
           m.forest_distance_m, m.forest_feature_id, m.unit_paths, m.hierarchy,
           m.hierarchy_unit_paths, m.triage_reason, m.layer_version_ids, clock_timestamp()
    FROM batch b
    CROSS JOIN LATERAL classify_detection(b.workspace_id, b.id, b.observed_at, b.geom) m
    ON CONFLICT (workspace_id, detection_id, observed_at) DO UPDATE SET
        forest_class = EXCLUDED.forest_class,
        forest_distance_m = EXCLUDED.forest_distance_m,
        forest_feature_id = EXCLUDED.forest_feature_id,
        unit_paths = EXCLUDED.unit_paths,
        hierarchy = EXCLUDED.hierarchy,
        triage_reason = EXCLUDED.triage_reason,
        layer_version_ids = EXCLUDED.layer_version_ids,
        hierarchy_unit_paths = EXCLUDED.hierarchy_unit_paths
    RETURNING detection_matches AS match
),
routed AS (
    INSERT INTO alerts (workspace_id, detection_id, observed_at, hierarchy, queue, hierarchy_id, unit_path,
                        hierarchy_unit_paths, unit_paths, forest_class, triage_reason, layer_version_ids)
    SELECT (match).workspace_id, (match).detection_id, (match).observed_at, (match).hierarchy, route.*,
           (match).hierarchy_unit_paths, (match).unit_paths, (match).forest_class, (match).triage_reason,
           (match).layer_version_ids
    FROM matched CROSS JOIN LATERAL alert_route(match) route
    ON CONFLICT (workspace_id, detection_id) DO NOTHING
    RETURNING id
),
progress AS (
    SELECT (SELECT count(*) FROM batch) AS processed, (SELECT count(*) FROM matched) AS matched,
           (SELECT count(*) FROM routed) AS routed
)
UPDATE layer_rematches r
SET cursor_time = COALESCE(tail.observed_at, r.cursor_time),
    cursor_id = COALESCE(tail.id, r.cursor_id),
    processed = r.processed + p.processed,
    matches_written = r.matches_written + p.matched,
    alerts_created = r.alerts_created + p.routed,
    finished_at = CASE WHEN p.processed < 250 THEN clock_timestamp() END
FROM progress p
LEFT JOIN (SELECT id, observed_at FROM batch ORDER BY observed_at DESC, id DESC LIMIT 1) tail ON true
WHERE r.layer_version_id = $1 AND r.finished_at IS NULL
RETURNING r.finished_at IS NOT NULL;
