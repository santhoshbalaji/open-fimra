-- Incident grouping, merging and the shared workflow tables. Made-up points
-- over open sea.

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO workspaces (id, slug, name)
VALUES ('00000000-0000-0000-0000-000000000006', 'incident-test', 'Incident Test');
SELECT ensure_detection_partition(date '2000-01-01');

-- Two fires 2,000 metres apart, then a third between them: the third merges
-- the two incidents into the one with the earliest observation.
CREATE TEMP TABLE points (name text, detection bigint, at timestamptz, lon float8);
WITH added AS (
    INSERT INTO detections (source, product, source_key, observed_at, geom)
    SELECT 'incident_test', 'P', name, at, ST_SetSRID(ST_Point(lon, 16.0), 4326)
    FROM (VALUES ('west', timestamptz '2000-01-01 06:00Z', 86.0), ('east', '2000-01-01 07:00Z', 86.01868),
                 ('middle', '2000-01-01 08:00Z', 86.00934), ('late', '2000-01-02 09:00Z', 86.0)) p(name, at, lon)
    RETURNING id, source_key, observed_at, ST_X(geom) AS lon
)
INSERT INTO points SELECT source_key, id, observed_at, lon FROM added;

INSERT INTO detection_matches (workspace_id, detection_id, observed_at, forest_class)
SELECT '00000000-0000-0000-0000-000000000006', detection, at, 'inside' FROM points;

CREATE TEMP VIEW members AS
SELECT p.name, m.incident_id, m.first_incident_id
FROM points p JOIN alerts a ON a.detection_id = p.detection AND a.workspace_id = '00000000-0000-0000-0000-000000000006'
JOIN incident_members m ON m.alert_id = a.id;

CREATE FUNCTION pg_temp.alert_points(names text[]) RETURNS void LANGUAGE sql AS $$
    INSERT INTO alerts (workspace_id, detection_id, observed_at, hierarchy, queue, hierarchy_unit_paths,
                        unit_paths, forest_class, layer_version_ids)
    SELECT '00000000-0000-0000-0000-000000000006', detection, at, '{}', 'triage', '{}', '{}', 'inside', '{}'
    FROM points WHERE name = ANY (names);
$$;

SELECT pg_temp.alert_points(ARRAY['west', 'east']);
DO $$
BEGIN
    IF (SELECT count(DISTINCT incident_id) FROM members) <> 2 THEN
        RAISE EXCEPTION 'fires 2,000 metres apart should start two incidents';
    END IF;
END $$;

SELECT pg_temp.alert_points(ARRAY['middle']);
DO $$
DECLARE
    west bigint := (SELECT incident_id FROM members WHERE name = 'west');
    east_first bigint := (SELECT first_incident_id FROM members WHERE name = 'east');
BEGIN
    IF (SELECT count(DISTINCT incident_id) FROM members) <> 1 OR west <> (SELECT min(first_incident_id) FROM members) THEN
        RAISE EXCEPTION 'a bridging fire should merge both incidents into the earliest';
    END IF;
    IF (SELECT merged_into FROM incidents WHERE id = east_first) <> west THEN
        RAISE EXCEPTION 'the merged incident should point to the one that continues';
    END IF;
    IF (SELECT (first_observed_at, last_observed_at) FROM incidents WHERE id = west)
       <> (timestamptz '2000-01-01 06:00Z', timestamptz '2000-01-01 08:00Z') THEN
        RAISE EXCEPTION 'the incident should span its members';
    END IF;
    IF NOT EXISTS (SELECT FROM audit_log WHERE action = 'incident.merge' AND target_id = west::text) THEN
        RAISE EXCEPTION 'a merge should be audited';
    END IF;
END $$;

-- 25 hours after the latest member, at the first place: a new incident.
SELECT pg_temp.alert_points(ARRAY['late']);
DO $$
BEGIN
    IF (SELECT count(DISTINCT incident_id) FROM members) <> 2 THEN
        RAISE EXCEPTION 'a fire outside the time window should start its own incident';
    END IF;
    IF (SELECT count(*) FROM step_log l JOIN incidents i ON i.id = l.incident_id
        WHERE i.workspace_id = '00000000-0000-0000-0000-000000000006' AND l.to_step = 'new' AND l.location IS NULL) <> 3 THEN
        RAISE EXCEPTION 'each incident should open at New without a location';
    END IF;
END $$;

-- A log entry or workflow belongs to an alert or an incident, never both.
DO $$
BEGIN
    INSERT INTO workflow_instances (alert_id, incident_id, template_id, current_step, entered_at)
    SELECT a.id, m.incident_id, i.template_id, 'new', now()
    FROM alerts a JOIN incident_members m ON m.alert_id = a.id JOIN workflow_instances i ON i.alert_id = a.id LIMIT 1;
    RAISE EXCEPTION 'a workflow for both an alert and an incident should be rejected';
EXCEPTION WHEN check_violation OR unique_violation THEN NULL;
END $$;

DO $$
BEGIN
    UPDATE step_log SET note = 'changed' WHERE incident_id IS NOT NULL;
    RAISE EXCEPTION 'incident steps should be append-only';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

-- Settings outside the accepted range fall back to the defaults.
DO $$
BEGIN
    IF (SELECT (distance_m, time_window) FROM incident_rule('{"incidents":{"distanceMetres":50,"windowHours":2.5}}'))
       <> (1500::float8, interval '24 hours')
       OR (SELECT (distance_m, time_window) FROM incident_rule('{"incidents":{"distanceMetres":3000,"windowHours":6}}'))
       <> (3000::float8, interval '6 hours') THEN
        RAISE EXCEPTION 'unexpected grouping rule';
    END IF;
END $$;

ROLLBACK;
