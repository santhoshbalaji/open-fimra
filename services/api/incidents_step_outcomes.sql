-- Use the same member decisions for the preview and the write. Start with
-- visible members so even skipped results cannot expose hidden alerts.
member_outcomes AS (
    SELECT COALESCE(jsonb_agg(jsonb_build_object(
        'alertId', wi.alert_id::text, 'fromLabel', s.label, 'reason',
        CASE
            WHEN wi.current_step = $4 THEN 'already_at_step'
            WHEN t.to_step IS NULL AND s.terminal THEN 'closed'
            WHEN t.to_step IS NULL THEN 'different_step'
            WHEN NOT EXISTS (SELECT FROM alert_moves($1,$2,wi.alert_id) WHERE step=$4) THEN 'role'
            WHEN EXISTS (SELECT FROM alert_missing_forms(wi.alert_id,$4)) THEN 'report_required'
            ELSE 'transition_allowed'
        END
    ) ORDER BY wi.alert_id), '[]') AS alerts
    FROM (SELECT DISTINCT alert_id FROM incident_alert_roles($1,$2,$3)) visible
    JOIN workflow_instances wi USING (alert_id)
    JOIN workflow_steps s ON (s.template_id,s.slug)=(wi.template_id,wi.current_step)
    LEFT JOIN workflow_transitions t ON (t.template_id,t.from_step,t.to_step)=(wi.template_id,wi.current_step,$4)
)
