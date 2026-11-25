-- Exports of field reports freeze their rows like other exports, and each row
-- names a submission of the same alert. A made-up workspace and points over
-- open sea.

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO workspaces (id, slug, name)
VALUES ('00000000-0000-0000-0000-000000000010', 'report-export-test', 'Report Export Test');
INSERT INTO users (id, oidc_subject, username, display_name)
VALUES ('00000000-0000-0000-0000-0000000010a1', 'report-export-user', 'report.export', 'Report Export User');
SELECT ensure_detection_partition(date '2000-01-01');
WITH d AS (
    INSERT INTO detections (source, product, source_key, observed_at, geom)
    VALUES ('report_export_test', 'P', 'one', '2000-01-01 06:00Z', ST_SetSRID(ST_Point(85.0, 15.0), 4326)),
           ('report_export_test', 'P', 'two', '2000-01-01 07:00Z', ST_SetSRID(ST_Point(85.1, 15.1), 4326))
    RETURNING id, observed_at
), m AS (
    INSERT INTO detection_matches (workspace_id, detection_id, observed_at, forest_class)
    SELECT '00000000-0000-0000-0000-000000000010', id, observed_at, 'inside' FROM d RETURNING *
)
INSERT INTO alerts (workspace_id, detection_id, observed_at, hierarchy, queue, hierarchy_unit_paths, unit_paths, forest_class, layer_version_ids)
SELECT workspace_id, detection_id, observed_at, '{}', 'triage', '{}', '{}', 'inside', '{}' FROM m;

CREATE TEMP TABLE ids AS
SELECT (SELECT min(id) FROM alerts WHERE workspace_id = '00000000-0000-0000-0000-000000000010') AS first_alert,
       (SELECT max(id) FROM alerts WHERE workspace_id = '00000000-0000-0000-0000-000000000010') AS second_alert,
       (SELECT id FROM workflow_templates WHERE workspace_id IS NULL) AS template;

INSERT INTO forms (workspace_id, form_key) VALUES ('00000000-0000-0000-0000-000000000010', 'export-report');
INSERT INTO form_versions (form_id, version, source, definition, columns, published_at, published_by)
SELECT id, 1, '{"version":1}', '{"version": 1, "title": {"en": "Export report"}}', '[]', now(), '00000000-0000-0000-0000-0000000010a1'
FROM forms WHERE form_key = 'export-report';
INSERT INTO submissions (id, workspace_id, form_version_id, alert_id, template_id, step, user_id, answers, request_hash)
SELECT '00000000-0000-4000-8000-0000000010b1', '00000000-0000-0000-0000-000000000010', v.id, i.first_alert, i.template,
       'first_report', '00000000-0000-0000-0000-0000000010a1', '{}', repeat('0', 64)
FROM ids i CROSS JOIN form_versions v JOIN forms f ON f.id = v.form_id WHERE f.form_key = 'export-report';

INSERT INTO exports (id, workspace_id, requested_by, kind, format, filters, requester_roles, row_count, expires_at)
VALUES ('00000000-0000-0000-0000-000000001011', '00000000-0000-0000-0000-000000000010', 'subject',
        'submissions', 'gpkg', '{}', '[]', 1, now() + interval '1 day'),
       ('00000000-0000-0000-0000-000000001012', '00000000-0000-0000-0000-000000000010', 'subject',
        'submissions', 'gpkg', '{}', '[]', 1, now() + interval '1 day'),
       ('00000000-0000-0000-0000-000000001013', '00000000-0000-0000-0000-000000000010', 'subject',
        'alerts', 'gpkg', '{}', '[]', 1, now() + interval '1 day');

DO $$
BEGIN
    -- A report row needs its submission.
    BEGIN
        INSERT INTO export_rows (export_id, ordinal, workspace_id, detection_id, observed_at, alert_id)
        SELECT '00000000-0000-0000-0000-000000001012', 1, a.workspace_id, a.detection_id, a.observed_at, a.id
        FROM alerts a WHERE a.id = (SELECT first_alert FROM ids);
        RAISE EXCEPTION 'a report export row without a submission should be refused';
    EXCEPTION WHEN insufficient_privilege THEN NULL;
    END;
    -- The submission must belong to the row's alert.
    BEGIN
        INSERT INTO export_rows (export_id, ordinal, workspace_id, detection_id, observed_at, alert_id, submission_id)
        SELECT '00000000-0000-0000-0000-000000001012', 1, a.workspace_id, a.detection_id, a.observed_at, a.id,
               '00000000-0000-4000-8000-0000000010b1'
        FROM alerts a WHERE a.id = (SELECT second_alert FROM ids);
        RAISE EXCEPTION 'a submission under another alert should be refused';
    EXCEPTION WHEN insufficient_privilege THEN NULL;
    END;
    -- An alert export carries no submissions.
    BEGIN
        INSERT INTO export_rows (export_id, ordinal, workspace_id, detection_id, observed_at, alert_id, submission_id)
        SELECT '00000000-0000-0000-0000-000000001013', 1, a.workspace_id, a.detection_id, a.observed_at, a.id,
               '00000000-0000-4000-8000-0000000010b1'
        FROM alerts a WHERE a.id = (SELECT first_alert FROM ids);
        RAISE EXCEPTION 'an alert export row with a submission should be refused';
    EXCEPTION WHEN insufficient_privilege THEN NULL;
    END;
END $$;

-- The submission under its own alert is accepted, once.
INSERT INTO export_rows (export_id, ordinal, workspace_id, detection_id, observed_at, alert_id, submission_id)
SELECT '00000000-0000-0000-0000-000000001011', 1, a.workspace_id, a.detection_id, a.observed_at, a.id,
       '00000000-0000-4000-8000-0000000010b1'
FROM alerts a WHERE a.id = (SELECT first_alert FROM ids);

DO $$
BEGIN
    INSERT INTO export_rows (export_id, ordinal, workspace_id, detection_id, observed_at, alert_id, submission_id)
    SELECT '00000000-0000-0000-0000-000000001011', 2, a.workspace_id, a.detection_id, a.observed_at, a.id,
           '00000000-0000-4000-8000-0000000010b1'
    FROM alerts a WHERE a.id = (SELECT first_alert FROM ids);
    RAISE EXCEPTION 'a row beyond the frozen row count should be refused';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

DO $$
BEGIN
    UPDATE export_rows SET submission_id = NULL WHERE export_id = '00000000-0000-0000-0000-000000001011';
    RAISE EXCEPTION 'changing a frozen report row should be refused';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

ROLLBACK;
