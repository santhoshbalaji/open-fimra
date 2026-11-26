BEGIN;

DO $$
DECLARE
    workspace uuid;
    another_workspace uuid;
    hierarchy uuid;
    operator uuid;
    record_id uuid := gen_random_uuid();
    referral_id uuid := gen_random_uuid();
    entry_id uuid := gen_random_uuid();
    attachment_id uuid := gen_random_uuid();
    case_id bigint;
    report_version bigint;
    referral_version bigint;
    referral_form bigint;
BEGIN
    INSERT INTO workspaces(slug, name) VALUES ('test-operations', 'Test Operations') RETURNING id INTO workspace;
    INSERT INTO workspaces(slug, name) VALUES ('test-operations-other', 'Other Test Operations') RETURNING id INTO another_workspace;
    INSERT INTO hierarchies(workspace_id, slug, name, kind)
    VALUES (workspace, 'territorial', 'Territorial', 'territorial') RETURNING id INTO hierarchy;
    INSERT INTO hierarchy_levels VALUES (hierarchy, 0, 'state', '{"en":"State"}');
    INSERT INTO units(hierarchy_id, depth, code, name, path) VALUES (hierarchy, 0, 'TS', 'Test State', 'ts');
    INSERT INTO users(oidc_subject, username, display_name)
    VALUES ('test-operations-user', 'test-operations-user', 'Test Operator') RETURNING id INTO operator;
    INSERT INTO role_assignments(user_id, workspace_id, hierarchy_id, unit_path, role_slug)
    VALUES (operator, workspace, hierarchy, 'ts', 'state_admin');
    INSERT INTO incidents(workspace_id, first_observed_at, last_observed_at)
    VALUES (workspace, now(), now()) RETURNING id INTO case_id;
    INSERT INTO operations_records(id, workspace_id, hierarchy_id, unit_path, kind, owner_id)
    VALUES (record_id, workspace, hierarchy, 'ts', 'preparedness', operator);
    INSERT INTO operations_records(id, workspace_id, hierarchy_id, unit_path, kind, incident_id, owner_id)
    VALUES (referral_id, workspace, hierarchy, 'ts', 'offence', case_id, operator);
    IF (SELECT full_access FROM operations_roles('test-operations-user', 'test-operations', referral_id)) IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'administrator needs a referral grant';
    END IF;
    INSERT INTO referral_grants(workspace_id, hierarchy_id, unit_path, user_id, kind)
    VALUES (workspace, hierarchy, 'ts', operator, 'relief');
    IF (SELECT full_access FROM operations_roles('test-operations-user', 'test-operations', referral_id)) IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'a different referral grant should not give access';
    END IF;
    INSERT INTO referral_grants(workspace_id, hierarchy_id, unit_path, user_id, kind)
    VALUES (workspace, hierarchy, 'ts', operator, 'offence');
    IF (SELECT full_access FROM operations_roles('test-operations-user', 'test-operations', referral_id)) IS DISTINCT FROM true OR
       EXISTS (SELECT FROM operations_roles('test-operations-user', 'test-operations-other', referral_id)) THEN
        RAISE EXCEPTION 'a grant should apply only to its own workspace and kind';
    END IF;

    INSERT INTO forms(workspace_id, form_key) VALUES (workspace, 'preparedness-work'), (workspace, 'offence-referral');
    INSERT INTO form_versions(form_id, version, source, definition, columns, published_at, published_by)
    SELECT id, 1, '{"version":1}', '{"version":1}', '[]', now(), operator FROM forms WHERE workspace_id = workspace;
    SELECT v.id INTO report_version FROM form_versions v JOIN forms f ON f.id = v.form_id
    WHERE f.workspace_id = workspace AND f.form_key = 'preparedness-work';
    SELECT v.id, f.id INTO referral_version, referral_form FROM form_versions v JOIN forms f ON f.id = v.form_id
    WHERE f.workspace_id = workspace AND f.form_key = 'offence-referral';

    INSERT INTO submissions(id, workspace_id, form_version_id, operations_record_id, user_id, answers, request_hash)
    VALUES (entry_id, workspace, report_version, record_id, operator, '{"work":"Fictional plan"}', repeat('0', 64));
    BEGIN
        INSERT INTO form_assignments(form_id, form_version_id, template_id, step)
        VALUES (referral_form, referral_version, workspace_workflow_template(workspace), 'first_report');
        RAISE EXCEPTION 'referrals should not be assigned to the alert workflow';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        INSERT INTO submissions(id, workspace_id, form_version_id, operations_record_id, user_id, answers, request_hash)
        VALUES (gen_random_uuid(), workspace, referral_version, record_id, operator, '{}', repeat('0', 64));
        RAISE EXCEPTION 'a referral form should not use a normal record';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        INSERT INTO submissions(id, workspace_id, form_version_id, operations_record_id, user_id, answers, request_hash)
        VALUES (gen_random_uuid(), workspace, referral_version, referral_id, operator, '{"case_reference":"DEMO-1","public_summary":"Review open.","copied_document":{}}', repeat('0', 64));
        RAISE EXCEPTION 'referral values should be limited to references and a summary';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        INSERT INTO attachments(id, workspace_id, operations_record_id, sha256, size_bytes, content_type, file_name, uploaded_by)
        VALUES (attachment_id, workspace, referral_id, repeat('0', 64), 10, 'text/plain', 'test.txt', operator);
        RAISE EXCEPTION 'referrals should not store evidence files';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        INSERT INTO operations_records(id, workspace_id, hierarchy_id, unit_path, kind, incident_id, owner_id)
        VALUES (gen_random_uuid(), another_workspace, hierarchy, 'ts', 'offence', case_id, operator);
        RAISE EXCEPTION 'a referral should not point outside its workspace';
    EXCEPTION WHEN foreign_key_violation THEN NULL;
    END;
    INSERT INTO attachments(id, workspace_id, operations_record_id, sha256, size_bytes, content_type, file_name, uploaded_by)
    VALUES (attachment_id, workspace, record_id, repeat('0', 64), 10, 'text/plain', 'test.txt', operator);
    INSERT INTO submission_attachments VALUES (entry_id, attachment_id, 'notes');
END;
$$;

SET CONSTRAINTS ALL IMMEDIATE;
ROLLBACK;
