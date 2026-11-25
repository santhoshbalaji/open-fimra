-- National, state and division forms that extend each other. A made-up
-- workspace with a two-level hierarchy.

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO workspaces (id, slug, name)
VALUES ('00000000-0000-0000-0000-000000000010', 'templates-test', 'Templates Test');
INSERT INTO hierarchies (id, workspace_id, slug, name, kind)
VALUES ('00000000-0000-0000-0000-0000000010f1', '00000000-0000-0000-0000-000000000010', 'territorial', 'Territorial', 'territorial');
INSERT INTO users (id, oidc_subject, username, display_name)
VALUES ('00000000-0000-0000-0000-0000000010a1', 'templates-test-user', 'templates.test', 'Templates Test User');

INSERT INTO forms (workspace_id, form_key, level) VALUES
    ('00000000-0000-0000-0000-000000000010', 'national-report', 'national'),
    ('00000000-0000-0000-0000-000000000010', 'closed-report', 'national'),
    ('00000000-0000-0000-0000-000000000010', 'state-report', 'state');
INSERT INTO forms (workspace_id, form_key, level, owner_hierarchy_id, owner_unit_path) VALUES
    ('00000000-0000-0000-0000-000000000010', 'division-report', 'division', '00000000-0000-0000-0000-0000000010f1', 'ts.d1');

-- National templates are published in the transaction that adds them.
INSERT INTO form_versions (form_id, version, source, definition, columns, template, published_at, published_by)
SELECT id, 1, '{"version":1}', '{"version":1}', '[]',
       CASE form_key WHEN 'national-report' THEN '{"allowExtension": true, "locked": ["fire"]}'::jsonb
                     ELSE '{"allowExtension": false, "locked": []}'::jsonb END,
       now(), '00000000-0000-0000-0000-0000000010a1'
FROM forms WHERE form_key IN ('national-report', 'closed-report');

CREATE TEMP TABLE ids AS
SELECT (SELECT id FROM forms WHERE form_key = 'state-report') AS state_form,
       (SELECT id FROM forms WHERE form_key = 'division-report') AS division_form,
       (SELECT v.id FROM form_versions v JOIN forms f ON f.id = v.form_id WHERE f.form_key = 'national-report') AS national,
       (SELECT v.id FROM form_versions v JOIN forms f ON f.id = v.form_id WHERE f.form_key = 'closed-report') AS closed;

DO $$
BEGIN
    BEGIN
        INSERT INTO forms (workspace_id, form_key, level) VALUES ('00000000-0000-0000-0000-000000000010', 'no-owner', 'division');
        RAISE EXCEPTION 'a division form needs the unit that owns it';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        INSERT INTO forms (workspace_id, form_key, level, owner_hierarchy_id, owner_unit_path)
        VALUES ('00000000-0000-0000-0000-000000000010', 'top-owner', 'division', '00000000-0000-0000-0000-0000000010f1', 'ts');
        RAISE EXCEPTION 'a division form belongs to a unit below the top';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        UPDATE forms SET level = 'state' WHERE form_key = 'national-report';
        RAISE EXCEPTION 'a form keeps its level';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        INSERT INTO form_versions (form_id, version, source, definition, columns, extends_version_id)
        SELECT state_form, 1, '{}', '{"version": 1}', '[]', closed FROM ids;
        RAISE EXCEPTION 'a template that does not allow extension cannot be extended';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        INSERT INTO form_versions (form_id, version, source, definition, columns)
        SELECT division_form, 1, '{}', '{"version": 1}', '[]' FROM ids;
        RAISE EXCEPTION 'a division form extends a state form';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        INSERT INTO form_versions (form_id, version, source, definition, columns, extends_version_id)
        SELECT division_form, 1, '{}', '{"version": 1}', '[]', national FROM ids;
        RAISE EXCEPTION 'a division form cannot skip the state';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
    BEGIN
        INSERT INTO form_versions (form_id, version, source, definition, columns, template)
        SELECT state_form, 1, '{}', '{"version": 1}', '[]', '{"locked": []}' FROM ids;
        RAISE EXCEPTION 'template rules say whether extension is allowed';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END $$;

-- A state draft extends the national template, and a division form cannot
-- extend the draft until it is published.
INSERT INTO form_versions (form_id, version, source, definition, columns, template, extends_version_id)
SELECT state_form, 1, '{}', '{"version": 1}', '[]', '{"allowExtension": true, "locked": ["fire"]}', national FROM ids;
DO $$
BEGIN
    BEGIN
        INSERT INTO form_versions (form_id, version, source, definition, columns, extends_version_id)
        SELECT division_form, 1, '{}', '{"version": 1}', '[]', v.id
        FROM ids JOIN form_versions v ON v.form_id = ids.state_form;
        RAISE EXCEPTION 'only a published version can be extended';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END $$;
UPDATE form_versions SET published_at = now(), published_by = '00000000-0000-0000-0000-0000000010a1'
WHERE form_id = (SELECT state_form FROM ids);
INSERT INTO form_versions (form_id, version, source, definition, columns, extends_version_id)
SELECT division_form, 1, '{}', '{"version": 1}', '[]', v.id FROM ids JOIN form_versions v ON v.form_id = ids.state_form;

-- A national draft left at the end of a transaction is refused.
SAVEPOINT national_draft;
DO $$
BEGIN
    BEGIN
        INSERT INTO form_versions (form_id, version, source, definition, columns)
        SELECT f.id, 2, '{}', '{"version": 2}', '[]' FROM forms f WHERE f.form_key = 'national-report';
        SET CONSTRAINTS form_national_published IMMEDIATE;
        RAISE EXCEPTION 'a national template is never a draft';
    EXCEPTION WHEN check_violation THEN NULL;
    END;
END $$;
ROLLBACK TO SAVEPOINT national_draft;

ROLLBACK;
