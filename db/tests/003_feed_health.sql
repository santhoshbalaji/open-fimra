-- Feed-health alerts, backfill runs and links between records of the same
-- observation.

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO workspaces (id, slug, name)
VALUES ('00000000-0000-0000-0000-000000000001', 'feed-test', 'Feed Test');

-- One open alert per workspace feed.
INSERT INTO feed_health_alerts (workspace_id, source, product, opened_status, status, opened_at, last_checked_at)
VALUES ('00000000-0000-0000-0000-000000000001', 'test', 'P', 'quiet', 'quiet', now(), now());

DO $$
BEGIN
    INSERT INTO feed_health_alerts (workspace_id, source, product, opened_status, status, opened_at, last_checked_at)
    VALUES ('00000000-0000-0000-0000-000000000001', 'test', 'P', 'failing', 'failing', now(), now());
    RAISE EXCEPTION 'a second open alert for the same feed should be rejected';
EXCEPTION WHEN unique_violation THEN NULL;
END $$;

DO $$
BEGIN
    UPDATE feed_health_alerts SET status = 'healthy' WHERE source = 'test';
    RAISE EXCEPTION 'an open alert with a healthy status should be rejected';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

-- After it resolves, the same feed can open a new alert.
UPDATE feed_health_alerts SET status = 'healthy', resolved_at = now() WHERE source = 'test';
INSERT INTO feed_health_alerts (workspace_id, source, product, opened_status, status, opened_at, last_checked_at)
VALUES ('00000000-0000-0000-0000-000000000001', 'test', 'P', 'failing', 'failing', now(), now());

-- A successful run records when its response arrived.
DO $$
BEGIN
    INSERT INTO ingest_runs (source, product, window_start, window_end, status, finished_at)
    VALUES ('test', 'P', now(), now(), 'succeeded', now());
    RAISE EXCEPTION 'a successful run without a fetch time should be rejected';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

DO $$
BEGIN
    INSERT INTO ingest_runs (kind, source, product, window_start, window_end) VALUES ('other', 'test', 'P', now(), now());
    RAISE EXCEPTION 'an unknown run kind should be rejected';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

-- A record links to one earlier record, and an earlier record takes one link.
SELECT ensure_detection_partition('2000-01-01');
INSERT INTO detections (source, product, observed_at, geom, source_key) VALUES
    ('test', 'P_NRT', '2000-01-01 10:00+00', ST_SetSRID(ST_Point(86.3, 16.4), 4326), 'a'),
    ('test', 'P_SP', '2000-01-01 10:00+00', ST_SetSRID(ST_Point(86.3, 16.4), 4326), 'b'),
    ('test', 'P_SP', '2000-01-01 10:00+00', ST_SetSRID(ST_Point(86.3, 16.4), 4326), 'c');

INSERT INTO detection_links (detection_id, observed_at, primary_id, primary_observed_at, reason, distance_m)
SELECT b.id, b.observed_at, a.id, a.observed_at, 'same_observation', 0
FROM detections a, detections b WHERE a.source_key = 'a' AND b.source_key = 'b' AND a.source = 'test' AND b.source = 'test';

DO $$
BEGIN
    INSERT INTO detection_links (detection_id, observed_at, primary_id, primary_observed_at, reason, distance_m)
    SELECT c.id, c.observed_at, a.id, a.observed_at, 'same_observation', 0
    FROM detections a, detections c WHERE a.source_key = 'a' AND c.source_key = 'c' AND a.source = 'test' AND c.source = 'test';
    RAISE EXCEPTION 'a second link to the same earlier record should be rejected';
EXCEPTION WHEN unique_violation THEN NULL;
END $$;

DO $$
BEGIN
    INSERT INTO detection_links (detection_id, observed_at, primary_id, primary_observed_at, reason, distance_m)
    SELECT id, observed_at, id, observed_at, 'same_observation', 0 FROM detections WHERE source_key = 'c' AND source = 'test';
    RAISE EXCEPTION 'a record linked to itself should be rejected';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

-- Deleting either record removes the link.
DELETE FROM detections WHERE source = 'test' AND source_key = 'a';
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM detection_links) THEN
        RAISE EXCEPTION 'the link should go with its earlier record';
    END IF;
END $$;

ROLLBACK;
