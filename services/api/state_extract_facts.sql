-- Frozen independently from the daily operational roll-up. The same observation
-- may appear in more than one unit's snapshot. State totals use its stable key.
WITH matched_in_unit AS MATERIALIZED (
    SELECT m.detection_id,m.observed_at,m.forest_class
    FROM detection_matches m
    WHERE m.workspace_id=$1 AND m.observed_at >= $4 AND m.observed_at < $5
      AND EXISTS (SELECT FROM unnest(m.unit_paths) p
        JOIN units u ON u.hierarchy_id=$2 AND u.path=p
        WHERE p <@ $3 AND u.valid_from <= m.observed_at AND (u.valid_to IS NULL OR u.valid_to > m.observed_at))
), detection_records AS (
    SELECT m.detection_id AS id,m.observed_at,m.forest_class,
        coalesce(l.primary_id,m.detection_id) AS primary_id,
        coalesce(l.primary_observed_at,m.observed_at) AS primary_at
    FROM matched_in_unit m LEFT JOIN detection_links l ON (l.detection_id,l.observed_at)=(m.detection_id,m.observed_at)
    UNION
    SELECT l.detection_id,l.observed_at,m.forest_class,l.primary_id,l.primary_observed_at
    FROM matched_in_unit m JOIN detection_links l ON (l.primary_id,l.primary_observed_at)=(m.detection_id,m.observed_at)
    WHERE l.observed_at >= $4 AND l.observed_at < $5
), detections_in_unit AS MATERIALIZED (
    SELECT DISTINCT ON (r.id,r.observed_at) r.*,d.source
    FROM detection_records r JOIN detections d ON (d.id,d.observed_at)=(r.id,r.observed_at)
    ORDER BY r.id,r.observed_at,r.forest_class
), members AS MATERIALIZED (
    SELECT a.id, a.observed_at, a.forest_class, a.dispatch_lead, m.incident_id
    FROM alerts a LEFT JOIN incident_members m ON m.alert_id=a.id
    WHERE a.workspace_id=$1 AND a.observed_at < $5
), unit_alerts AS MATERIALIZED (
    SELECT a.id, a.observed_at, coalesce(outcome.to_step, CASE flash.answers->>'fire_seen'
        WHEN 'yes' THEN 'fire_seen' WHEN 'no' THEN 'no_fire_found'
        ELSE CASE WHEN a.forest_class='outside' THEN 'non_forest' ELSE 'awaiting_verification' END END) AS disposition
    FROM members a
    LEFT JOIN LATERAL (
        SELECT l.to_step FROM step_log l WHERE l.alert_id=a.id
          AND coalesce(l.actual_at,l.occurred_at) < $5
          AND l.to_step IN ('false_alarm','duplicate','non_forest','controlled_burn','not_reachable','unsafe','outside_jurisdiction')
        ORDER BY l.id DESC LIMIT 1
    ) outcome ON true
    LEFT JOIN LATERAL (
        SELECT s.answers FROM submissions s JOIN form_versions v ON v.id=s.form_version_id
        JOIN forms f ON f.id=v.form_id AND f.form_key='response-flash'
        LEFT JOIN approvals ap ON ap.submission_id=s.id AND ap.decided_at < $5
        WHERE s.alert_id=a.id AND s.received_at < $5 AND ap.decision IS DISTINCT FROM 'returned'
        ORDER BY s.received_at DESC,s.id DESC LIMIT 1
    ) flash ON true
    WHERE a.dispatch_lead->>'hierarchyId'=$2::uuid::text AND (a.dispatch_lead->>'path')::ltree <@ $3
), owners AS (
    SELECT DISTINCT ON (incident_id) incident_id, dispatch_lead
    FROM members WHERE incident_id IS NOT NULL AND dispatch_lead IS NOT NULL
    ORDER BY incident_id,observed_at,id
), confirmed AS MATERIALIZED (
    SELECT i.id, evidence.confirmed_at FROM incidents i JOIN owners o ON o.incident_id=i.id
    CROSS JOIN LATERAL (
        SELECT min(at) AS confirmed_at FROM (
            SELECT s.received_at AS at FROM submissions s
            JOIN members a ON a.id=s.alert_id AND a.incident_id=i.id
            JOIN form_versions v ON v.id=s.form_version_id
            JOIN forms f ON f.id=v.form_id AND f.form_key='response-flash'
            LEFT JOIN approvals ap ON ap.submission_id=s.id AND ap.decided_at < $5
            WHERE s.answers->>'fire_seen'='yes' AND s.answers->>'land_class'='forest' AND s.received_at < $5
              AND ap.decision IS DISTINCT FROM 'returned'
              AND NOT EXISTS (SELECT FROM prescribed_burn_alerts p WHERE p.alert_id=a.id)
              AND NOT EXISTS (SELECT FROM step_log l WHERE l.alert_id=a.id
                  AND coalesce(l.actual_at,l.occurred_at) < $5
                  AND l.to_step IN ('false_alarm','non_forest','controlled_burn','outside_jurisdiction'))
        ) evidence
    ) evidence
    WHERE o.dispatch_lead->>'hierarchyId'=$2::uuid::text AND (o.dispatch_lead->>'path')::ltree <@ $3
      AND (i.merged_at IS NULL OR i.merged_at >= $5) AND evidence.confirmed_at IS NOT NULL
), states AS (
    SELECT c.id,s.to_step,s.terminal FROM confirmed c
    CROSS JOIN LATERAL (
        SELECT l.to_step,w.terminal FROM step_log l JOIN workflow_steps w ON (w.template_id,w.slug)=(l.template_id,l.to_step)
        WHERE l.incident_id=c.id AND l.to_step <> 'response_update' AND coalesce(l.actual_at,l.occurred_at) < $5
        ORDER BY l.id DESC LIMIT 1
    ) s
), facts(kind,key,metric) AS (
    SELECT 'detection',id::text || '/' || extract(epoch FROM observed_at)::text,
        'detection.' || source || '.' || forest_class FROM detections_in_unit
    UNION ALL SELECT 'deduplicated',primary_id::text || '/' || extract(epoch FROM primary_at)::text,
        'detection.deduplicated' FROM detections_in_unit
    UNION ALL SELECT 'alert',id::text,'alert.' || disposition FROM unit_alerts WHERE observed_at >= $4
    UNION ALL SELECT 'awaiting',id::text,'alert.awaiting_at_cutoff' FROM unit_alerts WHERE disposition='awaiting_verification'
    UNION ALL SELECT 'confirmed_new',id::text,'incident.new' FROM confirmed WHERE confirmed_at >= $4
    UNION ALL SELECT 'confirmed_active',id::text,'incident.active' FROM states WHERE NOT terminal
        AND to_step NOT IN ('extinguished','watch','first_report','final_report','verified')
    UNION ALL SELECT 'closure_backlog',id::text,'incident.closure_backlog' FROM states WHERE NOT terminal
        AND to_step IN ('extinguished','watch','first_report','final_report','verified')
    UNION ALL SELECT DISTINCT l.to_step,c.id::text,'incident.' || l.to_step FROM confirmed c
        JOIN step_log l ON l.incident_id=c.id WHERE coalesce(l.actual_at,l.occurred_at) >= $4
        AND coalesce(l.actual_at,l.occurred_at) < $5 AND l.to_step IN ('controlled','extinguished','closed')
)
SELECT coalesce(jsonb_agg(jsonb_build_object('kind',kind,'key',key,'metric',metric,'amount',1)
    ORDER BY kind,key,metric), '[]') FROM facts
