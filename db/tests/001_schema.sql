-- Schema behaviour tests. Run with `make db-test`, which applies every migration
-- first. Everything runs in one transaction and is rolled back at the end.
-- All names and shapes are made up.

\set ON_ERROR_STOP on
BEGIN;

-- A fictional workspace with a five-level territorial hierarchy.
INSERT INTO workspaces (id, slug, name)
VALUES ('00000000-0000-0000-0000-000000000001', 'demo', 'Demo State');

INSERT INTO hierarchies (id, workspace_id, slug, name, kind)
VALUES ('00000000-0000-0000-0000-000000000010', '00000000-0000-0000-0000-000000000001',
        'territorial', 'Territorial', 'territorial');

INSERT INTO hierarchy_levels (hierarchy_id, depth, slug, labels)
SELECT '00000000-0000-0000-0000-000000000010', d, s, jsonb_build_object('en', l)
FROM (VALUES (0, 'state', 'State'), (1, 'circle', 'Circle'), (2, 'division', 'Division'),
             (3, 'range', 'Range'), (4, 'beat', 'Beat')) AS v (d, s, l);

INSERT INTO units (hierarchy_id, depth, code, name, path, geom)
VALUES
  ('00000000-0000-0000-0000-000000000010', 0, 'DS', 'Demo State', 'ds', NULL),
  ('00000000-0000-0000-0000-000000000010', 1, 'C1', 'North Circle', 'ds.c1', NULL),
  ('00000000-0000-0000-0000-000000000010', 2, 'D1', 'Hill Division', 'ds.c1.d1', NULL),
  ('00000000-0000-0000-0000-000000000010', 3, 'R1', 'East Range', 'ds.c1.d1.r1', NULL),
  ('00000000-0000-0000-0000-000000000010', 4, 'B1', 'Beat One', 'ds.c1.d1.r1.b1',
   ST_Multi(ST_MakeEnvelope(77.00, 11.00, 77.10, 11.10, 4326))),
  ('00000000-0000-0000-0000-000000000010', 4, 'B2', 'Beat Two', 'ds.c1.d1.r1.b2',
   ST_Multi(ST_MakeEnvelope(77.10, 11.00, 77.20, 11.10, 4326)));

DO $$
BEGIN
  ASSERT (SELECT count(*) FROM units WHERE path <@ 'ds.c1.d1') = 4,
    'a division should contain itself, one range and two beats';
END $$;

-- A unit's path length must match its level.
DO $$
BEGIN
  INSERT INTO units (hierarchy_id, depth, code, name, path)
  VALUES ('00000000-0000-0000-0000-000000000010', 4, 'BX', 'Wrong depth', 'ds.c1');
  RAISE EXCEPTION 'a beat with a two-level path should be rejected';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

-- Only one current row per path; closing the old row allows a new one.
DO $$
BEGIN
  INSERT INTO units (hierarchy_id, depth, code, name, path)
  VALUES ('00000000-0000-0000-0000-000000000010', 4, 'B1', 'Beat One again', 'ds.c1.d1.r1.b1');
  RAISE EXCEPTION 'a second current row for the same path should be rejected';
EXCEPTION WHEN unique_violation THEN NULL;
END $$;

UPDATE units SET valid_to = now() + interval '1 second' WHERE path = 'ds.c1.d1.r1.b1';
INSERT INTO units (hierarchy_id, depth, code, name, path, valid_from)
VALUES ('00000000-0000-0000-0000-000000000010', 4, 'B1', 'Beat One (redrawn)', 'ds.c1.d1.r1.b1',
        now() + interval '1 second');

-- Layers: one published version at a time.
INSERT INTO layers (id, workspace_id, slug, name, role)
VALUES ('00000000-0000-0000-0000-000000000020', '00000000-0000-0000-0000-000000000001',
        'forest', 'Forest', 'forest');

INSERT INTO layer_versions (id, layer_id, version, status, published_at)
VALUES ('00000000-0000-0000-0000-000000000021', '00000000-0000-0000-0000-000000000020', 1, 'published', now());

DO $$
BEGIN
  INSERT INTO layer_versions (layer_id, version, status, published_at)
  VALUES ('00000000-0000-0000-0000-000000000020', 2, 'published', now());
  RAISE EXCEPTION 'a second published version should be rejected';
EXCEPTION WHEN unique_violation THEN NULL;
END $$;

-- A hierarchy layer must name its hierarchy.
DO $$
BEGIN
  INSERT INTO layers (workspace_id, slug, name, role)
  VALUES ('00000000-0000-0000-0000-000000000001', 'beats', 'Beats', 'hierarchy');
  RAISE EXCEPTION 'a hierarchy layer without a hierarchy should be rejected';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

INSERT INTO layer_features (layer_version_id, code, name, geom)
VALUES ('00000000-0000-0000-0000-000000000021', 'F1', 'Demo Reserve',
        ST_Multi(ST_MakeEnvelope(77.02, 11.02, 77.08, 11.08, 4326)));

-- Detections: a missing partition fails loudly.
DO $$
BEGIN
  INSERT INTO detections (source, product, observed_at, geom, source_key)
  VALUES ('firms', 'VIIRS_SNPP_NRT', '2031-03-15 08:30Z', ST_Point(77.05, 11.05, 4326), 'k1');
  RAISE EXCEPTION 'an insert with no partition should fail';
EXCEPTION WHEN check_violation OR undefined_table OR invalid_parameter_value THEN NULL;
END $$;

-- Creating a partition twice is harmless, and partitions follow UTC months.
DO $$
BEGIN
  ASSERT ensure_detection_partition('2031-03-01') = 'detections_2031_03';
  ASSERT ensure_detection_partition('2031-03-31') = 'detections_2031_03';
  PERFORM ensure_detection_partition('2031-04-01');
END $$;

INSERT INTO detections (source, product, observed_at, geom, source_key)
VALUES ('firms', 'VIIRS_SNPP_NRT', '2031-03-15 08:30Z', ST_Point(77.05, 11.05, 4326), 'k1'),
       ('firms', 'VIIRS_SNPP_NRT', '2031-03-31 23:30Z', ST_Point(77.15, 11.05, 4326), 'k2');

DO $$
BEGIN
  ASSERT (SELECT tableoid::regclass::text FROM detections WHERE source_key = 'k2') = 'detections_2031_03',
    '23:30 UTC on 31 March belongs to the March partition';
END $$;

-- Replaying the same record adds nothing.
INSERT INTO detections (source, product, observed_at, geom, source_key)
VALUES ('firms', 'VIIRS_SNPP_NRT', '2031-03-15 08:30Z', ST_Point(77.05, 11.05, 4326), 'k1')
ON CONFLICT (source, source_key, observed_at) DO NOTHING;

DO $$
BEGIN
  ASSERT (SELECT count(*) FROM detections) = 2, 'a replayed record should not be duplicated';
END $$;

-- Matches: permission filtering by hierarchy path.
INSERT INTO detection_matches (workspace_id, detection_id, observed_at, forest_class, forest_distance_m, unit_paths)
SELECT '00000000-0000-0000-0000-000000000001', id, observed_at,
       CASE source_key WHEN 'k1' THEN 'inside' ELSE 'outside' END,
       CASE source_key WHEN 'k1' THEN 0 ELSE 7000 END,
       CASE source_key WHEN 'k1' THEN ARRAY['ds.c1.d1.r1.b1']::ltree[] ELSE ARRAY['ds.c1.d1.r1.b2']::ltree[] END
FROM detections;

DO $$
BEGIN
  ASSERT (SELECT count(*) FROM detection_matches WHERE unit_paths <@ 'ds.c1.d1'::ltree) = 2,
    'a division officer should see both beats';
  ASSERT (SELECT count(*) FROM detection_matches WHERE unit_paths <@ 'ds.c1.d1.r1.b2'::ltree) = 1,
    'a beat officer should see only their beat';
END $$;

-- Deleting a detection removes its matches.
DELETE FROM detections WHERE source_key = 'k2';
DO $$
BEGIN
  ASSERT (SELECT count(*) FROM detection_matches) = 1, 'matches should follow their detection';
END $$;

-- The audit log accepts new rows and refuses changes.
INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id)
VALUES ('00000000-0000-0000-0000-000000000001', 'user', 'demo-subject', 'layer.publish', 'layer_version',
        '00000000-0000-0000-0000-000000000021');

DO $$
BEGIN
  UPDATE audit_log SET action = 'layer.delete';
  RAISE EXCEPTION 'an audit row should not be editable';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

DO $$
BEGIN
  DELETE FROM audit_log;
  RAISE EXCEPTION 'an audit row should not be deletable';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

-- The job queue is in place.
DO $$
BEGIN
  ASSERT to_regclass('river_job') IS NOT NULL, 'river_job should exist';
END $$;

ROLLBACK;
\echo 'Schema tests passed.'
