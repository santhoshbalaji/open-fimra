-- Synthetic test data for the field app. The demo seed has no forest in beat
-- B01, where the demo beat guard works, so the guard would have no alerts.
-- This adds one made-up forest patch inside B01 to a separate test database.
-- Never load it into a shared development database: the export test expects
-- the beat guard to see no alerts there.
INSERT INTO layer_features (layer_version_id, code, name, attributes, geom, area_m2)
SELECT v.id, 'F90', 'Field app test forest', '{"legal_status": "reserved_forest"}'::jsonb, patch.geom,
       ST_Area(patch.geom::geography)
FROM layers l
JOIN workspaces w ON w.id = l.workspace_id AND w.slug = 'demo'
JOIN layer_versions v ON v.layer_id = l.id AND v.status = 'published'
CROSS JOIN LATERAL (
    SELECT ST_Multi(ST_Intersection(u.geom,
               ST_Expand(ST_SetSRID(ST_Point(86.2264, 16.3470), 4326), 0.01))) AS geom
    FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
    WHERE h.workspace_id = w.id AND u.code = 'B01' AND u.valid_to IS NULL
    ORDER BY h.slug
    LIMIT 1
) patch
WHERE l.role = 'forest'
  AND NOT EXISTS (SELECT 1 FROM layer_features f WHERE f.layer_version_id = v.id AND f.code = 'F90');
