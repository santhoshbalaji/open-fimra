-- XLSForm conversion jobs hold the right source for their direction, and a
-- finished job holds the right result.

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO workspaces (id, slug, name)
VALUES ('00000000-0000-0000-0000-00000000f001', 'form-conversion-test', 'Form Conversion Test');

INSERT INTO form_conversions (id, workspace_id, requested_by, direction, source_name, source_file)
VALUES ('00000000-0000-0000-0000-00000000f011', '00000000-0000-0000-0000-00000000f001', 'subject', 'import',
        'demo.xlsx', '\x504b');
INSERT INTO form_conversions (id, workspace_id, requested_by, direction, source_form)
VALUES ('00000000-0000-0000-0000-00000000f012', '00000000-0000-0000-0000-00000000f001', 'subject', 'export',
        '{"formatVersion": 1}');

-- A finished import holds its draft, and a finished export its workbook.
UPDATE form_conversions SET status = 'ready', draft_form = '{"id": "demo"}', finished_at = now()
WHERE id = '00000000-0000-0000-0000-00000000f011';
UPDATE form_conversions SET status = 'ready', result_file = '\x504b', finished_at = now()
WHERE id = '00000000-0000-0000-0000-00000000f012';

DO $$
BEGIN
    INSERT INTO form_conversions (workspace_id, requested_by, direction, source_file, source_form)
    VALUES ('00000000-0000-0000-0000-00000000f001', 'subject', 'export', '\x504b', '{}');
    RAISE EXCEPTION 'an export should not take an uploaded file';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

DO $$
BEGIN
    INSERT INTO form_conversions (workspace_id, requested_by, direction, source_name)
    VALUES ('00000000-0000-0000-0000-00000000f001', 'subject', 'import', 'demo.xlsx');
    RAISE EXCEPTION 'an import should need its workbook';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

DO $$
BEGIN
    UPDATE form_conversions SET status = 'ready', draft_form = NULL, result_file = '\x504b'
    WHERE id = '00000000-0000-0000-0000-00000000f011';
    RAISE EXCEPTION 'a finished import should hold a draft and no workbook';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

DO $$
BEGIN
    UPDATE form_conversions SET status = 'failed', draft_form = NULL
    WHERE id = '00000000-0000-0000-0000-00000000f011';
    RAISE EXCEPTION 'a failed job should give its reason';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

ROLLBACK;
