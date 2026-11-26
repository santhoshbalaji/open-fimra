-- Freezes the rows the caller may see into a new export, in one statement.
-- The grant rule is the one in tiles.sql for detections and alerts.sql for
-- alerts: the alert_grants view and match_under_grant(), on the match's paths
-- for detections and on the alert's own snapshot for alerts and submissions.
-- Review queues need a grant at division level or above.
-- $1 subject, $2 workspace slug, $3 kind, $4 format, $5 hours, $6 forest classes,
-- $7 queue ('' for all), $8 unit path prefix (or NULL), $9 workflow step ('' for all),
-- $10 maximum rows, $11 lifetime of the file, $12 form key ('' for all),
-- $13 approval decision, $14 alert group ('' for all).
WITH grants AS MATERIALIZED (
    SELECT g.*, u.code, u.name, level.slug AS level
    FROM alert_grants g
    JOIN units u ON u.hierarchy_id = g.hierarchy_id AND u.current_path = g.unit_path
    JOIN hierarchy_levels level ON level.hierarchy_id = u.hierarchy_id AND level.depth = u.depth
    WHERE g.oidc_subject = $1 AND g.slug = $2
),
detection_rows AS (
    SELECT m.workspace_id, m.detection_id, m.observed_at, NULL::bigint AS alert_id,
           NULL::uuid AS submission_id, m.observed_at AS sort_time,
           COALESCE(m.boundary_snapshot, jsonb_build_object(
               'hierarchy', m.hierarchy, 'layerVersionIds', m.layer_version_ids,
               'forestClass', m.forest_class, 'forestFeatureId', m.forest_feature_id,
               'triageReason', m.triage_reason)) AS boundary_snapshot,
           NULL::jsonb AS dispatch
    FROM detection_matches m
    JOIN detections d ON (d.id, d.observed_at) = (m.detection_id, m.observed_at)
    LEFT JOIN alerts routed ON (routed.workspace_id, routed.detection_id) = (m.workspace_id, m.detection_id)
    WHERE $3 = 'detections'
      AND d.observed_at BETWEEN now() - make_interval(hours => $5) AND now()
      AND m.observed_at BETWEEN now() - make_interval(hours => $5) AND now()
      AND m.forest_class = ANY($6::text[])
      AND EXISTS (
          SELECT 1 FROM grants g WHERE g.workspace_id = m.workspace_id AND
              ((($8::ltree IS NULL OR m.unit_paths <@ $8) AND match_under_grant(m.hierarchy_unit_paths, g.hierarchy_id, g.unit_path, g.depth))
              OR (($8::ltree IS NULL OR routed.unit_paths <@ $8)
                  AND dispatch_detection_visible(routed, g.can_review, g.hierarchy_id, g.unit_path, g.depth)))
      )
),
alert_snapshots AS NOT MATERIALIZED (
    SELECT a.*, COALESCE(a.boundary_snapshot, jsonb_build_object(
               'hierarchy', a.hierarchy, 'layerVersionIds', a.layer_version_ids,
               'forestClass', a.forest_class, 'triageReason', a.triage_reason,
               'forestFeatureId', (SELECT m.forest_feature_id FROM detection_matches m
                   WHERE (m.workspace_id, m.detection_id, m.observed_at) = (a.workspace_id, a.detection_id, a.observed_at)))
           ) AS export_boundary,
           (a.original_dispatch - 'candidatePaths') || jsonb_build_object('currentLead', a.dispatch_lead, 'currentSupporting', a.dispatch_support) AS export_dispatch
    FROM alerts a
),
alert_rows AS (
    -- Alerts keep their own routing snapshot, as in alerts.sql.
    SELECT a.workspace_id, a.detection_id, a.observed_at, a.id AS alert_id,
           NULL::uuid AS submission_id, a.observed_at AS sort_time, a.export_boundary, a.export_dispatch
    FROM alert_snapshots a JOIN workflow_instances i ON i.alert_id = a.id
    WHERE $3 = 'alerts'
      AND EXISTS (
          SELECT 1 FROM grants g WHERE g.workspace_id = a.workspace_id
            AND (a.queue = 'unit' OR g.can_review)
            AND match_under_grant(a.hierarchy_unit_paths, g.hierarchy_id, g.unit_path, g.depth)
      )
      AND a.observed_at <= now() AND ($5 = 0 OR a.observed_at >= now() - make_interval(hours => $5))
      AND CASE $14
          WHEN 'overdue' THEN alert_due_at(i.template_id, i.current_step, i.entered_at) < now()
          WHEN 'active' THEN EXISTS (SELECT FROM workflow_steps s
              WHERE (s.template_id, s.slug) = (i.template_id, i.current_step) AND NOT s.terminal)
          WHEN 'pending_reports' THEN EXISTS (SELECT FROM workflow_steps s
              WHERE (s.template_id, s.slug) = (i.template_id, i.current_step) AND s.report_pending)
          ELSE true
      END
      AND ($7 = '' OR a.queue = $7)
      AND ($9 = '' OR i.current_step = $9)
      AND a.forest_class = ANY($6::text[])
      AND ($8::ltree IS NULL OR a.unit_paths <@ $8)
),
submission_rows AS (
    -- A submission is seen through its alert, with the alert's rules. The time
    -- window is when the report arrived; the other filters are the alert's.
    SELECT a.workspace_id, a.detection_id, a.observed_at, a.id AS alert_id,
           s.id AS submission_id, s.received_at AS sort_time, a.export_boundary, a.export_dispatch
    FROM submissions s
    JOIN alert_snapshots a ON a.id = s.alert_id
    JOIN workflow_instances i ON i.alert_id = a.id
    JOIN form_versions v ON v.id = s.form_version_id
    JOIN forms f ON f.id = v.form_id
    LEFT JOIN approvals ap ON ap.submission_id = s.id
    WHERE $3 = 'submissions'
      AND s.workspace_id IN (SELECT workspace_id FROM grants)
      AND EXISTS (
          SELECT 1 FROM grants g WHERE g.workspace_id = a.workspace_id
            AND (a.queue = 'unit' OR g.can_review)
            AND match_under_grant(a.hierarchy_unit_paths, g.hierarchy_id, g.unit_path, g.depth)
      )
      AND s.received_at >= now() - make_interval(hours => $5)
      AND ($7 = '' OR a.queue = $7)
      AND ($9 = '' OR i.current_step = $9)
      AND a.forest_class = ANY($6::text[])
      AND ($8::ltree IS NULL OR a.unit_paths <@ $8)
      AND ($12 = '' OR f.form_key = $12)
      AND ($13 = '' OR COALESCE(ap.decision, 'pending') = $13)
),
permitted AS (
    SELECT row_number() OVER (ORDER BY sort_time DESC, detection_id DESC, alert_id DESC, submission_id DESC)::integer AS ordinal, *
    FROM (SELECT * FROM detection_rows UNION ALL SELECT * FROM alert_rows UNION ALL SELECT * FROM submission_rows) picked
),
total AS (SELECT count(*)::integer AS n FROM permitted),
step AS (
    SELECT ($9 = '' OR EXISTS (
        SELECT FROM workflow_steps s JOIN workflow_templates t ON t.id = s.template_id
        WHERE s.slug = $9 AND (t.workspace_id IS NULL OR t.workspace_id IN (SELECT workspace_id FROM grants))
    )) AND ($12 = '' OR EXISTS (
        SELECT FROM forms f WHERE f.form_key = $12 AND f.workspace_id IN (SELECT workspace_id FROM grants)
    )) AS known
),
created AS (
    INSERT INTO exports (workspace_id, requested_by, kind, format, filters, requester_roles, row_count, expires_at, duty_grants)
    SELECT w.id, $1, $3, $4,
           jsonb_build_object(
               'hours', NULLIF($5, 0),
               'from', CASE WHEN $5 <> 0 THEN
                   to_char((now() - make_interval(hours => $5)) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') END,
               'to', to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
               'forestClasses', to_jsonb($6::text[]), 'queue', NULLIF($7, ''),
               'unitPath', $8::text, 'step', NULLIF($9, ''))
           || CASE WHEN $14 <> '' THEN jsonb_build_object('group', $14) ELSE '{}' END
           || CASE WHEN $3 = 'submissions' THEN jsonb_build_object('form', NULLIF($12, ''), 'decision', NULLIF($13, ''))
                   ELSE '{}' END,
           (SELECT jsonb_agg(DISTINCT jsonb_build_object('role', g.role_slug, 'level', g.level,
                                                        'unitCode', g.code, 'unitName', g.name))
            FROM grants g),
           total.n, now() + $11::interval, dispatch_grant_keys(w.id, $1, true)
    FROM workspaces w, total
    WHERE w.slug = $2 AND EXISTS (SELECT 1 FROM grants) AND total.n <= $10 AND (SELECT known FROM step)
    RETURNING id, filters
),
frozen AS (
    INSERT INTO export_rows (export_id, ordinal, workspace_id, detection_id, observed_at, alert_id, submission_id, boundary_snapshot, dispatch)
    SELECT c.id, p.ordinal, p.workspace_id, p.detection_id, p.observed_at, p.alert_id, p.submission_id, p.boundary_snapshot, p.dispatch
    FROM created c CROSS JOIN permitted p
    RETURNING 1
)
SELECT EXISTS (SELECT 1 FROM grants), (SELECT known FROM step), (SELECT n FROM total),
       (SELECT id FROM created), (SELECT filters FROM created),
       (SELECT count(*) FROM frozen)
