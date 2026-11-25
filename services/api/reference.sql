WITH bounds AS (
    SELECT ST_TileEnvelope($2, $3, $4) AS tile,
           ST_Transform(ST_TileEnvelope($2, $3, $4, margin => 64.0 / 4096), 4326) AS search,
           40075016.686 / (512 * power(2.0, $2)) AS pixel
), shapes AS (
    SELECT 'units' AS kind, u.depth, h.slug AS hierarchy, level.slug AS level_slug,
           level.labels->>'en' AS level_label, u.code, u.name, NULL AS legal_status, u.geom
    FROM hierarchies h
    JOIN units u ON u.hierarchy_id = h.id AND u.valid_to IS NULL
    JOIN hierarchy_levels level ON level.hierarchy_id = h.id AND level.depth = u.depth
    CROSS JOIN bounds b
    WHERE h.workspace_id = $1 AND u.geom && b.search AND (NOT $5 OR u.depth = 0)
    UNION ALL
    SELECT CASE l.role WHEN 'forest' THEN 'forest' ELSE 'exclusions' END,
           NULL, NULL, NULL, NULL, f.code, f.name, f.attributes->>'legal_status', f.geom
    FROM layers l
    JOIN layer_versions v ON v.layer_id = l.id AND v.status = 'published'
    JOIN layer_features f ON f.layer_version_id = v.id
    CROSS JOIN bounds b
    WHERE NOT $5 AND l.workspace_id = $1 AND l.role IN ('forest', 'exclusion') AND f.geom && b.search
), projected AS MATERIALIZED (
    SELECT kind, depth, hierarchy, level_slug, level_label, code, name, legal_status,
           ST_Transform(geom, 3857) AS geom FROM shapes
), features AS MATERIALIZED (
    SELECT kind, depth, hierarchy, level_slug, level_label, code, name, legal_status,
           ST_AsMVTGeom(ST_SimplifyPreserveTopology(geom, b.pixel / 2), b.tile, 4096, 64) AS geom
    FROM projected CROSS JOIN bounds b
    WHERE depth = 0 OR ST_Dimension(geom) < 2 OR ST_Area(geom) >= b.pixel * b.pixel
), unit_tile AS (
    SELECT depth, hierarchy, level_slug, level_label, code, name, geom FROM features
    WHERE kind = 'units' AND geom IS NOT NULL
), forest_tile AS (
    SELECT code, name, legal_status, geom FROM features WHERE kind = 'forest' AND geom IS NOT NULL
), exclusion_tile AS (
    SELECT code, name, geom FROM features WHERE kind = 'exclusions' AND geom IS NOT NULL
)
SELECT (SELECT ST_AsMVT(unit_tile, 'units') FROM unit_tile) ||
       (SELECT ST_AsMVT(forest_tile, 'forest') FROM forest_tile) ||
       (SELECT ST_AsMVT(exclusion_tile, 'exclusions') FROM exclusion_tile)
