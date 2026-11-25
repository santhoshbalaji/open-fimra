-- Reference check for the synthetic demo state. `make seed-test` runs it after
-- seed/demo-state/load.sql and seed/demo-state/reference.sql, inside one
-- transaction that is rolled back.
--
-- It does two things:
--   1. checks that the demo data contains the awkward cases it is meant to;
--   2. classifies each reference detection with the plain rules below and
--      compares the result with the expected one.
--
-- The rules here are the reference meaning of each result, written as simply as
-- possible. The real matching code (build slice 6) must give the same answers.
--
--   not matched        more than the workspace margin outside its boundary
--   fixed_heat_source  inside a published exclusion feature
--   inside             inside a published forest feature
--   near_edge          within the workspace's near-edge distance of a forest feature
--   outside            anything else
--
-- Triage applies to inside and near_edge detections:
--   no_unit            no current beat covers the point
--   overlapping_units  the point is in the interior of two or more beats
--   shared_boundary    two or more beats cover the point, but only on their edges

\set ON_ERROR_STOP on

-- 1. The demo data contains its deliberate defects.
DO $$
DECLARE
  ws uuid := (SELECT id FROM workspaces WHERE slug = 'demo');
BEGIN
  ASSERT (SELECT count(*) FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
          WHERE h.workspace_id = ws AND u.depth = 4) = 24, 'the demo state has 24 beats';

  ASSERT (SELECT count(*) FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
          WHERE h.workspace_id = ws AND u.depth > 0
            AND NOT EXISTS (SELECT 1 FROM units p WHERE p.hierarchy_id = u.hierarchy_id
                            AND p.path = subpath(u.path, 0, nlevel(u.path) - 1))) = 0,
    'every unit below the state has a parent';

  ASSERT (SELECT count(*) FROM units a JOIN units b ON a.hierarchy_id = b.hierarchy_id AND a.id < b.id
          WHERE a.depth = 4 AND b.depth = 4 AND ST_Overlaps(a.geom, b.geom)) = 1,
    'exactly one pair of beats overlaps';

  ASSERT (SELECT ST_NumInteriorRings(ST_GeometryN(geom, 1)) FROM layer_features WHERE code = 'F02') = 1,
    'forest F02 has a hole';

  ASSERT (SELECT count(*) FROM layer_features WHERE code IS NULL) = 1,
    'one forest feature has no code';

  ASSERT EXISTS (SELECT 1 FROM layer_features f JOIN workspaces w ON w.id = ws
                 WHERE f.code = 'F03' AND NOT ST_Within(f.geom, w.boundary)),
    'forest F03 crosses the state boundary';
END $$;

-- 2. Every reference detection gets its expected result.
CREATE TEMP TABLE reference_results ON COMMIT DROP AS
WITH ws AS (
  SELECT id, boundary,
         (settings ->> 'nearEdgeMetres')::float8 AS near_m,
         (settings ->> 'areaMarginMetres')::float8 AS margin_m
  FROM workspaces WHERE slug = 'demo'
),
published AS (
  SELECT f.*, l.role
  FROM layer_features f
  JOIN layer_versions v ON v.id = f.layer_version_id AND v.status = 'published'
  JOIN layers l ON l.id = v.layer_id
  JOIN ws ON ws.id = l.workspace_id
),
beats AS (
  SELECT u.path, u.geom
  FROM units u
  JOIN hierarchies h ON h.id = u.hierarchy_id
  JOIN ws ON ws.id = h.workspace_id
  WHERE u.depth = 4 AND u.valid_to IS NULL
),
classified AS (
  SELECT r.*,
    CASE
      WHEN NOT ST_DWithin(ws.boundary::geography, r.geom::geography, ws.margin_m) THEN NULL
      WHEN EXISTS (SELECT 1 FROM published p WHERE p.role = 'exclusion' AND ST_Covers(p.geom, r.geom)) THEN 'fixed_heat_source'
      WHEN EXISTS (SELECT 1 FROM published p WHERE p.role = 'forest' AND ST_Covers(p.geom, r.geom)) THEN 'inside'
      WHEN EXISTS (SELECT 1 FROM published p WHERE p.role = 'forest'
                   AND ST_DWithin(p.geom::geography, r.geom::geography, ws.near_m)) THEN 'near_edge'
      ELSE 'outside'
    END AS forest_class,
    CASE WHEN ST_DWithin(ws.boundary::geography, r.geom::geography, ws.margin_m) THEN
      COALESCE((SELECT array_agg(b.path ORDER BY b.path) FROM beats b WHERE ST_Covers(b.geom, r.geom)), '{}')
    ELSE '{}' END AS beats,
    (SELECT count(*) FROM beats b WHERE ST_Contains(b.geom, r.geom)) AS interior_beats,
    (SELECT p.code FROM published p WHERE p.role = 'forest' AND ST_Covers(p.geom, r.geom) ORDER BY p.id LIMIT 1) AS forest_code
  FROM reference_detections r CROSS JOIN ws
)
SELECT id,
  expected_forest_class, forest_class,
  (SELECT COALESCE(array_agg(x ORDER BY x), '{}') FROM unnest(expected_beats) x) AS expected_beats, beats,
  expected_queue, expected_triage,
  CASE
    WHEN forest_class NOT IN ('inside', 'near_edge') OR forest_class IS NULL THEN NULL
    WHEN cardinality(beats) = 0 THEN 'no_unit'
    WHEN interior_beats >= 2 THEN 'overlapping_units'
    WHEN cardinality(beats) >= 2 THEN 'shared_boundary'
  END AS triage,
  expected_forest_code, forest_code
FROM classified;

DO $$
DECLARE
  r record;
  bad int := 0;
  queue text;
BEGIN
  FOR r IN SELECT * FROM reference_results ORDER BY id LOOP
    queue := CASE
      WHEN r.triage IS NOT NULL THEN 'triage'
      WHEN r.forest_class = 'inside' THEN 'unit'
      WHEN r.forest_class = 'near_edge' THEN 'edge_review' END;
    IF r.forest_class IS DISTINCT FROM r.expected_forest_class
       OR r.beats IS DISTINCT FROM r.expected_beats
       OR queue IS DISTINCT FROM r.expected_queue
       OR r.triage IS DISTINCT FROM r.expected_triage
       OR r.forest_code IS DISTINCT FROM r.expected_forest_code THEN
      bad := bad + 1;
      RAISE WARNING '%: class % (expected %), beats % (expected %), triage % (expected %), forest % (expected %), queue % (expected %)',
        r.id, r.forest_class, r.expected_forest_class, r.beats, r.expected_beats,
        r.triage, r.expected_triage, r.forest_code, r.expected_forest_code, queue, r.expected_queue;
    END IF;
  END LOOP;
  IF bad > 0 THEN
    RAISE EXCEPTION '% of % reference detections did not get their expected result',
      bad, (SELECT count(*) FROM reference_results);
  END IF;
  RAISE NOTICE 'All % reference detections got their expected result.', (SELECT count(*) FROM reference_results);
END $$;
