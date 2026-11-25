WITH candidates AS MATERIALIZED (
    SELECT d.id, d.observed_at, d.geom
    FROM matchable_detections d JOIN workspaces w ON w.id = $1
    WHERE d.observed_at BETWEEN $3 AND $4 AND detection_in_workspace(w, d.geom)
),
compared AS (
    SELECT d.id, d.observed_at,
           live.forest_class AS old_class, draft.forest_class AS new_class,
           live.triage_reason AS old_triage, draft.triage_reason AS new_triage,
           old_beats.paths AS old_beats, new_beats.paths AS new_beats,
           old_route.queue AS old_queue, new_route.queue AS new_queue
    FROM candidates d
    CROSS JOIN LATERAL classify_detection($1, d.id, d.observed_at, d.geom) live
    CROSS JOIN LATERAL classify_detection($1, d.id, d.observed_at, d.geom, $2) draft
    LEFT JOIN LATERAL alert_route(live) old_route ON true
    LEFT JOIN LATERAL alert_route(draft) new_route ON true
    CROSS JOIN LATERAL (
        SELECT array_agg(h.slug || '/' || p.path ORDER BY h.slug, p.path) AS paths
        FROM hierarchies h JOIN hierarchy_levels l ON l.hierarchy_id = h.id AND l.slug = 'beat'
        CROSS JOIN LATERAL jsonb_array_elements_text(live.hierarchy_unit_paths -> h.id::text) p(path)
        WHERE h.workspace_id = $1 AND nlevel(p.path::ltree) = l.depth + 1
    ) old_beats
    CROSS JOIN LATERAL (
        SELECT array_agg(h.slug || '/' || p.path ORDER BY h.slug, p.path) AS paths
        FROM hierarchies h JOIN hierarchy_levels l ON l.hierarchy_id = h.id AND l.slug = 'beat'
        CROSS JOIN LATERAL jsonb_array_elements_text(draft.hierarchy_unit_paths -> h.id::text) p(path)
        WHERE h.workspace_id = $1 AND nlevel(p.path::ltree) = l.depth + 1
    ) new_beats
),
changes AS (
    SELECT category, c.*, row_number() OVER (PARTITION BY category ORDER BY observed_at DESC, id DESC) AS rank
    FROM compared c
    CROSS JOIN LATERAL (VALUES
        ('included', old_queue IS NULL AND new_queue IS NOT NULL),
        ('excluded', old_queue IS NOT NULL AND new_queue IS NULL),
        ('reassigned', old_class IS DISTINCT FROM new_class OR old_beats IS DISTINCT FROM new_beats),
        ('triage', old_triage IS DISTINCT FROM new_triage)
    ) kinds(category, changed)
    WHERE changed
)
SELECT (SELECT count(*) FROM candidates),
       COALESCE(jsonb_object_agg(category, jsonb_build_object('count', n, 'items', examples)), '{}')
FROM (
    SELECT category, count(*) AS n,
           jsonb_agg(jsonb_build_object(
               'detectionId', id::text, 'observedAt', observed_at,
               'oldClass', old_class, 'newClass', new_class,
               'oldBeats', COALESCE(old_beats, '{}'), 'newBeats', COALESCE(new_beats, '{}'),
               'oldTriage', old_triage, 'newTriage', new_triage
           ) ORDER BY rank) FILTER (WHERE rank <= 20) AS examples
    FROM changes GROUP BY category
) groups;
