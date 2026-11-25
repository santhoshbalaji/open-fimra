WITH grants AS MATERIALIZED (
    SELECT a.workspace_id, a.hierarchy_id, a.unit_path, u.depth
    FROM role_assignments a
    JOIN users caller ON caller.id = a.user_id AND caller.oidc_subject = $1
    JOIN workspaces w ON w.id = a.workspace_id AND w.slug = $2
    JOIN units u ON u.hierarchy_id = a.hierarchy_id AND u.current_path = a.unit_path
),
bounds AS (
    SELECT ST_TileEnvelope($3, $4, $5) AS tile,
           ST_Transform(ST_TileEnvelope($3, $4, $5, margin => 64.0 / 4096), 4326) AS search
),
points AS (
    SELECT d.id, floor(extract(epoch FROM d.observed_at))::bigint AS observed_at,
           CASE
             WHEN d.observed_at > now() - interval '6 hours' THEN 0
             WHEN d.observed_at > now() - interval '24 hours' THEN 1
             WHEN d.observed_at > now() - interval '72 hours' THEN 2
             ELSE 3
           END AS age,
           m.forest_class, COALESCE(m.triage_reason, '') AS triage,
           COALESCE(line.levels #>> '{beat,code}', '') AS beat_code,
           COALESCE(line.levels #>> '{beat,name}', '') AS beat_name,
           COALESCE(line.levels #>> '{range,name}', '') AS range_name,
           COALESCE(line.levels #>> '{division,name}', '') AS division_name,
           COALESCE(line.hierarchy, '') AS place_hierarchy, COALESCE(line.levels::text, '') AS places,
           d.source, d.product, d.frp_mw, d.confidence,
           ST_AsMVTGeom(ST_Transform(d.geom, 3857), b.tile, 4096, 64) AS geom
    FROM bounds b
    JOIN detections d ON d.geom && b.search
    JOIN detection_matches m ON (m.detection_id, m.observed_at) = (d.id, d.observed_at)
    LEFT JOIN LATERAL (
        SELECT key AS hierarchy, value AS levels FROM jsonb_each(m.hierarchy) ORDER BY key LIMIT 1
    ) line ON true
    WHERE d.observed_at BETWEEN now() - make_interval(hours => $6) AND now()
      AND m.observed_at BETWEEN now() - make_interval(hours => $6) AND now()
      AND m.forest_class = ANY($7::text[])
      AND ($8::ltree IS NULL OR m.unit_paths <@ $8)
      AND EXISTS (
          SELECT 1 FROM grants g WHERE g.workspace_id = m.workspace_id AND
              match_under_grant(m.hierarchy_unit_paths, g.hierarchy_id, g.unit_path, g.depth)
      )
)
SELECT EXISTS (SELECT 1 FROM grants),
       COALESCE((SELECT ST_AsMVT(points, 'detections') FROM points WHERE geom IS NOT NULL), ''::bytea);
