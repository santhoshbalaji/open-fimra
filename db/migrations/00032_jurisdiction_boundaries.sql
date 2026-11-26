-- Level depth orders the hierarchy. Paths contain only the units a branch uses.
-- +goose Up
ALTER TABLE hierarchy_levels ADD COLUMN optional boolean NOT NULL DEFAULT false;
UPDATE hierarchy_levels level SET optional = true
FROM hierarchy_levels parent JOIN hierarchy_levels child ON child.hierarchy_id = parent.hierarchy_id
WHERE level.hierarchy_id = parent.hierarchy_id AND parent.depth = level.depth - 1 AND child.depth = level.depth + 1
  AND (ROW(parent.slug, level.slug, child.slug) = ROW('division', 'sub_division', 'range')
       OR ROW(parent.slug, level.slug, child.slug) = ROW('range', 'section', 'beat'));
ALTER TABLE units DROP CONSTRAINT units_check,
    ADD CONSTRAINT units_path_length_check CHECK (nlevel(path) BETWEEN greatest(1, depth - 1) AND depth + 1);
ALTER TABLE draft_units DROP CONSTRAINT draft_units_check,
    DROP CONSTRAINT draft_units_check1,
    ADD CONSTRAINT draft_units_path_length_check CHECK (nlevel(path) BETWEEN greatest(1, depth - 1) AND depth + 1),
    ADD CONSTRAINT draft_units_parent_path_check CHECK (
        parent_path IS NULL OR (nlevel(parent_path) BETWEEN greatest(1, depth - 2) AND depth
                               AND (path IS NULL OR parent_path = subpath(path, 0, -1)))
    );

-- +goose StatementBegin
CREATE FUNCTION hierarchy_parent_allowed(hierarchy uuid, child_depth smallint, parent_depth smallint)
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT parent_depth < child_depth AND NOT EXISTS (
        SELECT 1 FROM hierarchy_levels
        WHERE hierarchy_id = hierarchy AND depth > parent_depth AND depth < child_depth AND NOT optional
    );
$$;
-- +goose StatementEnd

ALTER TABLE layer_versions
    ADD COLUMN effective_from timestamptz CHECK (
        effective_from IS NULL OR (published_at IS NOT NULL AND effective_from <= published_at)
    ),
    ADD COLUMN overlay_kind text CHECK (overlay_kind IN (
        'protected_area', 'civil_district', 'civil_block', 'civil_village', 'community', 'neighbouring_state'
    )),
    ADD COLUMN overlay_from timestamptz,
    ADD CONSTRAINT layer_versions_overlay_period_check CHECK (
        (overlay_kind IS NULL) = (overlay_from IS NULL)
        AND (overlay_from IS NULL OR (published_at IS NOT NULL AND overlay_from >= published_at
                                     AND (retired_at IS NULL OR overlay_from <= retired_at)))
    );

-- The live match can change on an explicit replay. This snapshot records the
-- boundaries effective at observation time and follows the row into exports.
ALTER TABLE detection_matches ADD COLUMN beat_paths jsonb,
    ADD COLUMN boundary_snapshot jsonb;
ALTER TABLE alerts ADD COLUMN boundary_snapshot jsonb;
ALTER TABLE export_rows ADD COLUMN boundary_snapshot jsonb,
    ADD COLUMN dispatch jsonb;

DROP FUNCTION classify_detection(uuid, bigint, timestamptz, geometry, uuid);

-- +goose StatementBegin
CREATE FUNCTION classify_detection_at(workspace uuid, detection bigint, observed timestamptz, point geometry,
                                   draft_version uuid, effective_at timestamptz)
RETURNS SETOF detection_matches LANGUAGE sql STABLE AS $$
WITH draft AS (
    SELECT v.id, l.id AS layer_id, l.hierarchy_id, (v.import_settings->>'depth')::smallint AS depth
    FROM layer_versions v JOIN layers l ON l.id = v.layer_id
    WHERE v.id = draft_version AND l.workspace_id = workspace AND v.status = 'draft'
),
valid_units AS NOT MATERIALIZED (
    SELECT u.* FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
    WHERE h.workspace_id = workspace AND
          CASE WHEN effective_at IS NULL THEN u.valid_to IS NULL
               ELSE u.valid_from <= effective_at AND (u.valid_to IS NULL OR effective_at < u.valid_to) END
),
selected_layers AS (
    SELECT l.role, l.slug, l.name, v.id,
           CASE WHEN effective_at IS NULL OR v.overlay_from <= effective_at THEN v.overlay_kind END AS overlay_kind
    FROM layers l JOIN layer_versions v ON v.layer_id = l.id
    WHERE l.workspace_id = workspace AND (l.role IN ('forest', 'exclusion') OR
          (v.overlay_kind IS NOT NULL AND (effective_at IS NULL OR v.overlay_from <= effective_at)))
      AND (v.id IN (SELECT id FROM draft) OR (
          l.id NOT IN (SELECT layer_id FROM draft) AND
          CASE WHEN effective_at IS NULL THEN v.status = 'published'
               ELSE COALESCE(v.effective_from, v.published_at) <= effective_at
                    AND (v.retired_at IS NULL OR effective_at < v.retired_at) END
      ))
),
candidate AS (
    SELECT COALESCE((w.settings->>'nearEdgeMetres')::float8, 0) AS near_m, point AS geom
    FROM workspaces w
    WHERE w.id = workspace AND (
        CASE WHEN effective_at IS NOT NULL AND EXISTS (
            SELECT 1 FROM workspace_setup s JOIN units u ON u.hierarchy_id = s.hierarchy_id
            WHERE s.workspace_id = w.id AND u.depth = 0 AND u.geom IS NOT NULL
        ) THEN EXISTS (
            SELECT 1 FROM workspace_setup s JOIN valid_units root ON root.hierarchy_id = s.hierarchy_id
            WHERE s.workspace_id = w.id AND root.depth = 0 AND ST_DWithin(
                root.geom::geography, point::geography, COALESCE((w.settings->>'areaMarginMetres')::float8, 0))
        ) ELSE detection_in_workspace(w, point) END
    )
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
    triage.reason, versions.ids, NULL::timestamptz, COALESCE(units.paths_by_hierarchy, '{}'),
    units.beat_paths,
    jsonb_build_object('hierarchy', units.snapshot, 'layerVersionIds', versions.ids,
                       'overlays', overlays.matches, 'forestClass', c.forest_class,
                       'forestFeatureId', c.forest_feature_id, 'triageReason', triage.reason)
FROM classified c
CROSS JOIN LATERAL (
    WITH covering AS (
        SELECT u.hierarchy_id, u.path, u.depth, u.code, u.name, l.slug, l.optional,
               ST_Contains(u.geom, c.geom) AS interior
        FROM valid_units u
        JOIN hierarchy_levels l ON l.hierarchy_id = u.hierarchy_id AND l.depth = u.depth
        WHERE ST_Covers(u.geom, c.geom)
          AND NOT EXISTS (SELECT 1 FROM draft d WHERE d.hierarchy_id = u.hierarchy_id AND d.depth = u.depth)
        UNION ALL
        SELECT d.hierarchy_id, u.path, u.depth, u.code, u.name, l.slug, l.optional, ST_Contains(u.geom, c.geom)
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
        WHERE NOT u.optional OR u.path @> r.path
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
        COALESCE((SELECT jsonb_object_agg(hierarchy_id, paths) FROM (
            SELECT hierarchy_id, array_agg(path ORDER BY path) AS paths
            FROM covering WHERE slug = 'beat' GROUP BY hierarchy_id
        ) beats_per_hierarchy), '{}') AS beat_paths,
        COALESCE((SELECT jsonb_object_agg(h.slug, COALESCE((
            SELECT jsonb_object_agg(l.slug, jsonb_build_object('code', l.code, 'name', l.name))
            FROM levels l WHERE l.hierarchy_id = h.id
        ), '{}')) FROM hierarchies h WHERE h.workspace_id = workspace), '{}') AS snapshot
) units
CROSS JOIN LATERAL (
    SELECT CASE WHEN c.forest_class IN ('inside', 'near_edge') THEN
        CASE WHEN units.beats = 0 THEN 'no_unit'
             WHEN units.interior_beats >= 2 THEN 'overlapping_units'
             WHEN units.beats >= 2 THEN 'shared_boundary' END
    END AS reason
) triage
CROSS JOIN LATERAL (
    SELECT ARRAY(
        SELECT id FROM selected_layers
        UNION
        SELECT u.layer_version_id FROM valid_units u
        WHERE u.layer_version_id IS NOT NULL
          AND NOT EXISTS (SELECT 1 FROM draft d WHERE d.hierarchy_id = u.hierarchy_id AND d.depth = u.depth)
        UNION
        SELECT id FROM draft WHERE hierarchy_id IS NOT NULL
        ORDER BY 1
    ) AS ids
) versions
CROSS JOIN LATERAL (
    SELECT COALESCE(jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
        'kind', p.overlay_kind, 'layer', p.slug, 'versionId', p.id, 'featureId', f.id::text,
        'code', f.code, 'name', COALESCE(NULLIF(f.name, ''), NULLIF(f.code, ''), p.name),
        'zone', CASE WHEN p.overlay_kind = 'protected_area' AND lower(btrim(f.attributes->>'zone')) IN ('core', 'buffer')
                    THEN lower(btrim(f.attributes->>'zone')) END
    )) ORDER BY p.slug, f.id), '[]') AS matches
    FROM selected_layers p JOIN layer_features f ON f.layer_version_id = p.id
    WHERE p.overlay_kind IS NOT NULL AND ST_Covers(f.geom, c.geom)
) overlays;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION classify_detection(workspace uuid, detection bigint, observed timestamptz, point geometry,
                                   draft_version uuid DEFAULT NULL)
RETURNS SETOF detection_matches LANGUAGE sql STABLE AS $$
    SELECT * FROM classify_detection_at(workspace, detection, observed, point, draft_version,
        CASE WHEN draft_version IS NULL THEN observed END);
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION alert_route(m detection_matches)
RETURNS TABLE(queue text, hierarchy_id uuid, unit_path ltree)
LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN m.triage_reason IS NOT NULL OR beat.path IS NULL THEN 'triage'
                WHEN m.forest_class = 'near_edge' AND w.review_near_edge THEN 'edge_review'
                ELSE 'unit' END, beat.hierarchy_id, beat.path
    FROM workspaces w
    LEFT JOIN LATERAL (
        SELECT h.id AS hierarchy_id, p.path::ltree AS path
        FROM hierarchies h JOIN hierarchy_levels l ON l.hierarchy_id = h.id AND l.slug = 'beat'
        CROSS JOIN LATERAL jsonb_array_elements_text(m.beat_paths -> h.id::text) p(path)
        WHERE m.triage_reason IS NULL AND h.workspace_id = w.id
        ORDER BY l.depth DESC, h.id, p.path LIMIT 1
    ) beat ON true
    WHERE w.id = m.workspace_id AND m.forest_class IN ('inside', 'near_edge');
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION alert_route(m detection_matches)
RETURNS TABLE(queue text, hierarchy_id uuid, unit_path ltree)
LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN m.triage_reason IS NOT NULL OR beat.path IS NULL THEN 'triage'
                WHEN m.forest_class = 'near_edge' AND w.review_near_edge THEN 'edge_review'
                ELSE 'unit' END, beat.hierarchy_id, beat.path
    FROM workspaces w
    LEFT JOIN LATERAL (
        SELECT h.id AS hierarchy_id, p.path::ltree AS path
        FROM hierarchies h JOIN hierarchy_levels l ON l.hierarchy_id = h.id AND l.slug = 'beat'
        CROSS JOIN LATERAL jsonb_array_elements_text(m.hierarchy_unit_paths -> h.id::text) p(path)
        WHERE m.triage_reason IS NULL AND h.workspace_id = w.id AND nlevel(p.path::ltree) = l.depth + 1
        ORDER BY l.depth DESC, h.id, p.path LIMIT 1
    ) beat ON true
    WHERE w.id = m.workspace_id AND m.forest_class IN ('inside', 'near_edge');
$$;
-- +goose StatementEnd
DROP FUNCTION classify_detection(uuid, bigint, timestamptz, geometry, uuid);
DROP FUNCTION classify_detection_at(uuid, bigint, timestamptz, geometry, uuid, timestamptz);

ALTER TABLE export_rows DROP COLUMN dispatch, DROP COLUMN boundary_snapshot;
ALTER TABLE alerts DROP COLUMN boundary_snapshot;
ALTER TABLE detection_matches DROP COLUMN boundary_snapshot, DROP COLUMN beat_paths;
ALTER TABLE layer_versions DROP CONSTRAINT layer_versions_overlay_period_check,
    DROP COLUMN overlay_from, DROP COLUMN overlay_kind, DROP COLUMN effective_from;
DROP FUNCTION hierarchy_parent_allowed(uuid, smallint, smallint);
-- Preserve paths and grants if optional levels were used. The old length
-- rule applies to writes under the old schema.
ALTER TABLE draft_units DROP CONSTRAINT draft_units_parent_path_check,
    DROP CONSTRAINT draft_units_path_length_check,
    ADD CONSTRAINT draft_units_check CHECK (path IS NULL OR nlevel(path) = depth + 1) NOT VALID,
    ADD CONSTRAINT draft_units_check1 CHECK (parent_path IS NULL OR nlevel(parent_path) = depth) NOT VALID;
ALTER TABLE units DROP CONSTRAINT units_path_length_check,
    ADD CONSTRAINT units_check CHECK (nlevel(path) = depth + 1) NOT VALID;
ALTER TABLE hierarchy_levels DROP COLUMN optional;

-- +goose StatementBegin
CREATE FUNCTION classify_detection(workspace uuid, detection bigint, observed timestamptz, point geometry,
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
