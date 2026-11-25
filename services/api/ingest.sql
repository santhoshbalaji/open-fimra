WITH input AS (
    SELECT *
    FROM jsonb_to_recordset($1::jsonb) AS r(
        source_key text, satellite text, instrument text, observed_at timestamptz,
        longitude float8, latitude float8, scan_m real, track_m real, brightness_k real,
        frp_mw real, confidence text, day_night text, raw jsonb
    )
),
inserted AS (
    INSERT INTO detections (source, product, source_key, satellite, instrument, observed_at,
                            geom, scan_m, track_m, brightness_k, frp_mw, confidence, day_night, raw)
    SELECT $2, $3, source_key, satellite, instrument, observed_at,
           ST_SetSRID(ST_Point(longitude, latitude), 4326), scan_m, track_m,
           brightness_k, frp_mw, NULLIF(confidence, ''), NULLIF(day_night, ''), COALESCE(raw, '{}')
    FROM input
    ON CONFLICT DO NOTHING
    RETURNING id, observed_at, geom
),
published AS (
    SELECT l.workspace_id, l.role, v.id
    FROM layers l JOIN layer_versions v ON v.layer_id = l.id
    WHERE v.status = 'published' AND l.role IN ('forest', 'exclusion', 'hierarchy')
),
candidates AS (
    SELECT d.*, w.id AS workspace_id, COALESCE((w.settings->>'nearEdgeMetres')::float8, 0) AS near_m
    FROM inserted d
    JOIN workspaces w ON ST_DWithin(w.boundary::geography, d.geom::geography,
                                     COALESCE((w.settings->>'areaMarginMetres')::float8, 0))
),
classified AS (
    SELECT c.*,
        CASE
          WHEN EXISTS (
            SELECT 1 FROM published p JOIN layer_features f ON f.layer_version_id = p.id
            WHERE p.workspace_id = c.workspace_id AND p.role = 'exclusion' AND ST_Covers(f.geom, c.geom)
          ) THEN 'fixed_heat_source'
          WHEN forest.covers THEN 'inside'
          WHEN EXISTS (
            SELECT 1 FROM published p JOIN layer_features f ON f.layer_version_id = p.id
            WHERE p.workspace_id = c.workspace_id AND p.role = 'forest'
              AND ST_DWithin(f.geom::geography, c.geom::geography, c.near_m)
          ) THEN 'near_edge'
          ELSE 'outside'
        END AS forest_class,
        CASE WHEN forest.covers THEN forest.id END AS forest_feature_id,
        forest.distance_m
    FROM candidates c
    LEFT JOIN LATERAL (
        SELECT f.id, ST_Covers(f.geom, c.geom) AS covers,
               CASE WHEN ST_Covers(f.geom, c.geom) THEN 0
                    ELSE ST_Distance(f.geom::geography, c.geom::geography) END AS distance_m
        FROM published p JOIN layer_features f ON f.layer_version_id = p.id
        WHERE p.workspace_id = c.workspace_id AND p.role = 'forest' AND NOT ST_IsEmpty(f.geom)
        ORDER BY covers DESC, distance_m, f.id
        LIMIT 1
    ) forest ON true
),
matched AS (
    INSERT INTO detection_matches (workspace_id, detection_id, observed_at, forest_class,
                                   forest_feature_id, forest_distance_m, unit_paths, hierarchy,
                                   hierarchy_unit_paths, triage_reason, layer_version_ids)
    SELECT c.workspace_id, c.id, c.observed_at, c.forest_class,
        c.forest_feature_id, c.distance_m, COALESCE(units.paths, '{}'), units.snapshot,
        COALESCE(units.paths_by_hierarchy, '{}'),
        CASE WHEN c.forest_class IN ('inside', 'near_edge') THEN
            CASE WHEN units.beats = 0 THEN 'no_unit'
                 WHEN units.interior_beats >= 2 THEN 'overlapping_units'
                 WHEN units.beats >= 2 THEN 'shared_boundary' END
        END,
        ARRAY(SELECT p.id FROM published p WHERE p.workspace_id = c.workspace_id ORDER BY p.id)
    FROM classified c
    CROSS JOIN LATERAL (
        WITH covering AS (
            SELECT u.hierarchy_id, u.path, u.depth, u.code, u.name, l.slug,
                   ST_Contains(u.geom, c.geom) AS interior
            FROM hierarchies h
            JOIN units u ON u.hierarchy_id = h.id
            JOIN hierarchy_levels l ON l.hierarchy_id = u.hierarchy_id AND l.depth = u.depth
            WHERE h.workspace_id = c.workspace_id AND u.valid_to IS NULL AND ST_Covers(u.geom, c.geom)
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
            ), '{}')) FROM hierarchies h WHERE h.workspace_id = c.workspace_id), '{}') AS snapshot
    ) units
    ON CONFLICT DO NOTHING
    RETURNING 1
)
SELECT (SELECT count(*) FROM inserted)::integer, (SELECT count(*) FROM matched)::integer;
