-- Export requests freeze their rows: only the request's own transaction can
-- add them, and nothing can widen them later.

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO workspaces (id, slug, name)
VALUES ('00000000-0000-0000-0000-000000000e01', 'export-test', 'Export Test');
SELECT ensure_detection_partition('2001-01-01');
INSERT INTO detections (source, product, observed_at, geom, source_key)
VALUES ('test', 'P', '2001-01-01 10:00+00', ST_SetSRID(ST_Point(86, 16), 4326), 'export-1'),
       ('test', 'P', '2001-01-01 11:00+00', ST_SetSRID(ST_Point(86, 16), 4326), 'export-2');
INSERT INTO detection_matches (workspace_id, detection_id, observed_at, forest_class)
SELECT '00000000-0000-0000-0000-000000000e01', id, observed_at, 'inside' FROM detections WHERE source = 'test';

-- An export made in this transaction takes exactly its row count.
INSERT INTO exports (id, workspace_id, requested_by, kind, format, filters, requester_roles, row_count, expires_at)
VALUES ('00000000-0000-0000-0000-000000000e11', '00000000-0000-0000-0000-000000000e01', 'subject',
        'detections', 'gpkg', '{}', '[]', 1, now() + interval '1 day');
INSERT INTO export_rows (export_id, ordinal, workspace_id, detection_id, observed_at)
SELECT '00000000-0000-0000-0000-000000000e11', 1, workspace_id, detection_id, observed_at
FROM detection_matches WHERE workspace_id = '00000000-0000-0000-0000-000000000e01' ORDER BY observed_at LIMIT 1;

DO $$
BEGIN
    INSERT INTO export_rows (export_id, ordinal, workspace_id, detection_id, observed_at)
    SELECT '00000000-0000-0000-0000-000000000e11', 2, workspace_id, detection_id, observed_at
    FROM detection_matches WHERE workspace_id = '00000000-0000-0000-0000-000000000e01' ORDER BY observed_at DESC LIMIT 1;
    RAISE EXCEPTION 'a row beyond the frozen row count should be rejected';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

-- An export from another transaction, such as the worker's, takes no rows.
INSERT INTO exports (id, workspace_id, requested_by, kind, format, filters, requester_roles, row_count,
                     expires_at, created_xact)
VALUES ('00000000-0000-0000-0000-000000000e12', '00000000-0000-0000-0000-000000000e01', 'subject',
        'detections', 'gpkg', '{}', '[]', 1, now() + interval '1 day', '1');
DO $$
BEGIN
    INSERT INTO export_rows (export_id, ordinal, workspace_id, detection_id, observed_at)
    SELECT '00000000-0000-0000-0000-000000000e12', 1, workspace_id, detection_id, observed_at
    FROM detection_matches WHERE workspace_id = '00000000-0000-0000-0000-000000000e01' LIMIT 1;
    RAISE EXCEPTION 'rows added outside the request transaction should be rejected';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

-- Alert exports need an alert on every row.
INSERT INTO exports (id, workspace_id, requested_by, kind, format, filters, requester_roles, row_count, expires_at)
VALUES ('00000000-0000-0000-0000-000000000e13', '00000000-0000-0000-0000-000000000e01', 'subject',
        'alerts', 'gpkg', '{}', '[]', 1, now() + interval '1 day');
DO $$
BEGIN
    INSERT INTO export_rows (export_id, ordinal, workspace_id, detection_id, observed_at)
    SELECT '00000000-0000-0000-0000-000000000e13', 1, workspace_id, detection_id, observed_at
    FROM detection_matches WHERE workspace_id = '00000000-0000-0000-0000-000000000e01' LIMIT 1;
    RAISE EXCEPTION 'an alert export row without an alert should be rejected';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

-- Saved rows and the request cannot change.
DO $$
BEGIN
    UPDATE export_rows SET observed_at = observed_at + interval '1 hour';
    RAISE EXCEPTION 'changing an export row should be rejected';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

DO $$
BEGIN
    UPDATE exports SET row_count = 2 WHERE id = '00000000-0000-0000-0000-000000000e11';
    RAISE EXCEPTION 'changing the frozen row count should be rejected';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

DO $$
BEGIN
    UPDATE exports SET filters = '{"hours": 720}' WHERE id = '00000000-0000-0000-0000-000000000e11';
    RAISE EXCEPTION 'changing the filters should be rejected';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

-- A finished file can never hold more rows than were frozen.
DO $$
BEGIN
    UPDATE exports SET status = 'ready', finished_at = now(), file_key = '00000000-0000-0000-0000-000000000e11.gpkg',
           file_bytes = 10, feature_count = 2
    WHERE id = '00000000-0000-0000-0000-000000000e11';
    RAISE EXCEPTION 'a file with more features than frozen rows should be rejected';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

UPDATE exports SET status = 'ready', finished_at = now(), file_key = '00000000-0000-0000-0000-000000000e11.gpkg',
       file_bytes = 10, feature_count = 1
WHERE id = '00000000-0000-0000-0000-000000000e11';

-- Expiry removes the rows and the file name.
DELETE FROM export_rows WHERE export_id = '00000000-0000-0000-0000-000000000e11';
UPDATE exports SET status = 'expired', file_key = NULL, file_bytes = NULL, feature_count = NULL
WHERE id = '00000000-0000-0000-0000-000000000e11';

ROLLBACK;
