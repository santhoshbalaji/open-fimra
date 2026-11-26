BEGIN;
DO $$
DECLARE
    ws uuid;
    hierarchy uuid;
    caller uuid;
    liaison uuid;
BEGIN
    INSERT INTO workspaces(slug,name) VALUES ('test-limited-roles','Test limited roles') RETURNING id INTO ws;
    INSERT INTO hierarchies(workspace_id,slug,name,kind)
    VALUES (ws,'test','Test','territorial') RETURNING id INTO hierarchy;
    INSERT INTO hierarchy_levels(hierarchy_id,depth,slug,labels) VALUES
        (hierarchy,0,'state','{"en":"State"}'),
        (hierarchy,1,'division','{"en":"Division"}'),
        (hierarchy,2,'range','{"en":"Range"}');
    INSERT INTO units(hierarchy_id,depth,code,name,path) VALUES
        (hierarchy,0,'TEST','Test State','test'),
        (hierarchy,1,'D1','Test Division','test.d1'),
        (hierarchy,2,'R1','Test Range One','test.d1.r1'),
        (hierarchy,2,'R2','Test Range Two','test.d1.r2');
    INSERT INTO users(oidc_subject,username,display_name)
    VALUES ('test-community-caller','test.caller','Test Caller') RETURNING id INTO caller;
    INSERT INTO users(oidc_subject,username,display_name)
    VALUES ('test-district-liaison','test.liaison','Test Liaison') RETURNING id INTO liaison;
    INSERT INTO role_assignments(user_id,workspace_id,hierarchy_id,unit_path,role_slug) VALUES
        (caller,ws,hierarchy,'test.d1.r1','community_caller'),
        (liaison,ws,hierarchy,'test.d1.r1','district_liaison'),
        (liaison,ws,hierarchy,'test.d1.r2','district_liaison');

    ASSERT (SELECT count(*) FROM alert_grants WHERE workspace_id=ws AND NOT can_review
            AND role_slug IN ('community_caller','district_liaison'))=3,
        'the limited roles can read their ranges without a triage grant';
    ASSERT (SELECT count(*) FROM alert_grants g WHERE g.workspace_id=ws AND
            alert_path_visible('unit',jsonb_build_object(hierarchy,'["test.d1.r2"]'::jsonb),
                g.can_review,g.hierarchy_id,g.unit_path,g.depth))=1,
        'only the liaison has visibility in the second range';
END $$;
ROLLBACK;
