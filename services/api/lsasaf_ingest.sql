WITH raw AS (
    SELECT r.*, ST_SetSRID(ST_GeomFromGeoJSON(r.footprint), 4326) AS cell,
           ST_SetSRID(ST_Point(r.longitude, r.latitude), 4326) AS centre
    FROM jsonb_to_recordset($1::jsonb) AS r(
        source_key text, satellite text, instrument text, longitude float8, latitude float8,
        scan_m real, track_m real, brightness_k real, frp_mw real, confidence text, raw jsonb,
        line smallint, pixel smallint, footprint jsonb)
),
input AS MATERIALIZED (
    SELECT r.*, COALESCE((w.settings->>'nearEdgeMetres')::float8, 0) AS near_m
    FROM raw r JOIN workspaces w ON w.id = $2
    WHERE ST_Intersects(w.boundary, r.cell)
),
inserted AS (
    INSERT INTO detections (source, product, source_key, satellite, instrument, observed_at, geom,
                            scan_m, track_m, brightness_k, frp_mw, confidence, raw, ingest_run_id, workspace_id)
    SELECT 'lsasaf', 'SEVIRI_IODC_FRP', source_key, satellite, instrument, $3, centre,
           scan_m, track_m, brightness_k, frp_mw, NULLIF(confidence, ''), raw, $4, $2
    FROM input ON CONFLICT DO NOTHING RETURNING id, source_key
),
layers_at_scan AS MATERIALIZED (
    SELECT v.id, l.role
    FROM layers l JOIN layer_versions v ON v.layer_id = l.id
    WHERE l.workspace_id = $2 AND l.role IN ('forest', 'exclusion')
      AND COALESCE(v.effective_from, v.published_at) <= $3
      AND (v.retired_at IS NULL OR $3 < v.retired_at)
),
classified AS MATERIALIZED (
    SELECT d.id, i.cell, i.line, i.pixel, paths.*,
           forest.id AS forest_id, forest.distance AS forest_distance,
           CASE WHEN EXISTS (
                SELECT FROM layers_at_scan l JOIN layer_features f ON f.layer_version_id = l.id
                WHERE l.role = 'exclusion' AND ST_Covers(f.geom, i.cell)
           ) THEN 'fixed_heat_source'
           WHEN forest.distance = 0 THEN 'inside'
           WHEN forest.id IS NOT NULL THEN 'near_edge'
           ELSE 'outside' END AS forest_class
    FROM inserted d JOIN input i USING (source_key)
    LEFT JOIN LATERAL (
        SELECT f.id, ST_Distance(f.geom::geography, i.cell::geography) AS distance
        FROM layers_at_scan l JOIN layer_features f ON f.layer_version_id = l.id
        WHERE l.role = 'forest' AND f.geom && ST_Expand(i.cell,
            i.near_m / (110000 * cos(radians(LEAST(89.9, GREATEST(abs(ST_YMax(i.cell)), abs(ST_YMin(i.cell)))
                + i.near_m / 110000)))), i.near_m / 110000)
          AND ST_DWithin(f.geom::geography, i.cell::geography, i.near_m)
        ORDER BY distance, f.id LIMIT 1
    ) forest ON true
    CROSS JOIN LATERAL (
        WITH candidates AS MATERIALIZED (
            SELECT u.hierarchy_id, u.path, u.layer_version_id, l.slug, u.code, u.name
            FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
            JOIN hierarchy_levels l ON l.hierarchy_id = u.hierarchy_id AND l.depth = u.depth
            WHERE h.workspace_id = $2 AND u.valid_from <= $3 AND (u.valid_to IS NULL OR $3 < u.valid_to)
              AND ST_Intersects(u.geom, i.cell)
        )
        SELECT ARRAY(SELECT DISTINCT path FROM candidates ORDER BY path) AS paths,
               COALESCE((SELECT jsonb_object_agg(hierarchy_id, paths) FROM (
                   SELECT hierarchy_id, array_agg(path ORDER BY path) AS paths FROM candidates GROUP BY 1
               ) by_hierarchy), '{}') AS hierarchy_paths,
               COALESCE((SELECT jsonb_object_agg(hierarchy_id, paths) FROM (
                   SELECT hierarchy_id, array_agg(path ORDER BY path) AS paths
                   FROM candidates WHERE slug = 'beat' GROUP BY 1
               ) by_hierarchy), '{}') AS beat_paths,
               COALESCE((SELECT jsonb_agg(jsonb_build_object('path', path, 'code', code, 'name', name)
                       ORDER BY hierarchy_id, path) FROM candidates WHERE slug = 'beat'), '[]') AS beats,
               ARRAY(SELECT id FROM layers_at_scan UNION
                     SELECT layer_version_id FROM candidates WHERE layer_version_id IS NOT NULL ORDER BY 1) AS versions
    ) paths
),
matched AS (
    INSERT INTO detection_matches (workspace_id, detection_id, observed_at, forest_class,
        forest_distance_m, forest_feature_id, unit_paths, hierarchy_unit_paths, triage_reason,
        layer_version_ids, matched_at, beat_paths, boundary_snapshot)
    SELECT $2, id, $3, forest_class, forest_distance, forest_id, paths, hierarchy_paths,
           CASE WHEN beats = '[]' THEN 'no_unit' WHEN jsonb_array_length(beats) > 1 THEN 'overlapping_units' END,
           versions, clock_timestamp(), beat_paths,
           jsonb_build_object('forestClass', forest_class, 'layerVersionIds', versions, 'candidateBeats', beats)
    FROM classified RETURNING detection_id
),
watched AS MATERIALIZED (
    SELECT c.*, previous.id AS case_id
    FROM classified c
    LEFT JOIN LATERAL (
        SELECT w.id FROM lsasaf_cases w WHERE w.workspace_id = $2 AND (w.line, w.pixel) = (c.line, c.pixel)
          AND w.last_slot >= $3::timestamptz - interval '2 hours' AND w.first_slot <= $3::timestamptz + interval '2 hours'
        ORDER BY CASE WHEN $3 BETWEEN w.first_slot AND w.last_slot THEN 0
                      ELSE LEAST(abs(extract(epoch FROM $3 - w.first_slot)), abs(extract(epoch FROM $3 - w.last_slot))) END,
                 w.id LIMIT 1
    ) previous ON true
    WHERE c.forest_class IN ('inside', 'near_edge')
),
new_cases AS (
    INSERT INTO lsasaf_cases(workspace_id,line,pixel,first_slot,last_slot)
    SELECT DISTINCT $2, line, pixel, $3, $3 FROM watched WHERE case_id IS NULL
    RETURNING id, line, pixel
),
extended AS (
    UPDATE lsasaf_cases w SET first_slot = LEAST(first_slot,$3), last_slot = GREATEST(last_slot,$3)
    WHERE id IN (SELECT case_id FROM watched) RETURNING id
),
footprints AS (
    INSERT INTO lsasaf_pixels(workspace_id,detection_id,slot,case_id,footprint)
    SELECT $2, c.id, $3, COALESCE(e.id, n.id), c.cell
    FROM classified c JOIN matched m ON m.detection_id = c.id
    LEFT JOIN watched w ON w.id = c.id
    LEFT JOIN extended e ON e.id = w.case_id
    LEFT JOIN new_cases n ON (n.line,n.pixel) = (w.line,w.pixel)
)
SELECT (SELECT count(*) FROM input)::int, (SELECT count(*) FROM inserted)::int;
