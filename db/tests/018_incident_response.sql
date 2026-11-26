BEGIN;
SET LOCAL TIME ZONE 'UTC';

DO $$
DECLARE
    workspace uuid;
    legacy uuid;
    extended bigint;
    original bigint;
    start_at timestamptz := '2001-02-01 13:00Z';
BEGIN
    INSERT INTO workspaces(slug,name) VALUES ('test-response','Test Response') RETURNING id INTO workspace;
    INSERT INTO workspaces(slug,name,extended_incident_response) VALUES ('test-original','Original Response',false) RETURNING id INTO legacy;
    extended := workspace_workflow_template(workspace);
    original := workspace_workflow_template(legacy);
    IF extended=original OR NOT workflow_template_extended(extended) OR workflow_template_extended(original) THEN
        RAISE EXCEPTION 'New and original workspaces should select different templates';
    END IF;
    IF (SELECT count(*) FROM workflow_steps WHERE template_id=extended AND terminal) <> 8 OR
        (SELECT count(*) FROM workflow_steps WHERE template_id=original AND terminal) <> 5 THEN
        RAISE EXCEPTION 'Every disposition should have its own workflow step';
    END IF;
    IF NOT EXISTS (SELECT FROM workflow_transitions WHERE template_id=extended AND from_step='closed'
                   AND to_step='reopened' AND allowed_roles=ARRAY['division_officer','state_admin']) THEN
        RAISE EXCEPTION 'Only officers should be able to reopen a closed case';
    END IF;
    INSERT INTO incident_season_targets VALUES (workspace,'[2001-02-01,2001-02-08)','new',5,10);
    BEGIN
        INSERT INTO incident_season_targets VALUES (workspace,'[2001-02-07,2001-02-09)','new',5,10);
        RAISE EXCEPTION 'Overlapping dates for a milestone should be rejected';
    EXCEPTION WHEN exclusion_violation THEN NULL;
    END;
    IF (SELECT array_agg(due_at ORDER BY level) FROM incident_response_target(workspace,extended,'new',start_at))
       IS DISTINCT FROM ARRAY[start_at+interval '5 minutes',start_at+interval '10 minutes'] THEN
        RAISE EXCEPTION 'Season dates and both escalation levels should determine the deadlines';
    END IF;
    IF (SELECT due_at FROM incident_response_target(workspace,extended,'new',start_at-interval '1 day'))
       IS DISTINCT FROM start_at-interval '1 day'+interval '30 minutes' THEN
        RAISE EXCEPTION 'The default workflow deadline should apply outside the season';
    END IF;
END;
$$;

ROLLBACK;
