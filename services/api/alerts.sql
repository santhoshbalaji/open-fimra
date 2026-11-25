WITH grants AS MATERIALIZED (
    SELECT * FROM alert_grants WHERE oidc_subject=$1 AND slug=$2
),
visible AS (
    SELECT a.id, a.queue, a.workspace_id, a.observed_at, a.detection_id
    FROM alerts a JOIN workflow_instances i ON i.alert_id = a.id
    WHERE EXISTS (
        SELECT 1 FROM grants g WHERE g.workspace_id = a.workspace_id
          AND alert_path_visible(a.queue, a.hierarchy_unit_paths, g.can_review, g.hierarchy_id, g.unit_path, g.depth)
    )
      AND ($3::bigint IS NULL OR a.id = $3)
      AND ($3 IS NOT NULL OR a.observed_at BETWEEN now() - make_interval(hours => $4) AND now())
      AND ($6 = '' OR i.current_step = $6)
      AND ($7 = '' OR a.forest_class = $7)
      AND ($8::ltree IS NULL OR a.unit_paths <@ $8)
      AND ($12::bigint IS NULL OR a.detection_id = $12)
),
page AS (
    SELECT * FROM visible a
    WHERE ($5 = '' OR a.queue = $5)
      AND ($9::timestamptz IS NULL OR (a.observed_at, a.id) < ($9, $10::bigint))
    ORDER BY a.observed_at DESC, a.id DESC LIMIT $11
),
details AS (
    SELECT p.id::text AS id, p.detection_id::text AS detection_id, p.queue, i.current_step AS status, s.label AS step_label, i.entered_at AS step_time,
           EXISTS (SELECT FROM workflow_transitions t WHERE t.template_id=i.template_id
                   AND t.from_step=i.current_step AND now() > i.entered_at+t.time_limit) AS overdue, a.created_at,
           a.hierarchy_id, a.unit_path::text, a.hierarchy, p.observed_at AS observed_time,
           floor(extract(epoch FROM p.observed_at))::bigint AS observed_at,
           ST_X(d.geom) AS longitude, ST_Y(d.geom) AS latitude,
           a.forest_class, CASE WHEN p.queue = 'triage' THEN COALESCE(a.triage_reason, 'no_unit') END AS triage,
           d.source, d.product, d.confidence, d.frp_mw,
           line.levels #>> '{beat,name}' AS beat_name, line.levels #>> '{beat,code}' AS beat_code,
           line.levels #>> '{range,name}' AS range_name, line.levels #>> '{division,name}' AS division_name
    FROM page p JOIN alerts a ON a.id = p.id
    JOIN workflow_instances i ON i.alert_id=a.id
    JOIN workflow_steps s ON (s.template_id,s.slug)=(i.template_id,i.current_step)
    JOIN detections d ON (d.id, d.observed_at) = (p.detection_id, p.observed_at)
    LEFT JOIN hierarchies h ON h.id = a.hierarchy_id
    LEFT JOIN LATERAL (
        SELECT value AS levels FROM jsonb_each(a.hierarchy)
        ORDER BY (key = h.slug) DESC, key LIMIT 1
    ) line ON true
)
SELECT EXISTS (SELECT 1 FROM grants), EXISTS (SELECT 1 FROM grants WHERE can_review),
       $6 = '' OR EXISTS (
           SELECT FROM workflow_steps s JOIN workflow_templates t ON t.id = s.template_id
           WHERE s.slug = $6 AND (t.workspace_id IS NULL OR t.workspace_id IN (SELECT workspace_id FROM grants))
       ),
       (SELECT COALESCE(jsonb_object_agg(queue, n), '{}') FROM
           (SELECT queue, count(*) AS n FROM visible GROUP BY queue) counts),
       COALESCE((SELECT jsonb_agg(to_jsonb(details) ORDER BY observed_time DESC, id::bigint DESC)
                 FROM details), '[]')
