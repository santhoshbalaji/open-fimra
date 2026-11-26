-- This replay is opt-in. It leaves Valley Division without a return.
BEGIN;
DO $$
DECLARE
    ws uuid;
    hierarchy uuid;
    level smallint;
    policy bigint;
    officer users;
    river_officer users;
    reviewer users;
    closing_day date := (now() AT TIME ZONE 'Asia/Kolkata')::date - 1;
    due timestamptz := daily_return_cutoff(closing_day, '18:00', 'Asia/Kolkata');
    nil_return uuid;
    original uuid;
    correction uuid;
    template jsonb;
BEGIN
    SELECT w.id,h.id,l.depth INTO STRICT ws,hierarchy,level
    FROM workspaces w JOIN hierarchies h ON h.workspace_id=w.id
    JOIN hierarchy_levels l ON l.hierarchy_id=h.id
    WHERE w.slug='demo' AND h.slug='territorial' AND l.slug='division';
    IF EXISTS (SELECT FROM daily_return_policies WHERE workspace_id=ws AND season && daterange(closing_day-7,closing_day,'[]')) THEN
        RETURN;
    END IF;
    SELECT u.* INTO STRICT officer FROM users u JOIN role_assignments g ON g.user_id=u.id
        WHERE g.workspace_id=ws AND g.unit_path='ds.c1.d1' AND g.role_slug='division_officer';
    SELECT u.* INTO STRICT reviewer FROM users u JOIN role_assignments g ON g.user_id=u.id
        WHERE g.workspace_id=ws AND g.unit_path='ds' AND g.role_slug='state_admin';
    INSERT INTO users(oidc_subject,username,display_name)
    VALUES ('demo-daily-river-officer','demo.daily.river.officer','Demo River Division Officer')
    ON CONFLICT (oidc_subject) DO NOTHING;
    SELECT * INTO STRICT river_officer FROM users WHERE oidc_subject='demo-daily-river-officer';
    INSERT INTO role_assignments(user_id,workspace_id,hierarchy_id,unit_path,role_slug)
    VALUES(river_officer.id,ws,hierarchy,'ds.c2.d3','division_officer') ON CONFLICT DO NOTHING;

    INSERT INTO daily_return_policies(workspace_id,hierarchy_id,depth,season,time_zone,cutoff,approver_role)
    VALUES(ws,hierarchy,level,daterange(closing_day-7,closing_day,'[]'),'Asia/Kolkata','18:00','state_admin')
    RETURNING id INTO policy;
    INSERT INTO notification_rules(workspace_id,event,recipients,channel)
    VALUES(ws,'daily_return_due','division_officer','in_app'),(ws,'daily_return_missing','reviewers','in_app')
    ON CONFLICT DO NOTHING;

    template := jsonb_build_object('period',jsonb_build_object('from',closing_day,'to',closing_day,
        'days',1,'start',daily_return_cutoff(closing_day-1,'18:00','Asia/Kolkata'),'end',due,
        'timeZone','Asia/Kolkata','partial',false),
        'dailyReturn',jsonb_build_object('cutoff',due,'author',officer.display_name,
            'policy',jsonb_build_object('hierarchy','territorial','level','division','timeZone','Asia/Kolkata',
            'cutoff','18:00','from',closing_day-7,'to',closing_day,'approver','state_admin')));

    INSERT INTO situation_reports(workspace_id,requested_by,hierarchy_id,unit_path,period_from,period_to,
        content,status,created_at,expires_at,daily_policy_id,daily_revision)
    VALUES(ws,officer.oidc_subject,hierarchy,'ds.c1.d1',closing_day,closing_day,
        template || jsonb_build_object('scope','Hill Division','dailyReturn',(template->'dailyReturn') ||
            '{"nil":true,"remarks":"No fire activity reported.","totals":{},"facts":[]}'),
        'draft',due,'infinity',policy,1) RETURNING id INTO nil_return;

    INSERT INTO situation_reports(workspace_id,requested_by,hierarchy_id,unit_path,period_from,period_to,
        content,status,created_at,expires_at,daily_policy_id,daily_revision)
    VALUES(ws,river_officer.oidc_subject,hierarchy,'ds.c2.d3',closing_day,closing_day,
        template || jsonb_build_object('scope','River Division','dailyReturn',(template->'dailyReturn') ||
            jsonb_build_object('author',river_officer.display_name) || '{"nil":false,"remarks":"Radio report from the reed beds.","totals":{"source.radio":1,"incident.new":1,"area.field.provisional":3},"facts":[{"kind":"source","key":"demo-radio","metric":"source.radio","amount":1},{"kind":"new","key":"demo-reed","metric":"incident.new","amount":1},{"kind":"area","key":"demo-reed","metric":"area.field.provisional","amount":3}]}'),
        'draft',due,'infinity',policy,1) RETURNING id INTO original;
    INSERT INTO daily_return_decisions(report_id,decision,decided_by,decided_name,decided_at,note) VALUES
        (original,'returned',reviewer.oidc_subject,reviewer.display_name,due+interval '10 minutes','Check the survey measurement.');

    INSERT INTO situation_reports(workspace_id,requested_by,hierarchy_id,unit_path,period_from,period_to,
        content,status,created_at,expires_at,daily_policy_id,daily_revision,daily_previous_id)
    VALUES(ws,river_officer.oidc_subject,hierarchy,'ds.c2.d3',closing_day,closing_day,
        template || jsonb_build_object('scope','River Division','dailyReturn',(template->'dailyReturn') ||
            jsonb_build_object('author',river_officer.display_name) || '{"nil":false,"remarks":"Corrected after checking the field survey.","totals":{"source.radio":1,"incident.new":1,"area.field.approved":2.4},"facts":[{"kind":"source","key":"demo-radio","metric":"source.radio","amount":1},{"kind":"new","key":"demo-reed","metric":"incident.new","amount":1},{"kind":"area","key":"demo-reed","metric":"area.field.approved","amount":2.4}]}'),
        'draft',due+interval '20 minutes','infinity',policy,2,original) RETURNING id INTO correction;
    INSERT INTO daily_return_decisions(report_id,decision,decided_by,decided_name,decided_at,note) VALUES
        (nil_return,'approved',reviewer.oidc_subject,reviewer.display_name,due+interval '30 minutes',''),
        (correction,'approved',reviewer.oidc_subject,reviewer.display_name,due+interval '30 minutes','');
END;
$$;
COMMIT;
SELECT 'Daily return demo day: ' || max(r.period_from)::text
FROM situation_reports r JOIN workspaces w ON w.id=r.workspace_id
WHERE w.slug='demo' AND r.daily_policy_id IS NOT NULL;
