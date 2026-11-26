BEGIN;

DO $$
DECLARE
    workspace uuid;
    actor uuid;
    template bigint;
    detection bigint;
    first_alert bigint;
    second_alert bigint;
    assessment bigint;
    provisional uuid := gen_random_uuid();
    revised uuid := gen_random_uuid();
    observed timestamptz := '2001-04-01 12:00Z';
BEGIN
    INSERT INTO workspaces(slug, name) VALUES ('test-assessment', 'Test assessment') RETURNING id INTO workspace;
    INSERT INTO users(oidc_subject, username, display_name) VALUES ('test-assessment', 'test.assessment', 'Test officer') RETURNING id INTO actor;
    template := workspace_workflow_template(workspace);
    PERFORM ensure_detection_partition('2001-04-01');
    INSERT INTO detections(source, product, observed_at, geom, source_key)
    VALUES ('demo_fsi', 'test', observed, ST_SetSRID(ST_MakePoint(86,16),4326), 'test')
    RETURNING id INTO detection;
    INSERT INTO detection_matches(workspace_id, detection_id, observed_at, forest_class)
    VALUES (workspace, detection, observed, 'inside');
    INSERT INTO alerts(workspace_id, detection_id, observed_at, hierarchy, queue, hierarchy_unit_paths, unit_paths, forest_class, layer_version_ids)
    VALUES (workspace, detection, observed, '{}', 'triage', '{}', '{}', 'inside', '{}') RETURNING id INTO first_alert;
    INSERT INTO detections(source, product, observed_at, geom, source_key)
    VALUES ('demo_fsi', 'test', observed, ST_SetSRID(ST_MakePoint(86,16),4326), 'test-second')
    RETURNING id INTO detection;
    INSERT INTO detection_matches(workspace_id, detection_id, observed_at, forest_class)
    VALUES (workspace, detection, observed, 'inside');
    INSERT INTO alerts(workspace_id, detection_id, observed_at, hierarchy, queue, hierarchy_unit_paths, unit_paths, forest_class, layer_version_ids)
    VALUES (workspace, detection, observed, '{}', 'triage', '{}', '{}', 'inside', '{}') RETURNING id INTO second_alert;

    INSERT INTO forms(workspace_id, form_key) VALUES (workspace, 'damage-assessment') RETURNING id INTO assessment;
    INSERT INTO form_versions(form_id, version, source, definition, columns, published_at, published_by)
    VALUES (assessment, 1, '{"version":1}', '{"version":1}', '[]', now(), actor) RETURNING id INTO assessment;
    INSERT INTO submissions(id, workspace_id, form_version_id, alert_id, template_id, step, user_id, answers, request_hash)
    VALUES (provisional, workspace, assessment, first_alert, template, 'final_report', actor, '{"area_ha":4}', repeat('a',64));
    INSERT INTO submissions(id, workspace_id, form_version_id, alert_id, template_id, step, user_id, answers, request_hash, provisional_submission_id)
    VALUES (revised, workspace, assessment, first_alert, template, 'final_report', actor, '{"area_ha":3}', repeat('b',64), provisional);
    BEGIN
        INSERT INTO submissions(id, workspace_id, form_version_id, alert_id, template_id, step, user_id, answers, request_hash, provisional_submission_id)
        VALUES (gen_random_uuid(), workspace, assessment, second_alert, template, 'final_report', actor, '{}', repeat('c',64), provisional);
        RAISE EXCEPTION 'A revision cannot link to a different alert';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        INSERT INTO submissions(id, workspace_id, form_version_id, alert_id, template_id, step, user_id, answers, request_hash, provisional_submission_id)
        VALUES (gen_random_uuid(), workspace, assessment, first_alert, template, 'final_report', actor, '{}', repeat('d',64), revised);
        RAISE EXCEPTION 'A revision must link to a provisional assessment';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        INSERT INTO source_feedback(alert_id, verification_id, external_alert_ref, payload, status, submitted_at)
        VALUES (first_alert, provisional, 'DEMO-FSI-TEST', '{}', 'accepted', now());
        RAISE EXCEPTION 'Acceptance must have a decision time';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END;
$$;

ROLLBACK;
