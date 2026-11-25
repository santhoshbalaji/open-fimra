-- A situation report keeps its request and content, and a finished report
-- holds its PDF. An expired report keeps only the request.

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO workspaces (id, slug, name)
VALUES ('00000000-0000-0000-0000-00000000e001', 'report-test', 'Report Test');

INSERT INTO situation_reports (id, workspace_id, requested_by, period_from, period_to, content, expires_at)
VALUES ('00000000-0000-0000-0000-00000000e011', '00000000-0000-0000-0000-00000000e001', 'subject',
        '2026-03-09', '2026-03-15', '{"scope": "Report Test"}', now() + interval '1 day');

UPDATE situation_reports SET status = 'ready', pdf = '\x255044462d', page_count = 1, finished_at = now()
WHERE id = '00000000-0000-0000-0000-00000000e011';

DO $$
BEGIN
    UPDATE situation_reports SET content = '{"scope": "Other"}' WHERE id = '00000000-0000-0000-0000-00000000e011';
    RAISE EXCEPTION 'a saved report should keep its content';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

DO $$
BEGIN
    UPDATE situation_reports SET period_to = '2026-03-16' WHERE id = '00000000-0000-0000-0000-00000000e011';
    RAISE EXCEPTION 'a saved report should keep its period';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

DO $$
BEGIN
    UPDATE situation_reports SET pdf = NULL WHERE id = '00000000-0000-0000-0000-00000000e011';
    RAISE EXCEPTION 'a ready report should hold its PDF';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

DO $$
BEGIN
    INSERT INTO situation_reports (workspace_id, requested_by, period_from, period_to, content, expires_at)
    VALUES ('00000000-0000-0000-0000-00000000e001', 'subject', '2026-03-15', '2026-03-09', '{}', now());
    RAISE EXCEPTION 'a period should not end before it starts';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

DO $$
BEGIN
    INSERT INTO situation_reports (workspace_id, requested_by, unit_path, period_from, period_to, content, expires_at)
    VALUES ('00000000-0000-0000-0000-00000000e001', 'subject', 'ds.c1', '2026-03-15', '2026-03-15', '{}', now());
    RAISE EXCEPTION 'a unit needs its hierarchy';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

-- Expiry clears the content and the file together.
DO $$
BEGIN
    UPDATE situation_reports SET status = 'expired' WHERE id = '00000000-0000-0000-0000-00000000e011';
    RAISE EXCEPTION 'an expired report should not keep its content';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

UPDATE situation_reports SET status = 'expired', content = '{}', pdf = NULL, page_count = NULL
WHERE id = '00000000-0000-0000-0000-00000000e011';

-- Response times cover every milestone of the default workflow.
DO $$
BEGIN
    IF (SELECT array_agg(attname::text ORDER BY attnum) FROM pg_attribute
        WHERE attrelid = 'alert_response_times'::regclass AND attnum > 0)
       <> ARRAY['workspace_id', 'alert_id', 'to_acknowledged', 'to_reached_site', 'to_controlled',
                'to_team_sent', 'to_extinguished', 'to_alerted'] THEN
        RAISE EXCEPTION 'alert_response_times has unexpected columns';
    END IF;
END $$;

ROLLBACK;
