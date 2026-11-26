-- The detections and routed alerts a caller may see in a time window. The
-- control-room dashboard (summary.sql) and situation reports
-- (situation_report.sql) both start with this text, so every count they share
-- comes from one definition.
-- $1 subject, $2 workspace slug,
-- $3 start of the window, or NULL for $4 hours before now,
-- $5 end of the window, or NULL for now (a later end is cut to now),
-- $6 forest class ('' for all), $7 unit path prefix (or NULL).
WITH grants AS MATERIALIZED (
    SELECT * FROM alert_grants WHERE oidc_subject=$1 AND slug=$2
), window_ends AS (
    SELECT coalesce($3::timestamptz, now()-make_interval(hours=>$4::integer)) AS start_at,
           least(coalesce($5::timestamptz, now()), now()) AS end_at
), bounds AS (
    -- Today runs from midnight in India Standard Time on the window's last day.
    SELECT start_at, end_at, date_trunc('day',end_at,'Asia/Kolkata') AS midnight FROM window_ends
), matches AS (
    SELECT m.observed_at,m.forest_class
    FROM detection_matches m CROSS JOIN bounds
    LEFT JOIN alerts routed ON (routed.workspace_id, routed.detection_id) = (m.workspace_id, m.detection_id)
    WHERE m.observed_at BETWEEN least(start_at,midnight) AND end_at
      AND ($6='' OR m.forest_class=$6) AND EXISTS (SELECT FROM grants g WHERE g.workspace_id=m.workspace_id
        AND ((($7::ltree IS NULL OR m.unit_paths <@ $7) AND match_under_grant(m.hierarchy_unit_paths,g.hierarchy_id,g.unit_path,g.depth))
              OR (($7::ltree IS NULL OR routed.unit_paths <@ $7)
                  AND dispatch_detection_visible(routed, g.can_review, g.hierarchy_id, g.unit_path, g.depth))))
), visible AS MATERIALIZED (
    -- Steps and overdue checks are as of now, also for a window in the past.
    SELECT a.id,a.queue,i.template_id,i.current_step,i.entered_at,s.terminal,s.report_pending,
           EXISTS (SELECT FROM workflow_transitions t
              WHERE (t.template_id,t.from_step)=(i.template_id,i.current_step)
                AND now()>i.entered_at+t.time_limit) AS overdue
    FROM alerts a
    JOIN workflow_instances i ON i.alert_id=a.id
    JOIN workflow_steps s ON (s.template_id,s.slug)=(i.template_id,i.current_step)
    WHERE a.observed_at BETWEEN (SELECT start_at FROM bounds) AND (SELECT end_at FROM bounds)
      AND ($6='' OR a.forest_class=$6) AND ($7::ltree IS NULL OR a.unit_paths <@ $7)
      AND EXISTS (
          SELECT FROM grants g WHERE g.workspace_id=a.workspace_id
            AND (a.queue='unit' OR g.can_review)
            AND match_under_grant(a.hierarchy_unit_paths,g.hierarchy_id,g.unit_path,g.depth)
      )
)
