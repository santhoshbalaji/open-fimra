-- Forest matching through the spatial index gives the exact geodesic answer.
-- All shapes are made up, far from any real forest.

\set ON_ERROR_STOP on
BEGIN;

-- The search box holds every point at the given geodesic distance, in every
-- direction, from the equator to high latitudes.
DO $$
DECLARE
    missed integer;
BEGIN
    SELECT count(*) INTO missed
    FROM unnest(ARRAY[0, 12.5, 23, 45, 60, 75, -40]) AS lat,
         unnest(ARRAY[1, 500, 5000, 80000]) AS metres,
         generate_series(0, 345, 15) AS azimuth
    WHERE NOT ST_Covers(
        metres_box(ST_SetSRID(ST_MakePoint(80, lat), 4326), metres),
        ST_Project(ST_SetSRID(ST_MakePoint(80, lat), 4326)::geography, metres * 0.999999, radians(azimuth))::geometry);
    IF missed > 0 THEN
        RAISE EXCEPTION '% projected points fell outside their search box', missed;
    END IF;
END $$;

-- At 60 degrees north a degree of longitude is half a degree of latitude.
-- Nine small squares lie about 0.007 degrees north of the point (about 780
-- metres) and one lies 0.010 degrees east (about 560 metres). In flat degrees
-- the east square comes tenth, outside the index's first candidates, but it
-- is the nearest on the ground.
INSERT INTO workspaces (id, slug, name, boundary, settings)
VALUES ('00000000-0000-0000-0000-000000000501', 'match-test', 'Match Test',
        ST_Multi(ST_MakeEnvelope(10.9, 59.9, 11.1, 60.1, 4326)), '{"nearEdgeMetres": 600}');
INSERT INTO layers (id, workspace_id, slug, name, role)
VALUES ('00000000-0000-0000-0000-000000000502', '00000000-0000-0000-0000-000000000501', 'forest', 'Forest', 'forest');
INSERT INTO layer_versions (id, layer_id, version, status, published_at)
VALUES ('00000000-0000-0000-0000-000000000503', '00000000-0000-0000-0000-000000000502', 1, 'published', now());

INSERT INTO layer_features (layer_version_id, code, geom)
SELECT '00000000-0000-0000-0000-000000000503', 'north-' || i,
       ST_MakeEnvelope(11 + i * 0.0002, 60.007 + i * 0.00002, 11 + i * 0.0002 + 0.0001, 60.0072 + i * 0.00002, 4326)
FROM generate_series(0, 8) AS i;
INSERT INTO layer_features (layer_version_id, code, geom)
VALUES ('00000000-0000-0000-0000-000000000503', 'east', ST_MakeEnvelope(11.01, 59.9999, 11.0102, 60.0001, 4326));

DO $$
DECLARE
    point geometry := ST_SetSRID(ST_MakePoint(11, 60), 4326);
    expected float8;
    got record;
BEGIN
    SELECT min(ST_Distance(f.geom::geography, point::geography)) INTO expected
    FROM layer_features f WHERE f.layer_version_id = '00000000-0000-0000-0000-000000000503';
    SELECT * INTO got FROM classify_detection('00000000-0000-0000-0000-000000000501', 1, now(), point);
    IF got.forest_class <> 'near_edge' OR abs(got.forest_distance_m - expected) > 0.01 OR expected > 600 THEN
        RAISE EXCEPTION 'expected near edge at % metres, got % at %', expected, got.forest_class, got.forest_distance_m;
    END IF;

    -- Inside a square: the covering feature, at zero distance.
    SELECT * INTO got FROM classify_detection('00000000-0000-0000-0000-000000000501', 2, now(),
        ST_SetSRID(ST_MakePoint(11.0101, 60), 4326));
    IF got.forest_class <> 'inside' OR got.forest_distance_m <> 0 OR got.forest_feature_id IS NULL THEN
        RAISE EXCEPTION 'expected inside, got % at %', got.forest_class, got.forest_distance_m;
    END IF;

    -- Far from every square but inside the workspace.
    SELECT * INTO got FROM classify_detection('00000000-0000-0000-0000-000000000501', 3, now(),
        ST_SetSRID(ST_MakePoint(10.95, 59.95), 4326));
    IF got.forest_class <> 'outside' OR got.forest_distance_m < 600 OR got.forest_feature_id IS NOT NULL THEN
        RAISE EXCEPTION 'expected outside, got % at %', got.forest_class, got.forest_distance_m;
    END IF;
END $$;

ROLLBACK;
