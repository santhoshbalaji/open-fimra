-- Incidents the caller may see: those with at least one member alert that the
-- alert queue would show them. Every count, time, place and point comes from
-- those visible members only. A merged incident reads through the incident it
-- merged into, and only its own ID is listed as merged.
WITH grants AS MATERIALIZED (
    SELECT * FROM alert_grants WHERE oidc_subject=$1 AND slug=$2
),
candidates AS MATERIALIZED (
    SELECT i.* FROM incidents i
    WHERE i.workspace_id IN (SELECT workspace_id FROM grants)
      AND CASE WHEN $3::bigint IS NULL
               THEN i.merged_into IS NULL AND i.last_observed_at >= now() - make_interval(hours => $4)
               ELSE i.id = $3 END
),
members AS MATERIALIZED (
    SELECT c.id AS incident_id, a.id, a.queue, a.observed_at, a.forest_class, a.unit_paths, a.hierarchy,
           a.hierarchy_id, a.detection_id
    FROM candidates c
    JOIN incident_members m ON m.incident_id = COALESCE(c.merged_into, c.id)
    JOIN alerts a ON a.id = m.alert_id
    WHERE EXISTS (
        SELECT 1 FROM grants g WHERE g.workspace_id = a.workspace_id
          AND alert_path_visible(a.queue, a.hierarchy_unit_paths, g.can_review, g.hierarchy_id, g.unit_path, g.depth)
    )
),
visible AS (
    SELECT m.incident_id AS id, max(m.observed_at) AS last_time
    FROM members m
    GROUP BY m.incident_id
    HAVING ($3::bigint IS NOT NULL OR max(m.observed_at) BETWEEN now() - make_interval(hours => $4) AND now())
       AND ($5 = '' OR bool_or(m.queue = $5))
       AND ($7 = '' OR bool_or(m.forest_class = $7))
       AND ($8::ltree IS NULL OR bool_or(m.unit_paths <@ $8))
),
filtered AS (
    SELECT v.id, v.last_time FROM visible v
    JOIN workflow_instances wi ON wi.incident_id = v.id
    WHERE $6 = '' OR wi.current_step = $6
),
page AS (
    SELECT * FROM filtered
    WHERE $9::timestamptz IS NULL OR (last_time, id) < ($9, $10::bigint)
    ORDER BY last_time DESC, id DESC LIMIT $11
),
member_places AS (
    SELECT m.*, ST_X(d.geom) AS longitude, ST_Y(d.geom) AS latitude,
           line.levels #>> '{beat,name}' AS beat_name, line.levels #>> '{division,name}' AS division_name
    FROM members m JOIN page p ON p.id = m.incident_id
    JOIN detections d ON (d.id, d.observed_at) = (m.detection_id, m.observed_at)
    LEFT JOIN hierarchies h ON h.id = m.hierarchy_id
    LEFT JOIN LATERAL (
        SELECT value AS levels FROM jsonb_each(m.hierarchy)
        ORDER BY (key = h.slug) DESC, key LIMIT 1
    ) line ON true
),
details AS (
    SELECT p.id::text AS id, wi.current_step AS status, s.label AS step_label, wi.entered_at AS step_time,
           s.terminal,
           EXISTS (SELECT FROM workflow_transitions t WHERE t.template_id=wi.template_id
                   AND t.from_step=wi.current_step AND now() > wi.entered_at+t.time_limit) AS overdue,
           i.created_at, i.merged_into::text AS merged_into, i.merged_at,
           p.last_time AS last_observed_time,
           floor(extract(epoch FROM p.last_time))::bigint AS last_observed_at,
           (SELECT floor(extract(epoch FROM min(observed_at)))::bigint FROM member_places WHERE incident_id = p.id) AS first_observed_at,
           (SELECT count(*) FROM member_places WHERE incident_id = p.id) AS alert_count,
           (SELECT jsonb_object_agg(queue, n) FROM (SELECT queue, count(*) AS n FROM member_places
                WHERE incident_id = p.id GROUP BY queue) q) AS queues,
           (SELECT jsonb_object_agg(forest_class, n) FROM (SELECT forest_class, count(*) AS n FROM member_places
                WHERE incident_id = p.id GROUP BY forest_class) c) AS classes,
           (SELECT COALESCE(jsonb_agg(DISTINCT beat_name ORDER BY beat_name), '[]') FROM member_places
                WHERE incident_id = p.id AND beat_name IS NOT NULL) AS beat_names,
           (SELECT COALESCE(jsonb_agg(DISTINCT division_name ORDER BY division_name), '[]') FROM member_places
                WHERE incident_id = p.id AND division_name IS NOT NULL) AS division_names,
           (SELECT jsonb_build_array(min(longitude), min(latitude), max(longitude), max(latitude))
                FROM member_places WHERE incident_id = p.id) AS bounds
    FROM page p JOIN incidents i ON i.id = p.id
    JOIN workflow_instances wi ON wi.incident_id = p.id
    JOIN workflow_steps s ON (s.template_id, s.slug) = (wi.template_id, wi.current_step)
)
SELECT EXISTS (SELECT 1 FROM grants),
       $6 = '' OR EXISTS (
           SELECT FROM workflow_steps s JOIN workflow_templates t ON t.id = s.template_id
           WHERE s.slug = $6 AND (t.workspace_id IS NULL OR t.workspace_id IN (SELECT workspace_id FROM grants))
       ),
       (SELECT count(*) FROM filtered),
       COALESCE((SELECT jsonb_agg(to_jsonb(details) ORDER BY last_observed_time DESC, id::bigint DESC)
                 FROM details), '[]')
