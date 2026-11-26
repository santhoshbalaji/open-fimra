WITH visible AS MATERIALIZED (
    SELECT alert_id, bool_or(officer) AS officer FROM incident_alert_roles($1,$2,$3) GROUP BY alert_id
), current_case AS (
    SELECT i.*, wi.template_id FROM incidents i JOIN workflow_instances wi ON wi.incident_id=i.id WHERE i.id=$3
), entries AS (
    SELECT l.recorded_at AS ordered_at, l.id,
        jsonb_build_object('from',l.from_step,'step',l.to_step,'label',s.label,
            'at',l.occurred_at,'actualAt',l.actual_at,'estimatedAt',l.estimated_at,'syncedAt',l.recorded_at,
            'response',l.response_note,'actor',u.display_name,'note',l.note,'reason',l.override_reason,
            'longitude',ST_X(l.location),'latitude',ST_Y(l.location),'reportPending',l.report_pending,
            'alertId',l.alert_id::text,'alertsMoved',(SELECT count(*) FROM step_log x
                JOIN visible v ON v.alert_id=x.alert_id WHERE x.incident_step_id=l.id)) AS entry
    FROM current_case c
    JOIN step_log l ON l.incident_id=c.id OR (
        workflow_template_extended(l.template_id) AND l.from_step IS NOT NULL
        AND l.incident_step_id IS NULL AND l.alert_id IN (SELECT alert_id FROM visible)
        AND c.merged_into IS NULL)
    JOIN workflow_steps s ON (s.template_id,s.slug)=(l.template_id,l.to_step)
    LEFT JOIN users u ON u.id=l.user_id
    UNION ALL
    SELECT f.received_at,f.alert_id,
        jsonb_build_object('step','intake','label',CASE f.source WHEN 'phone' THEN 'Phone report'
            WHEN 'radio' THEN 'Radio report' WHEN 'staff' THEN 'Staff report' WHEN 'public' THEN 'Public report' END,
            'at',f.observed_at,'actualAt',f.observed_at,'syncedAt',f.received_at,
            'actor',u.display_name,'note',f.note,'alertId',f.alert_id::text)
    FROM fire_intake f JOIN visible v USING (alert_id) JOIN users u ON u.id=f.user_id
    UNION ALL
    SELECT c.changed_at,c.id,
        jsonb_build_object('step',c.operation,'label',CASE c.operation WHEN 'merge' THEN 'Incident merged' ELSE 'Incident split' END,
            'at',c.changed_at,'actor',u.display_name,'note',c.reason,
            'relatedIncidentId',CASE WHEN c.from_incident_id=$3 THEN c.to_incident_id ELSE c.from_incident_id END::text)
    FROM incident_membership_changes c JOIN users u ON u.id=c.user_id
    WHERE (c.from_incident_id=$3 OR c.to_incident_id=$3)
      AND EXISTS (
        SELECT FROM unnest(c.alert_ids) moved(alert_id)
        CROSS JOIN LATERAL alert_roles($1,$2,moved.alert_id) r)
      AND EXISTS (
        SELECT FROM incident_alert_roles($1,$2,
            CASE WHEN c.from_incident_id=$3 THEN c.to_incident_id ELSE c.from_incident_id END))
)
SELECT jsonb_build_object(
    'moves',COALESCE((SELECT jsonb_agg(to_jsonb(m) || jsonb_build_object('extended',workflow_template_extended(c.template_id))
        ORDER BY terminal,label) FROM incident_moves($1,$2,$3) m),'[]'),
    'mergedInto',c.merged_into::text,
    'canManageMembers',c.merged_into IS NULL AND NOT EXISTS (
        SELECT FROM incident_members m WHERE m.incident_id=c.id
        AND NOT EXISTS (SELECT FROM visible v WHERE v.alert_id=m.alert_id AND v.officer)),
    'entries',(SELECT jsonb_agg(entry ORDER BY ordered_at,id) FROM entries))
FROM current_case c WHERE EXISTS (SELECT FROM visible);
