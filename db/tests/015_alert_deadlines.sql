BEGIN;
SET LOCAL TIME ZONE 'UTC';

DO $$
DECLARE
    template bigint := (SELECT id FROM workflow_templates WHERE workspace_id IS NULL);
    entered timestamptz := '2001-02-01 00:00 UTC';
BEGIN
    IF alert_due_at(template, 'new', entered) IS DISTINCT FROM entered + interval '30 minutes' THEN
        RAISE EXCEPTION 'New alerts should have an acknowledgement deadline';
    END IF;

    IF alert_due_at(template, 'controlled', entered) IS NOT NULL THEN
        RAISE EXCEPTION 'Steps without time limits should have no deadline';
    END IF;

    UPDATE workflow_transitions
    SET time_limit = CASE to_step WHEN 'acknowledged' THEN interval '1 month' ELSE interval '29 days' END
    WHERE template_id = template AND from_step = 'new' AND to_step IN ('acknowledged', 'false_alarm');

    IF alert_due_at(template, 'new', entered) IS DISTINCT FROM '2001-03-01 00:00 UTC'::timestamptz THEN
        RAISE EXCEPTION 'Use the earliest absolute deadline across transitions';
    END IF;
END;
$$;

ROLLBACK;
