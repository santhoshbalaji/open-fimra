-- Forms, frozen versions, assignments, submissions, attachments and
-- approvals. A made-up workspace and a point over open sea.

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO workspaces (id, slug, name)
VALUES ('00000000-0000-0000-0000-000000000008', 'forms-test', 'Forms Test');
INSERT INTO users (id, oidc_subject, username, display_name)
VALUES ('00000000-0000-0000-0000-0000000008a1', 'forms-test-user', 'forms.test', 'Forms Test User');
SELECT ensure_detection_partition(date '2000-01-01');
WITH d AS (
    INSERT INTO detections (source, product, source_key, observed_at, geom)
    VALUES ('forms_test', 'P', 'one', '2000-01-01 06:00Z', ST_SetSRID(ST_Point(85.0, 15.0), 4326))
    RETURNING id, observed_at
), m AS (
    INSERT INTO detection_matches (workspace_id, detection_id, observed_at, forest_class)
    SELECT '00000000-0000-0000-0000-000000000008', id, observed_at, 'inside' FROM d RETURNING *
)
INSERT INTO alerts (workspace_id, detection_id, observed_at, hierarchy, queue, hierarchy_unit_paths, unit_paths, forest_class, layer_version_ids)
SELECT workspace_id, detection_id, observed_at, '{}', 'triage', '{}', '{}', 'inside', '{}' FROM m;

CREATE TEMP TABLE ids AS
SELECT (SELECT id FROM alerts WHERE workspace_id = '00000000-0000-0000-0000-000000000008') AS alert,
       (SELECT i.template_id FROM workflow_instances i JOIN alerts a ON a.id=i.alert_id WHERE a.workspace_id='00000000-0000-0000-0000-000000000008' LIMIT 1) AS template;

INSERT INTO forms (workspace_id, form_key) VALUES ('00000000-0000-0000-0000-000000000008', 'test-report');
INSERT INTO form_versions (form_id, version, source, definition, columns)
SELECT id, v, '{"version":' || v || '}', jsonb_build_object('version', v, 'title', jsonb_build_object('en', 'Test report')), '[]'
FROM forms CROSS JOIN generate_series(1, 2) v WHERE form_key = 'test-report';

DO $$
BEGIN
    BEGIN
        INSERT INTO forms (workspace_id, form_key) VALUES ('00000000-0000-0000-0000-000000000008', 'Bad Key');
        RAISE EXCEPTION 'a form key with capitals and a space should be refused';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        INSERT INTO form_versions (form_id, version, source, definition, columns)
        SELECT id, 3, '{}', '{"version": 4}', '[]' FROM forms WHERE form_key = 'test-report';
        RAISE EXCEPTION 'the file and the row should give the same version number';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    -- A draft can be assigned nowhere, but it can change.
    BEGIN
        INSERT INTO form_assignments (form_id, form_version_id, template_id, step)
        SELECT f.id, v.id, (SELECT template FROM ids), 'first_report'
        FROM forms f JOIN form_versions v ON v.form_id = f.id WHERE v.version = 1 AND f.form_key = 'test-report';
        RAISE EXCEPTION 'a draft should not be assignable';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    UPDATE form_versions SET source = source || ' ' WHERE version = 1
      AND form_id = (SELECT id FROM forms WHERE form_key = 'test-report');
    DELETE FROM form_versions WHERE version = 2 AND form_id = (SELECT id FROM forms WHERE form_key = 'test-report');
END $$;

UPDATE form_versions SET published_at = now(), published_by = '00000000-0000-0000-0000-0000000008a1'
WHERE form_id = (SELECT id FROM forms WHERE form_key = 'test-report');

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM audit_log WHERE action = 'form.publish' AND workspace_id = '00000000-0000-0000-0000-000000000008') THEN
        RAISE EXCEPTION 'publishing should be audited';
    END IF;
    BEGIN
        UPDATE form_versions SET source = '{}' WHERE form_id = (SELECT id FROM forms WHERE form_key = 'test-report');
        RAISE EXCEPTION 'a published version should never change';
    EXCEPTION WHEN insufficient_privilege THEN NULL;
    END;
    BEGIN
        DELETE FROM form_versions WHERE form_id = (SELECT id FROM forms WHERE form_key = 'test-report');
        RAISE EXCEPTION 'a published version should never be deleted';
    EXCEPTION WHEN insufficient_privilege THEN NULL;
    END;
END $$;

-- An assignment limited to unit paths does not cover an alert outside them.
INSERT INTO hierarchies (id, workspace_id, slug, name, kind)
VALUES ('00000000-0000-0000-0000-000000000080', '00000000-0000-0000-0000-000000000008', 'forms', 'Forms', 'territorial');
INSERT INTO form_assignments (form_id, form_version_id, template_id, step, hierarchy_id, unit_paths)
SELECT f.id, v.id, (SELECT template FROM ids), 'first_report', '00000000-0000-0000-0000-000000000080', '{x.y}'
FROM forms f JOIN form_versions v ON v.form_id = f.id WHERE f.form_key = 'test-report';
DO $$
BEGIN
    IF EXISTS (SELECT FROM alert_forms((SELECT alert FROM ids))) THEN
        RAISE EXCEPTION 'an alert outside the assigned units should need no form';
    END IF;
END $$;
UPDATE form_assignments SET hierarchy_id = NULL, unit_paths = '{}';

-- At the first report step, leaving needs the form; an outcome does not.
UPDATE workflow_instances SET current_step = 'first_report' WHERE alert_id = (SELECT alert FROM ids);
DO $$
BEGIN
    IF (SELECT count(*) FROM alert_missing_forms((SELECT alert FROM ids), 'final_report')) <> 1
       OR EXISTS (SELECT FROM alert_missing_forms((SELECT alert FROM ids), 'false_alarm')) THEN
        RAISE EXCEPTION 'the first report should be needed to move on, but not for an outcome';
    END IF;
END $$;

INSERT INTO attachments (id, workspace_id, alert_id, sha256, size_bytes, content_type, file_name, uploaded_by)
SELECT '00000000-0000-4000-8000-0000000008f1', '00000000-0000-0000-0000-000000000008', alert, repeat('a', 64), 10,
       'image/jpeg', 'site.jpg', '00000000-0000-0000-0000-0000000008a1' FROM ids;
INSERT INTO submissions (id, workspace_id, form_version_id, alert_id, template_id, step, user_id, answers, request_hash)
SELECT '00000000-0000-4000-8000-0000000008b1', '00000000-0000-0000-0000-000000000008', v.id, i.alert, i.template,
       'first_report', '00000000-0000-0000-0000-0000000008a1', '{"notes": "Synthetic"}', repeat('0', 64)
FROM ids i CROSS JOIN form_versions v JOIN forms f ON f.id = v.form_id WHERE f.form_key = 'test-report';
INSERT INTO submission_attachments VALUES ('00000000-0000-4000-8000-0000000008b1', '00000000-0000-4000-8000-0000000008f1', 'photos');

DO $$
BEGIN
    IF EXISTS (SELECT FROM alert_missing_forms((SELECT alert FROM ids), 'final_report')) THEN
        RAISE EXCEPTION 'a submission should complete the step';
    END IF;
    IF NOT EXISTS (SELECT FROM audit_log WHERE action = 'submission.create' AND target_id = '00000000-0000-4000-8000-0000000008b1') THEN
        RAISE EXCEPTION 'submissions should be audited';
    END IF;
    BEGIN
        INSERT INTO approvals (submission_id, decision, user_id)
        VALUES ('00000000-0000-4000-8000-0000000008b1', 'returned', '00000000-0000-0000-0000-0000000008a1');
        RAISE EXCEPTION 'a returned submission should need a note';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        UPDATE submissions SET answers = '{}';
        RAISE EXCEPTION 'submissions should be append-only';
    EXCEPTION WHEN insufficient_privilege THEN NULL;
    END;
    BEGIN
        UPDATE attachments SET sha256 = repeat('b', 64);
        RAISE EXCEPTION 'attachments should never be replaced';
    EXCEPTION WHEN insufficient_privilege THEN NULL;
    END;
END $$;

INSERT INTO approvals (submission_id, decision, note, user_id)
VALUES ('00000000-0000-4000-8000-0000000008b1', 'returned', 'Add the team size.', '00000000-0000-0000-0000-0000000008a1');
DO $$
BEGIN
    IF (SELECT count(*) FROM alert_missing_forms((SELECT alert FROM ids), 'final_report')) <> 1 THEN
        RAISE EXCEPTION 'a returned submission should no longer count';
    END IF;
    IF NOT EXISTS (SELECT FROM audit_log WHERE action = 'submission.return' AND target_id = '00000000-0000-4000-8000-0000000008b1') THEN
        RAISE EXCEPTION 'decisions should be audited';
    END IF;
    BEGIN
        INSERT INTO approvals (submission_id, decision, user_id)
        VALUES ('00000000-0000-4000-8000-0000000008b1', 'approved', '00000000-0000-0000-0000-0000000008a1');
        RAISE EXCEPTION 'a submission should have one decision';
    EXCEPTION WHEN unique_violation THEN NULL;
    END;
    BEGIN
        DELETE FROM approvals;
        RAISE EXCEPTION 'approvals should be append-only';
    EXCEPTION WHEN insufficient_privilege THEN NULL;
    END;
END $$;

ROLLBACK;
