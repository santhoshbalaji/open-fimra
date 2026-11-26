BEGIN;

DO $$
DECLARE
    ws uuid;
    hierarchy uuid;
    policy bigint;
    prior uuid;
BEGIN
    IF daily_return_cutoff('2026-03-08', '18:00', 'Asia/Kolkata') <> '2026-03-08 12:30Z'
       OR daily_return_cutoff('2026-03-08', '18:00', 'America/New_York') <> '2026-03-08 22:00Z'
       OR daily_return_cutoff('2026-03-08', '18:00', 'America/New_York')
          - daily_return_cutoff('2026-03-07', '18:00', 'America/New_York') <> interval '23 hours' THEN
        RAISE EXCEPTION 'The cutoff must follow local time across an offset change';
    END IF;

    INSERT INTO workspaces(slug,name) VALUES ('test-daily','Test daily return') RETURNING id INTO ws;
    INSERT INTO hierarchies(workspace_id,slug,name,kind) VALUES (ws,'territorial','Territorial','territorial') RETURNING id INTO hierarchy;
    INSERT INTO hierarchy_levels(hierarchy_id,depth,slug,labels) VALUES (hierarchy,1,'division','{"en":"Division"}');
    INSERT INTO daily_return_policies(workspace_id,hierarchy_id,depth,season,time_zone,cutoff,approver_role)
    VALUES(ws,hierarchy,1,'[2026-03-01,2026-04-01)','Asia/Kolkata','18:00','state_admin') RETURNING id INTO policy;

    BEGIN
        INSERT INTO daily_return_policies(workspace_id,hierarchy_id,depth,season,time_zone,cutoff,approver_role)
        VALUES(ws,hierarchy,1,'[2026-03-31,2026-04-02)','Asia/Kolkata','19:00','state_admin');
        RAISE EXCEPTION 'Reporting seasons must not overlap';
    EXCEPTION WHEN exclusion_violation THEN NULL;
    END;

    INSERT INTO situation_reports(workspace_id,requested_by,hierarchy_id,unit_path,period_from,period_to,content,
                                  status,expires_at,daily_policy_id,daily_revision)
    VALUES(ws,'producer',hierarchy,'root.division','2026-03-08','2026-03-08','{"dailyReturn":{"nil":true}}',
           'draft','infinity',policy,1) RETURNING id INTO prior;
    BEGIN
        UPDATE situation_reports SET content = '{"dailyReturn":{"nil":false}}' WHERE id=prior;
        RAISE EXCEPTION 'Saved figures must be immutable';
    EXCEPTION WHEN insufficient_privilege THEN NULL;
    END;
    BEGIN
        INSERT INTO situation_reports(workspace_id,requested_by,hierarchy_id,unit_path,period_from,period_to,content,
                                      status,expires_at,daily_policy_id,daily_revision,daily_previous_id)
        VALUES(ws,'producer',hierarchy,'root.division','2026-03-08','2026-03-08','{"dailyReturn":{"nil":false}}',
               'draft','infinity',policy,2,prior);
        RAISE EXCEPTION 'A correction must follow a review decision';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    INSERT INTO daily_return_decisions(report_id,decision,decided_by,decided_name,note)
    VALUES(prior,'returned','reviewer','Synthetic reviewer','Check the figures');
    INSERT INTO situation_reports(workspace_id,requested_by,hierarchy_id,unit_path,period_from,period_to,content,
                                  status,expires_at,daily_policy_id,daily_revision,daily_previous_id)
    VALUES(ws,'producer',hierarchy,'root.division','2026-03-08','2026-03-08','{"dailyReturn":{"nil":false}}',
           'draft','infinity',policy,2,prior);
    IF NOT EXISTS (SELECT FROM situation_reports WHERE id=prior AND content #>> '{dailyReturn,nil}'='true') THEN
        RAISE EXCEPTION 'The prior revision must remain available';
    END IF;
END;
$$;

ROLLBACK;
