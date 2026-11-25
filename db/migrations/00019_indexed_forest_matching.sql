-- Match a detection to forest through the spatial index. The earlier version
-- measured the geodesic distance to every forest feature, twice, for each
-- detection. Results are unchanged.

-- +goose Up
-- +goose StatementBegin
-- A box in degrees that holds every point within the given geodesic
-- distance of a point, for an index search before an exact distance test.
-- A degree of latitude is at least 110,574 metres and a degree of longitude
-- at least 111,319 metres times the cosine of the latitude, so the divisors
-- are rounded down to stay on the safe side.
CREATE FUNCTION metres_box(point geometry, metres float8)
RETURNS geometry LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT ST_Expand(point,
        LEAST(metres / (111000 * cos(radians(LEAST(abs(ST_Y(point)) + metres / 110000, 89.9)))), 360),
        LEAST(metres / 110000, 180));
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION classify_detection(workspace uuid, detection bigint, observed timestamptz, point geometry,
                                   draft_version uuid DEFAULT NULL)
RETURNS SETOF detection_matches LANGUAGE sql STABLE AS $$
WITH draft AS (
    SELECT v.id, l.id AS layer_id, l.hierarchy_id, (v.import_settings->>'depth')::smallint AS depth
    FROM layer_versions v JOIN layers l ON l.id = v.layer_id
    WHERE v.id = draft_version AND l.workspace_id = workspace AND v.status = 'draft'
),
selected_layers AS (
    SELECT l.role, v.id
    FROM layers l JOIN layer_versions v ON v.layer_id = l.id
    WHERE l.workspace_id = workspace AND l.role IN ('forest', 'exclusion')
      AND (v.id IN (SELECT id FROM draft) OR
           (v.status = 'published' AND l.id NOT IN (SELECT layer_id FROM draft)))
),
candidate AS (
    SELECT COALESCE((w.settings->>'nearEdgeMetres')::float8, 0) AS near_m, point AS geom
    FROM workspaces w
    WHERE w.id = workspace AND detection_in_workspace(w, point)
),
classified AS (
    SELECT c.*,
        CASE
          WHEN EXISTS (
            SELECT 1 FROM selected_layers p JOIN layer_features f ON f.layer_version_id = p.id
            WHERE p.role = 'exclusion' AND ST_Covers(f.geom, c.geom)
          ) THEN 'fixed_heat_source'
          WHEN forest.covers THEN 'inside'
          WHEN EXISTS (
            SELECT 1 FROM selected_layers p JOIN layer_features f ON f.layer_version_id = p.id
            WHERE p.role = 'forest' AND f.geom && metres_box(c.geom, c.near_m)
              AND ST_DWithin(f.geom::geography, c.geom::geography, c.near_m)
          ) THEN 'near_edge'
          ELSE 'outside'
        END AS forest_class,
        CASE WHEN forest.covers THEN forest.id END AS forest_feature_id,
        forest.distance_m
    FROM candidate c
    -- The covering feature with the lowest ID, or else the nearest feature
    -- with ties broken by ID. The index finds a few candidates by flat
    -- distance; the nearest of them bounds the box searched for the exact
    -- geodesic answer, so the result is the same as measuring every feature.
    LEFT JOIN LATERAL (
        SELECT f.id, true AS covers, 0::float8 AS distance_m
        FROM selected_layers p JOIN layer_features f ON f.layer_version_id = p.id
        WHERE p.role = 'forest' AND NOT ST_IsEmpty(f.geom) AND ST_Covers(f.geom, c.geom)
        ORDER BY f.id
        LIMIT 1
    ) covering ON true
    LEFT JOIN LATERAL (
        SELECT min(ST_Distance(n.geom::geography, c.geom::geography)) AS bound_m
        FROM (
            SELECT f.geom
            FROM selected_layers p JOIN layer_features f ON f.layer_version_id = p.id
            WHERE covering.id IS NULL AND p.role = 'forest' AND NOT ST_IsEmpty(f.geom)
            ORDER BY f.geom <-> c.geom
            LIMIT 8
        ) n
    ) nearby ON true
    LEFT JOIN LATERAL (
        SELECT f.id, false AS covers, ST_Distance(f.geom::geography, c.geom::geography) AS distance_m
        FROM selected_layers p JOIN layer_features f ON f.layer_version_id = p.id
        WHERE covering.id IS NULL AND p.role = 'forest' AND NOT ST_IsEmpty(f.geom)
          AND f.geom && metres_box(c.geom, nearby.bound_m)
        ORDER BY distance_m, f.id
        LIMIT 1
    ) nearest ON true
    CROSS JOIN LATERAL (
        SELECT COALESCE(covering.id, nearest.id) AS id, covering.id IS NOT NULL AS covers,
               COALESCE(covering.distance_m, nearest.distance_m) AS distance_m
    ) forest
)
SELECT workspace, detection, observed, c.forest_class,
    c.distance_m::real, c.forest_feature_id, COALESCE(units.paths, '{}'), units.snapshot,
    '{}'::jsonb,
    CASE WHEN c.forest_class IN ('inside', 'near_edge') THEN
        CASE WHEN units.beats = 0 THEN 'no_unit'
             WHEN units.interior_beats >= 2 THEN 'overlapping_units'
             WHEN units.beats >= 2 THEN 'shared_boundary' END
    END,
    ARRAY(
        SELECT id FROM selected_layers
        UNION
        SELECT u.layer_version_id FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
        WHERE h.workspace_id = workspace AND u.valid_to IS NULL AND u.layer_version_id IS NOT NULL
          AND NOT EXISTS (SELECT 1 FROM draft d WHERE d.hierarchy_id = u.hierarchy_id AND d.depth = u.depth)
        UNION
        SELECT id FROM draft WHERE hierarchy_id IS NOT NULL
        ORDER BY 1
    ), NULL::timestamptz, COALESCE(units.paths_by_hierarchy, '{}')
FROM classified c
CROSS JOIN LATERAL (
    WITH covering AS (
        SELECT u.hierarchy_id, u.path, u.depth, u.code, u.name, l.slug,
               ST_Contains(u.geom, c.geom) AS interior
        FROM hierarchies h
        JOIN units u ON u.hierarchy_id = h.id
        JOIN hierarchy_levels l ON l.hierarchy_id = u.hierarchy_id AND l.depth = u.depth
        WHERE h.workspace_id = workspace AND u.valid_to IS NULL AND ST_Covers(u.geom, c.geom)
          AND NOT EXISTS (SELECT 1 FROM draft d WHERE d.hierarchy_id = u.hierarchy_id AND d.depth = u.depth)
        UNION ALL
        SELECT d.hierarchy_id, u.path, u.depth, u.code, u.name, l.slug, ST_Contains(u.geom, c.geom)
        FROM draft d JOIN draft_units u ON u.layer_version_id = d.id
        JOIN hierarchy_levels l ON l.hierarchy_id = d.hierarchy_id AND l.depth = u.depth
        WHERE u.path IS NOT NULL AND ST_Covers(u.geom, c.geom)
    ),
    -- Choose the first deepest path as the representative line. Prefer
    -- its ancestors when there is more than one unit at a level.
    representative AS (
        SELECT DISTINCT ON (hierarchy_id) hierarchy_id, path
        FROM covering ORDER BY hierarchy_id, depth DESC, path
    ),
    levels AS (
        SELECT DISTINCT ON (u.hierarchy_id, u.slug) u.hierarchy_id, u.slug, u.code, u.name
        FROM covering u JOIN representative r USING (hierarchy_id)
        ORDER BY u.hierarchy_id, u.slug, (u.path @> r.path) DESC, u.path
    )
    SELECT
        (SELECT array_agg(DISTINCT path ORDER BY path) FROM covering) AS paths,
        (SELECT count(*) FROM covering WHERE slug = 'beat') AS beats,
        (SELECT count(*) FROM covering WHERE slug = 'beat' AND interior) AS interior_beats,
        (SELECT jsonb_object_agg(hierarchy_id, paths) FROM (
            SELECT hierarchy_id, array_agg(path ORDER BY path) AS paths
            FROM covering GROUP BY hierarchy_id
        ) paths_per_hierarchy) AS paths_by_hierarchy,
        COALESCE((SELECT jsonb_object_agg(h.slug, COALESCE((
            SELECT jsonb_object_agg(l.slug, jsonb_build_object('code', l.code, 'name', l.name))
            FROM levels l WHERE l.hierarchy_id = h.id
        ), '{}')) FROM hierarchies h WHERE h.workspace_id = workspace), '{}') AS snapshot
) units;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION classify_detection(workspace uuid, detection bigint, observed timestamptz, point geometry,
                                   draft_version uuid DEFAULT NULL)
RETURNS SETOF detection_matches LANGUAGE sql STABLE AS $$
WITH draft AS (
    SELECT v.id, l.id AS layer_id, l.hierarchy_id, (v.import_settings->>'depth')::smallint AS depth
    FROM layer_versions v JOIN layers l ON l.id = v.layer_id
    WHERE v.id = draft_version AND l.workspace_id = workspace AND v.status = 'draft'
),
selected_layers AS (
    SELECT l.role, v.id
    FROM layers l JOIN layer_versions v ON v.layer_id = l.id
    WHERE l.workspace_id = workspace AND l.role IN ('forest', 'exclusion')
      AND (v.id IN (SELECT id FROM draft) OR
           (v.status = 'published' AND l.id NOT IN (SELECT layer_id FROM draft)))
),
candidate AS (
    SELECT COALESCE((w.settings->>'nearEdgeMetres')::float8, 0) AS near_m, point AS geom
    FROM workspaces w
    WHERE w.id = workspace AND detection_in_workspace(w, point)
),
classified AS (
    SELECT c.*,
        CASE
          WHEN EXISTS (
            SELECT 1 FROM selected_layers p JOIN layer_features f ON f.layer_version_id = p.id
            WHERE p.role = 'exclusion' AND ST_Covers(f.geom, c.geom)
          ) THEN 'fixed_heat_source'
          WHEN forest.covers THEN 'inside'
          WHEN EXISTS (
            SELECT 1 FROM selected_layers p JOIN layer_features f ON f.layer_version_id = p.id
            WHERE p.role = 'forest'
              AND ST_DWithin(f.geom::geography, c.geom::geography, c.near_m)
          ) THEN 'near_edge'
          ELSE 'outside'
        END AS forest_class,
        CASE WHEN forest.covers THEN forest.id END AS forest_feature_id,
        forest.distance_m
    FROM candidate c
    LEFT JOIN LATERAL (
        SELECT f.id, ST_Covers(f.geom, c.geom) AS covers,
               CASE WHEN ST_Covers(f.geom, c.geom) THEN 0
                    ELSE ST_Distance(f.geom::geography, c.geom::geography) END AS distance_m
        FROM selected_layers p JOIN layer_features f ON f.layer_version_id = p.id
        WHERE p.role = 'forest' AND NOT ST_IsEmpty(f.geom)
        ORDER BY covers DESC, distance_m, f.id
        LIMIT 1
    ) forest ON true
)
SELECT workspace, detection, observed, c.forest_class,
    c.distance_m::real, c.forest_feature_id, COALESCE(units.paths, '{}'), units.snapshot,
    '{}'::jsonb,
    CASE WHEN c.forest_class IN ('inside', 'near_edge') THEN
        CASE WHEN units.beats = 0 THEN 'no_unit'
             WHEN units.interior_beats >= 2 THEN 'overlapping_units'
             WHEN units.beats >= 2 THEN 'shared_boundary' END
    END,
    ARRAY(
        SELECT id FROM selected_layers
        UNION
        SELECT u.layer_version_id FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
        WHERE h.workspace_id = workspace AND u.valid_to IS NULL AND u.layer_version_id IS NOT NULL
          AND NOT EXISTS (SELECT 1 FROM draft d WHERE d.hierarchy_id = u.hierarchy_id AND d.depth = u.depth)
        UNION
        SELECT id FROM draft WHERE hierarchy_id IS NOT NULL
        ORDER BY 1
    ), NULL::timestamptz, COALESCE(units.paths_by_hierarchy, '{}')
FROM classified c
CROSS JOIN LATERAL (
    WITH covering AS (
        SELECT u.hierarchy_id, u.path, u.depth, u.code, u.name, l.slug,
               ST_Contains(u.geom, c.geom) AS interior
        FROM hierarchies h
        JOIN units u ON u.hierarchy_id = h.id
        JOIN hierarchy_levels l ON l.hierarchy_id = u.hierarchy_id AND l.depth = u.depth
        WHERE h.workspace_id = workspace AND u.valid_to IS NULL AND ST_Covers(u.geom, c.geom)
          AND NOT EXISTS (SELECT 1 FROM draft d WHERE d.hierarchy_id = u.hierarchy_id AND d.depth = u.depth)
        UNION ALL
        SELECT d.hierarchy_id, u.path, u.depth, u.code, u.name, l.slug, ST_Contains(u.geom, c.geom)
        FROM draft d JOIN draft_units u ON u.layer_version_id = d.id
        JOIN hierarchy_levels l ON l.hierarchy_id = d.hierarchy_id AND l.depth = u.depth
        WHERE u.path IS NOT NULL AND ST_Covers(u.geom, c.geom)
    ),
    -- Choose the first deepest path as the representative line. Prefer
    -- its ancestors when there is more than one unit at a level.
    representative AS (
        SELECT DISTINCT ON (hierarchy_id) hierarchy_id, path
        FROM covering ORDER BY hierarchy_id, depth DESC, path
    ),
    levels AS (
        SELECT DISTINCT ON (u.hierarchy_id, u.slug) u.hierarchy_id, u.slug, u.code, u.name
        FROM covering u JOIN representative r USING (hierarchy_id)
        ORDER BY u.hierarchy_id, u.slug, (u.path @> r.path) DESC, u.path
    )
    SELECT
        (SELECT array_agg(DISTINCT path ORDER BY path) FROM covering) AS paths,
        (SELECT count(*) FROM covering WHERE slug = 'beat') AS beats,
        (SELECT count(*) FROM covering WHERE slug = 'beat' AND interior) AS interior_beats,
        (SELECT jsonb_object_agg(hierarchy_id, paths) FROM (
            SELECT hierarchy_id, array_agg(path ORDER BY path) AS paths
            FROM covering GROUP BY hierarchy_id
        ) paths_per_hierarchy) AS paths_by_hierarchy,
        COALESCE((SELECT jsonb_object_agg(h.slug, COALESCE((
            SELECT jsonb_object_agg(l.slug, jsonb_build_object('code', l.code, 'name', l.name))
            FROM levels l WHERE l.hierarchy_id = h.id
        ), '{}')) FROM hierarchies h WHERE h.workspace_id = workspace), '{}') AS snapshot
) units;
$$;
-- +goose StatementEnd
DROP FUNCTION metres_box(geometry, float8);
