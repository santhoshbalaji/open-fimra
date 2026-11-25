-- Share matching between ingest, layer previews and recent detection replays.
-- Keep each alert's routing snapshot and save progress for background replays.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION detection_in_workspace(w workspaces, point geometry)
RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT ST_DWithin(w.boundary::geography, point::geography,
                      COALESCE((w.settings->>'areaMarginMetres')::float8, 0));
$$;
-- +goose StatementEnd

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

-- Linked copies share the primary detection's matches and alerts.
CREATE VIEW matchable_detections AS
SELECT d.id, d.observed_at, d.geom
FROM detections d
WHERE NOT EXISTS (
    SELECT 1 FROM detection_links l WHERE (l.detection_id, l.observed_at) = (d.id, d.observed_at)
);

ALTER TABLE alerts
    ADD COLUMN hierarchy_unit_paths jsonb,
    ADD COLUMN unit_paths ltree[],
    ADD COLUMN forest_class text,
    ADD COLUMN triage_reason text,
    ADD COLUMN layer_version_ids uuid[];

UPDATE alerts a SET hierarchy_unit_paths = m.hierarchy_unit_paths, unit_paths = m.unit_paths,
    forest_class = m.forest_class, triage_reason = m.triage_reason, layer_version_ids = m.layer_version_ids
FROM detection_matches m
WHERE (a.workspace_id, a.detection_id, a.observed_at) = (m.workspace_id, m.detection_id, m.observed_at);

ALTER TABLE alerts
    ALTER COLUMN hierarchy_unit_paths SET NOT NULL,
    ALTER COLUMN unit_paths SET NOT NULL,
    ALTER COLUMN forest_class SET NOT NULL,
    ALTER COLUMN layer_version_ids SET NOT NULL;

-- Workflow access follows the same saved paths as the alert queue.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION alert_roles(subject text, workspace_slug text, alert bigint)
RETURNS TABLE(user_id uuid, role_slug text, officer boolean) LANGUAGE sql STABLE AS $$
    SELECT g.user_id,g.role_slug,g.role_slug='state_admin' OR (g.role_slug='division_officer' AND g.can_review)
    FROM alert_grants g JOIN alerts a ON a.workspace_id=g.workspace_id
    WHERE g.oidc_subject=subject AND g.slug=workspace_slug AND a.id=alert
      AND (a.queue='unit' OR g.can_review)
      AND match_under_grant(a.hierarchy_unit_paths,g.hierarchy_id,g.unit_path,g.depth);
$$;
-- +goose StatementEnd

CREATE TABLE layer_rematches (
    layer_version_id uuid PRIMARY KEY REFERENCES layer_versions ON DELETE CASCADE,
    river_job_id bigint NOT NULL,
    window_start timestamptz NOT NULL,
    window_end timestamptz NOT NULL,
    cursor_time timestamptz,
    cursor_id bigint,
    CHECK (window_start <= window_end),
    CHECK ((cursor_time IS NULL) = (cursor_id IS NULL)),
    processed bigint NOT NULL DEFAULT 0,
    matches_written bigint NOT NULL DEFAULT 0,
    alerts_created bigint NOT NULL DEFAULT 0,
    finished_at timestamptz
);

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION alert_roles(subject text, workspace_slug text, alert bigint)
RETURNS TABLE(user_id uuid, role_slug text, officer boolean) LANGUAGE sql STABLE AS $$
    SELECT g.user_id,g.role_slug,g.role_slug='state_admin' OR (g.role_slug='division_officer' AND g.can_review)
    FROM alert_grants g JOIN alerts a ON a.workspace_id=g.workspace_id
    JOIN detection_matches m ON (m.workspace_id,m.detection_id,m.observed_at)=(a.workspace_id,a.detection_id,a.observed_at)
    WHERE g.oidc_subject=subject AND g.slug=workspace_slug AND a.id=alert
      AND (a.queue='unit' OR g.can_review)
      AND match_under_grant(m.hierarchy_unit_paths,g.hierarchy_id,g.unit_path,g.depth);
$$;
-- +goose StatementEnd
DROP TABLE layer_rematches;
DROP VIEW matchable_detections;
ALTER TABLE alerts DROP COLUMN triage_reason, DROP COLUMN forest_class,
    DROP COLUMN unit_paths, DROP COLUMN hierarchy_unit_paths, DROP COLUMN layer_version_ids;
DROP FUNCTION classify_detection(uuid, bigint, timestamptz, geometry, uuid);
DROP FUNCTION detection_in_workspace(workspaces, geometry);
