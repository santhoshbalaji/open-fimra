-- A fact belongs to one lead, even when it can be seen by several units.
-- Incidents follow the earliest member with a lead, including when another
-- member or a support request belongs to a different division.
WITH led_alerts AS MATERIALIZED (
    SELECT a.id, a.observed_at, a.detection_id, a.dispatch_lead, im.incident_id,
           coalesce(fi.received_at, a.observed_at) AS source_at,
           coalesce(nullif(fi.source, 'phone'), CASE WHEN fi.source = 'phone' THEN 'call'
               WHEN d.source = 'manual' THEN CASE manual.source
                   WHEN 'public-call' THEN 'call' WHEN 'other-agency' THEN 'public'
                   WHEN 'other-satellite-service' THEN 'satellite' ELSE 'staff' END
               WHEN a.detection_id IS NULL THEN 'staff' ELSE 'satellite' END) AS source
    FROM alerts a LEFT JOIN incident_members im ON im.alert_id = a.id
    LEFT JOIN fire_intake fi ON fi.alert_id = a.id
    LEFT JOIN detections d ON (d.id, d.observed_at) = (a.detection_id, a.observed_at) AND d.source = 'manual'
    LEFT JOIN LATERAL (
        SELECT s.answers->>'report_source' AS source FROM submissions s
        JOIN form_versions v ON v.id = s.form_version_id JOIN forms f ON f.id = v.form_id AND f.form_key = 'manual-fire-report'
        WHERE d.id IS NOT NULL AND s.alert_id = a.id ORDER BY s.received_at, s.id LIMIT 1
    ) manual ON true
    WHERE a.workspace_id = $1 AND a.observed_at < $5
), unit_alerts AS MATERIALIZED (
    SELECT * FROM led_alerts a
    WHERE a.dispatch_lead->>'hierarchyId' = $2::uuid::text AND (a.dispatch_lead->>'path')::ltree <@ $3
), owners AS (
    SELECT DISTINCT ON (incident_id) incident_id, dispatch_lead
    FROM led_alerts WHERE incident_id IS NOT NULL AND dispatch_lead IS NOT NULL
    ORDER BY incident_id, observed_at, id
), cases AS MATERIALIZED (
    SELECT i.id, i.created_at, i.merged_at, i.first_observed_at FROM incidents i JOIN owners o ON o.incident_id = i.id
    WHERE o.dispatch_lead->>'hierarchyId' = $2::uuid::text AND (o.dispatch_lead->>'path')::ltree <@ $3
      AND (i.merged_at IS NULL OR i.merged_at >= $5)
), case_steps AS MATERIALIZED (
    SELECT l.id, l.incident_id, l.template_id, l.to_step, l.from_step, s.terminal,
           coalesce(l.actual_at, l.occurred_at) AS happened,
           l.actual_at IS NOT NULL OR l.estimated_at IS NULL AS actual,
           lag(coalesce(l.actual_at, l.occurred_at)) OVER history AS entered,
           lag(l.actual_at IS NOT NULL OR l.estimated_at IS NULL) OVER history AS actual_entry
    FROM step_log l JOIN cases c ON c.id = l.incident_id
    JOIN workflow_steps s ON (s.template_id, s.slug) = (l.template_id, l.to_step)
    WHERE coalesce(l.actual_at, l.occurred_at) < $5 AND l.to_step <> 'response_update'
    WINDOW history AS (PARTITION BY l.incident_id ORDER BY l.id)
), current_cases AS (
    SELECT DISTINCT ON (incident_id) * FROM case_steps ORDER BY incident_id, id DESC
), overdue_alerts AS (
    SELECT a.id FROM unit_alerts a CROSS JOIN LATERAL (
        SELECT l.template_id, l.to_step, coalesce(l.actual_at, l.occurred_at) AS happened
        FROM step_log l WHERE l.alert_id = a.id AND coalesce(l.actual_at, l.occurred_at) < $5
          AND l.to_step <> 'response_update' ORDER BY l.id DESC LIMIT 1
    ) s WHERE alert_due_at(s.template_id, s.to_step, s.happened) < $5
), outcomes AS (
    SELECT DISTINCT ON (l.alert_id) l.alert_id, l.to_step
    FROM step_log l JOIN unit_alerts a ON a.id = l.alert_id
    WHERE coalesce(l.actual_at, l.occurred_at) >= $4 AND coalesce(l.actual_at, l.occurred_at) < $5
      AND l.to_step IN ('false_alarm','duplicate','non_forest','controlled_burn','not_reachable','unsafe','outside_jurisdiction')
    ORDER BY l.alert_id, l.id DESC
), verifications AS (
    SELECT DISTINCT ON (s.alert_id) s.alert_id, s.answers->>'fire_seen' AS seen
    FROM submissions s JOIN unit_alerts a ON a.id = s.alert_id
    JOIN form_versions v ON v.id = s.form_version_id JOIN forms f ON f.id = v.form_id AND f.form_key = 'response-flash'
    LEFT JOIN approvals ap ON ap.submission_id = s.id
    WHERE s.received_at >= $4 AND s.received_at < $5 AND ap.decision IS DISTINCT FROM 'returned'
    ORDER BY s.alert_id, s.received_at DESC, s.id DESC
), area_entries AS (
    SELECT a.incident_id, s.id, s.answers, coalesce(ap.decided_at, s.received_at) AS as_of,
           CASE WHEN ap.decision = 'approved' THEN 'approved' ELSE 'provisional' END AS review
    FROM submissions s JOIN led_alerts a ON a.id = s.alert_id JOIN cases c ON c.id = a.incident_id
    JOIN form_versions v ON v.id = s.form_version_id JOIN forms f ON f.id = v.form_id AND f.form_key = 'damage-assessment'
    LEFT JOIN approvals ap ON ap.submission_id = s.id AND ap.decided_at < $5
    WHERE ap.decision IS DISTINCT FROM 'returned' AND jsonb_typeof(s.answers->'area_ha') = 'number'
      AND coalesce(ap.decided_at, s.received_at) >= $4 AND coalesce(ap.decided_at, s.received_at) < $5
), area AS (
    SELECT DISTINCT ON (incident_id) incident_id, review, as_of, (answers->>'area_ha')::numeric AS hectares,
           CASE
             WHEN lower(answers->>'method') ~ '(mixed|combined|मिश्र)' THEN 'mixed'
             WHEN lower(answers->>'method') ~ '(satellite|remote sensing|उपग्रह)' AND
                  lower(answers->>'method') ~ '(field|ground|gps|मैदान|स्थल)' THEN 'mixed'
             WHEN lower(answers->>'method') ~ '(satellite|remote sensing|उपग्रह)' THEN 'satellite'
             WHEN lower(answers->>'method') ~ '(field|ground|gps|मैदान|स्थल)' OR answers->>'measurement' = 'surveyed' THEN 'field'
             ELSE 'unspecified'
           END AS method
    FROM area_entries ORDER BY incident_id, (review = 'approved') DESC, as_of DESC, id DESC
), responses AS (
    SELECT s.id, s.from_step, round(extract(epoch FROM s.happened - s.entered)) AS seconds,
           s.actual AND s.actual_entry AS measured,
           s.happened <= s.entered + make_interval(mins => t.target_minutes) AS on_time
    FROM case_steps s LEFT JOIN incident_season_targets t
      ON t.workspace_id = $1 AND t.step = s.from_step AND t.season @> (s.entered AT TIME ZONE $6)::date
    WHERE s.happened >= $4 AND s.entered IS NOT NULL AND s.happened >= s.entered
      AND s.from_step IN ('new','reopened','acknowledged','assigned','team_sent')
), facts(kind, key, metric, amount, measured_at) AS (
    SELECT 'source', id::text, 'source.' || source, 1::numeric, NULL::timestamptz FROM unit_alerts WHERE source_at >= $4 AND source_at < $5
    UNION ALL SELECT 'outcome', alert_id::text, 'outcome.' || to_step, 1, NULL FROM outcomes
    UNION ALL SELECT 'verification', alert_id::text, 'verification.' || CASE seen WHEN 'yes' THEN 'yes' WHEN 'no' THEN 'no' ELSE 'unknown' END,
        1, NULL FROM verifications
    UNION ALL SELECT 'verification', alert_id::text, 'verification.' || CASE WHEN to_step = 'false_alarm' THEN 'no' ELSE 'unknown' END, 1, NULL
        FROM outcomes o WHERE NOT EXISTS (SELECT FROM verifications v WHERE v.alert_id = o.alert_id)
    UNION ALL SELECT 'new', id::text, 'incident.new', 1, NULL FROM cases WHERE first_observed_at >= $4
    UNION ALL SELECT 'active', incident_id::text, 'incident.active', 1, NULL FROM current_cases WHERE NOT terminal
        AND to_step NOT IN ('extinguished','watch','first_report','final_report','verified')
    UNION ALL SELECT to_step, incident_id::text, 'incident.' || to_step, 1, NULL FROM case_steps
        WHERE happened >= $4 AND to_step IN ('controlled','extinguished') GROUP BY incident_id, to_step
    UNION ALL SELECT 'requested', r.id::text, 'assistance.requested', 1, NULL FROM alert_support_requests r
        JOIN unit_alerts a ON a.id = r.alert_id WHERE r.requested_at >= $4 AND r.requested_at < $5
    UNION ALL SELECT 'area', incident_id::text, 'area.' || method || '.' || review, hectares, as_of FROM area
    UNION ALL SELECT 'overdue', c.incident_id::text, 'work.overdue', 1, NULL FROM current_cases c
        LEFT JOIN incident_season_targets t ON t.workspace_id = $1 AND t.step = c.to_step
          AND t.season @> (c.happened AT TIME ZONE $6)::date
        WHERE coalesce(c.happened + make_interval(mins => t.target_minutes),
                       alert_due_at(c.template_id, c.to_step, c.happened)) < $5
    UNION ALL SELECT 'overdue_alert', id::text, 'work.overdue_alerts', 1, NULL FROM overdue_alerts
    UNION ALL SELECT 'response.time', id::text, 'response.' || from_step || '.seconds', seconds, NULL FROM responses WHERE measured
    UNION ALL SELECT 'response.count', id::text, 'response.' || from_step || CASE WHEN NOT measured THEN '.unmeasured'
        WHEN on_time THEN '.within' WHEN NOT on_time THEN '.late' ELSE '.no_target' END, 1, NULL FROM responses
)
SELECT coalesce(jsonb_agg(jsonb_build_object('kind', kind, 'key', key, 'metric', metric, 'amount', amount,
    'at', measured_at) ORDER BY kind, key),'[]') FROM facts
