WITH workspace AS (
    SELECT w.id
    FROM workspaces w
    WHERE w.slug = $2
), grants AS MATERIALIZED (
    SELECT DISTINCT a.hierarchy_id, a.unit_path, u.geom
    FROM workspace w
    JOIN operational_role_assignments a ON a.workspace_id = w.id
    JOIN users usr ON usr.id = a.user_id AND usr.oidc_subject = $1
    JOIN units u ON u.hierarchy_id = a.hierarchy_id AND u.path = a.unit_path AND u.valid_to IS NULL
), extent AS (
    SELECT ST_Union(geom) AS geom FROM grants
), shapes AS (
    SELECT 'units' AS kind, jsonb_build_object('depth', u.depth, 'hierarchy', h.slug,
            'level_slug', lvl.slug, 'code', u.code, 'name', u.name) AS props, ST_SimplifyPreserveTopology(u.geom, 0.00025) AS geom
    FROM workspace w
    JOIN hierarchies h ON h.workspace_id = w.id
    JOIN units u ON u.hierarchy_id = h.id AND u.valid_to IS NULL
    JOIN hierarchy_levels lvl ON lvl.hierarchy_id = h.id AND lvl.depth = u.depth
    WHERE EXISTS (SELECT FROM grants g WHERE g.hierarchy_id = h.id AND u.path <@ g.unit_path)
    UNION ALL
    SELECT CASE l.role WHEN 'forest' THEN 'forest' ELSE 'exclusions' END,
           jsonb_build_object('code', f.code, 'name', f.name),
           ST_CollectionExtract(ST_Intersection(ST_SimplifyPreserveTopology(f.geom, 0.00025), e.geom), 3)
    FROM workspace w
    JOIN layers l ON l.workspace_id = w.id AND l.role IN ('forest', 'exclusion')
    JOIN layer_versions v ON v.layer_id = l.id AND v.status = 'published'
    JOIN layer_features f ON f.layer_version_id = v.id
    CROSS JOIN extent e
    WHERE ST_Intersects(f.geom, e.geom)
)
SELECT jsonb_build_object('type', 'FeatureCollection', 'features', COALESCE((
    SELECT jsonb_agg(jsonb_build_object('type', 'Feature', 'properties', props || jsonb_build_object('kind', kind),
            'geometry', ST_AsGeoJSON(geom)::jsonb))
    FROM shapes WHERE NOT ST_IsEmpty(geom)
), '[]'))
FROM workspace WHERE EXISTS (SELECT FROM grants)
