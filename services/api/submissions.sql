-- The field reports a caller may see, newest first. A submission is seen
-- through its alert, with the alert queue's rule in alerts.sql: the alert's
-- own routing snapshot under a grant, and review queues only for grants at
-- division level or above. A supervising officer on the alert (a state
-- administrator, or a division officer at division level or above) can
-- approve or return it, as in alert_roles().
-- $1 subject, $2 workspace slug, $3 hours, $4 queue, $5 alert step, $6 forest class,
-- $7 unit path prefix (or NULL), $8 form key, $9 decision (pending, approved,
-- returned or ''), $10 alert ID (or NULL), $11 cursor time, $12 cursor ID,
-- $13 page size plus one, $14 oldest first.
WITH grants AS MATERIALIZED (
    SELECT * FROM alert_grants WHERE oidc_subject = $1 AND slug = $2
),
visible AS (
    SELECT s.id, s.received_at, s.alert_id, s.form_version_id, s.step, s.template_id,
           COALESCE(ap.decision, 'pending') AS decision
    FROM submissions s
    JOIN alerts a ON a.id = s.alert_id
    JOIN workflow_instances i ON i.alert_id = a.id
    JOIN form_versions v ON v.id = s.form_version_id
    JOIN forms f ON f.id = v.form_id
    LEFT JOIN approvals ap ON ap.submission_id = s.id
    WHERE s.workspace_id IN (SELECT workspace_id FROM grants)
      AND EXISTS (
          SELECT 1 FROM grants g WHERE g.workspace_id = a.workspace_id
            AND alert_path_visible(a.queue, a.hierarchy_unit_paths, g.can_review, g.hierarchy_id, g.unit_path, g.depth)
      )
      -- An alert's own reports are listed whatever their age, like its detail.
      AND ($10::bigint IS NULL OR s.alert_id = $10)
      AND ($10 IS NOT NULL OR s.received_at >= now() - make_interval(hours => $3))
      AND ($4 = '' OR a.queue = $4)
      AND ($5 = '' OR i.current_step = $5)
      AND ($6 = '' OR a.forest_class = $6)
      AND ($7::ltree IS NULL OR a.unit_paths <@ $7)
      AND ($8 = '' OR f.form_key = $8)
),
page AS (
    SELECT * FROM visible
    WHERE ($9 = '' OR decision = $9)
      AND ($11::timestamptz IS NULL OR
          CASE WHEN $14::boolean THEN (received_at, id) > ($11, $12::uuid)
               ELSE (received_at, id) < ($11, $12::uuid) END)
    ORDER BY CASE WHEN $14 THEN received_at END, CASE WHEN $14 THEN id END,
             received_at DESC, id DESC LIMIT $13
),
details AS (
    SELECT p.id, f.form_key AS form, v.version, v.definition->'title' AS title, p.step, st.label AS step_label,
           p.alert_id::text AS alert_id, s.incident_id::text AS incident_id,
           i.current_step AS alert_status, cur.label AS alert_step_label,
           a.queue, a.forest_class, floor(extract(epoch FROM a.observed_at))::bigint AS observed_at,
           line.levels #>> '{beat,name}' AS beat_name, line.levels #>> '{range,name}' AS range_name,
           line.levels #>> '{division,name}' AS division_name,
           u.display_name AS submitted_by, p.received_at AS received_time, s.device_time,
           p.decision, ap.decided_at, au.display_name AS decided_by, ap.note AS decision_note,
           EXISTS (
               SELECT 1 FROM grants g WHERE g.workspace_id = a.workspace_id
                 AND (g.role_slug = 'state_admin' OR (g.role_slug = 'division_officer' AND g.can_review))
                 AND alert_path_visible(a.queue, a.hierarchy_unit_paths, g.can_review, g.hierarchy_id, g.unit_path, g.depth)
           ) AS can_approve
    FROM page p
    JOIN submissions s ON s.id = p.id
    JOIN alerts a ON a.id = p.alert_id
    JOIN workflow_instances i ON i.alert_id = a.id
    JOIN workflow_steps cur ON (cur.template_id, cur.slug) = (i.template_id, i.current_step)
    JOIN workflow_steps st ON (st.template_id, st.slug) = (p.template_id, p.step)
    JOIN form_versions v ON v.id = p.form_version_id
    JOIN forms f ON f.id = v.form_id
    JOIN users u ON u.id = s.user_id
    LEFT JOIN approvals ap ON ap.submission_id = p.id
    LEFT JOIN users au ON au.id = ap.user_id
    LEFT JOIN hierarchies h ON h.id = a.hierarchy_id
    LEFT JOIN LATERAL (
        SELECT value AS levels FROM jsonb_each(a.hierarchy)
        ORDER BY (key = h.slug) DESC, key LIMIT 1
    ) line ON true
)
SELECT EXISTS (SELECT 1 FROM grants),
       $5 = '' OR EXISTS (
           SELECT FROM workflow_steps s JOIN workflow_templates t ON t.id = s.template_id
           WHERE s.slug = $5 AND (t.workspace_id IS NULL OR t.workspace_id IN (SELECT workspace_id FROM grants))
       ),
       (SELECT COALESCE(jsonb_object_agg(decision, n), '{}') FROM
           (SELECT decision, count(*) AS n FROM visible GROUP BY decision) counts),
       COALESCE((SELECT jsonb_agg(to_jsonb(details) ORDER BY CASE WHEN $14 THEN received_time END, CASE WHEN $14 THEN id END, received_time DESC, id DESC) FROM details), '[]')
