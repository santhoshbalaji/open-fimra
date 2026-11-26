WITH grants AS MATERIALIZED (
    SELECT * FROM alert_grants WHERE oidc_subject=$1 AND slug=$2
),
visible AS (
    SELECT a.id, a.queue, a.workspace_id, a.observed_at, a.detection_id, deadline.due_at,
           CASE WHEN deadline.due_at < now() THEN deadline.due_at
                ELSE '9999-12-31 00:00:00+00'::timestamptz END AS overdue_sort_time
    FROM alerts a JOIN workflow_instances i ON i.alert_id = a.id
    CROSS JOIN LATERAL (SELECT alert_due_at(i.template_id, i.current_step, i.entered_at) AS due_at) deadline
    WHERE EXISTS (
        SELECT 1 FROM grants g WHERE g.workspace_id = a.workspace_id
          AND alert_path_visible(a.queue, a.hierarchy_unit_paths, g.can_review, g.hierarchy_id, g.unit_path, g.depth)
    )
      AND ($3::bigint IS NULL OR a.id = $3)
      -- An incident's members are listed whatever their age, like a detail.
      AND ($13::bigint IS NULL OR a.id IN (
          SELECT m.alert_id FROM incidents i JOIN incident_members m ON m.incident_id = COALESCE(i.merged_into, i.id)
          WHERE i.id = $13))
      AND ($3 IS NOT NULL OR $13 IS NOT NULL OR (a.observed_at <= now() AND ($4 = 0 OR a.observed_at >= now() - make_interval(hours => $4))))
      AND ($6 = '' OR i.current_step = $6)
      AND ($7 = '' OR a.forest_class = $7)
      AND ($8::ltree IS NULL OR a.unit_paths <@ $8)
      AND ($12::bigint IS NULL OR a.detection_id = $12)
      -- Match the dashboard counts across queues before paging.
      AND CASE $14
          WHEN 'active' THEN EXISTS (SELECT FROM workflow_steps s
              WHERE (s.template_id, s.slug) = (i.template_id, i.current_step) AND NOT s.terminal)
          WHEN 'overdue' THEN deadline.due_at < now()
          WHEN 'pending_reports' THEN EXISTS (SELECT FROM workflow_steps s
              WHERE (s.template_id, s.slug) = (i.template_id, i.current_step) AND s.report_pending)
          ELSE true
      END
),
page AS (
    SELECT * FROM visible a
    WHERE ($5 = '' OR a.queue = $5)
      AND ($9::timestamptz IS NULL OR CASE $15::text
          WHEN 'overdue' THEN (a.overdue_sort_time, a.id) > ($9, $10::bigint)
          WHEN 'oldest' THEN (a.observed_at, a.id) > ($9, $10::bigint)
          ELSE (a.observed_at, a.id) < ($9, $10::bigint) END)
    ORDER BY CASE $15 WHEN 'overdue' THEN a.overdue_sort_time WHEN 'oldest' THEN a.observed_at END,
             CASE WHEN $15 IN ('oldest', 'overdue') THEN a.id END,
             a.observed_at DESC, a.id DESC LIMIT $11
),
details AS (
    SELECT p.id::text AS id, p.detection_id::text AS detection_id, p.queue, i.current_step AS status, s.label AS step_label, i.entered_at AS step_time,
           COALESCE(p.due_at < now(), false) AS overdue, p.due_at, p.overdue_sort_time, a.created_at,
           a.assignment - 'auditId' AS assignment,
           p.queue <> 'unit' AND EXISTS (SELECT FROM grants g WHERE can_dispatch_alert(a, g)) AS can_assign,
           a.original_dispatch - 'candidatePaths' AS original_dispatch, a.dispatch_lead, a.dispatch_support,
           p.queue = 'unit' AND EXISTS (
               SELECT FROM grants g WHERE can_dispatch_alert(a, g)
           ) AS can_transfer,
           COALESCE((SELECT jsonb_agg(jsonb_build_object('id', r.id::text, 'unit', r.target, 'reason', r.reason,
                    'at', r.requested_at) ORDER BY r.id)
                     FROM alert_support_requests r WHERE r.alert_id = a.id), '[]') AS support_requests,
           a.hierarchy_id, a.unit_path::text, a.hierarchy,
           COALESCE(a.boundary_snapshot->'overlays', '[]') AS overlay_matches,
           COALESCE(a.boundary_snapshot->'layerVersionIds', to_jsonb(a.layer_version_ids)) AS boundary_version_ids, p.observed_at AS observed_time,
           floor(extract(epoch FROM p.observed_at))::bigint AS observed_at,
           ST_X(d.geom) AS longitude, ST_Y(d.geom) AS latitude,
           a.forest_class, CASE WHEN p.queue = 'triage' THEN COALESCE(a.triage_reason, 'no_unit') END AS triage,
           d.source, d.product, d.confidence, d.frp_mw,
           line.levels #>> '{beat,name}' AS beat_name, line.levels #>> '{beat,code}' AS beat_code,
           line.levels #>> '{range,name}' AS range_name, line.levels #>> '{division,name}' AS division_name,
           line.hierarchy AS place_hierarchy, line.levels AS places, m.incident_id::text AS incident_id,
           CASE WHEN p.queue <> 'unit' AND EXISTS (SELECT FROM grants g WHERE can_dispatch_alert(a, g)) THEN 'Assign to unit'
                WHEN s.terminal THEN 'Workflow complete'
                WHEN next_move.label IS NULL THEN 'No action available'
                WHEN EXISTS (SELECT FROM alert_missing_forms(p.id, next_move.step)) THEN 'Complete required reports'
                ELSE 'Next step: ' || next_move.label END AS next_action
    FROM page p JOIN alerts a ON a.id = p.id
    LEFT JOIN incident_members m ON m.alert_id = a.id
    JOIN workflow_instances i ON i.alert_id=a.id
    JOIN workflow_steps s ON (s.template_id,s.slug)=(i.template_id,i.current_step)
    JOIN detections d ON (d.id, d.observed_at) = (p.detection_id, p.observed_at)
    LEFT JOIN hierarchies h ON h.id = a.hierarchy_id
    LEFT JOIN LATERAL (
        SELECT step, label FROM alert_moves($1, $2, p.id) ORDER BY terminal, label, step LIMIT 1
    ) next_move ON true
    LEFT JOIN LATERAL (
        SELECT key AS hierarchy, value AS levels FROM jsonb_each(a.hierarchy)
        ORDER BY (key = h.slug) DESC NULLS LAST, (value ? 'beat') DESC, (value <> '{}'::jsonb) DESC, key LIMIT 1
    ) line ON true
)
SELECT EXISTS (SELECT 1 FROM grants), EXISTS (SELECT 1 FROM grants WHERE can_review),
       $6 = '' OR EXISTS (
           SELECT FROM workflow_steps s JOIN workflow_templates t ON t.id = s.template_id
           WHERE s.slug = $6 AND (t.workspace_id IS NULL OR t.workspace_id IN (SELECT workspace_id FROM grants))
       ),
       (SELECT COALESCE(jsonb_object_agg(queue, n), '{}') FROM
           (SELECT queue, count(*) AS n FROM visible GROUP BY queue) counts),
       COALESCE((SELECT jsonb_agg(to_jsonb(details)
                 ORDER BY CASE $15 WHEN 'overdue' THEN overdue_sort_time WHEN 'oldest' THEN observed_time END,
                          CASE WHEN $15 IN ('oldest', 'overdue') THEN id::bigint END,
                          observed_time DESC, id::bigint DESC)
                 FROM details), '[]')
