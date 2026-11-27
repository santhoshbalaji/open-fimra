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
                            geom, scan_m, track_m, brightness_k, frp_mw, confidence, day_night, raw,
                            ingest_run_id, workspace_id)
    SELECT $2, $3, source_key, satellite, instrument, observed_at,
           ST_SetSRID(ST_Point(longitude, latitude), 4326), scan_m, track_m,
           brightness_k, frp_mw, NULLIF(confidence, ''), NULLIF(day_night, ''), COALESCE(raw, '{}'),
           $5, $6
    FROM input
    ON CONFLICT DO NOTHING
    RETURNING id, observed_at, geom, satellite, scan_m, track_m
),
-- FIRMS NRT/SP keep their exact-minute rule. Across NOAA and FIRMS the
-- timestamps can differ because NOAA provides the start of an ~85 s swath.
-- A candidate can itself be an alias. Link directly to its primary and
-- reserve one record per arriving source/product in each observation.
nearest AS (
    SELECT i.id, i.observed_at, e.primary_id, e.primary_observed_at, e.distance_m
    FROM inserted i
    CROSS JOIN LATERAL (
        SELECT p.id AS primary_id, p.observed_at AS primary_observed_at,
               ST_Distance(e.geom::geography, i.geom::geography) AS distance_m
        FROM detections e
        LEFT JOIN detection_links alias ON (alias.detection_id, alias.observed_at) = (e.id, e.observed_at)
        JOIN detections p ON (p.id, p.observed_at) =
            (COALESCE(alias.primary_id, e.id), COALESCE(alias.primary_observed_at, e.observed_at))
        WHERE e.workspace_id IS NOT DISTINCT FROM $6::uuid
          AND e.observed_at BETWEEN i.observed_at - interval '2 minutes' AND i.observed_at + interval '2 minutes'
          AND (
              (e.source = $2 AND e.product = NULLIF($4::text, '')
               AND e.observed_at = i.observed_at AND e.satellite IS NOT DISTINCT FROM i.satellite
               AND ST_DWithin(e.geom::geography, i.geom::geography,
                              GREATEST(i.scan_m, i.track_m, 375) / 2))
              OR
              (e.source = $8 AND e.product = ANY($7::text[])
               AND CASE upper(i.satellite) WHEN '1' THEN 'N20' WHEN '2' THEN 'N21' ELSE upper(i.satellite) END
                   = CASE upper(e.satellite) WHEN '1' THEN 'N20' WHEN '2' THEN 'N21' ELSE upper(e.satellite) END
               AND ST_DWithin(e.geom::geography, i.geom::geography,
                   GREATEST(CASE WHEN $2 = 'firms' THEN i.scan_m ELSE e.scan_m END,
                            CASE WHEN $2 = 'firms' THEN i.track_m ELSE e.track_m END,
                            CASE WHEN $2 = 'firms' AND i.scan_m IS NULL AND i.track_m IS NULL THEN e.scan_m
                                 WHEN $2 <> 'firms' AND e.scan_m IS NULL AND e.track_m IS NULL THEN i.scan_m END,
                            375) / 2))
          )
          AND (p.source, p.product) <> ($2, $3)
          AND NOT EXISTS (
              SELECT 1 FROM detection_links l
              JOIN detections d ON (d.id, d.observed_at) = (l.detection_id, l.observed_at)
              WHERE (l.primary_id, l.primary_observed_at) = (p.id, p.observed_at)
                AND (d.source, d.product) = ($2, $3))
        ORDER BY (e.source = $2) DESC, distance_m, p.id
        LIMIT 1
    ) e
),
linked AS (
    INSERT INTO detection_links (detection_id, observed_at, primary_id, primary_observed_at, reason, distance_m)
    SELECT DISTINCT ON (primary_id, primary_observed_at)
           id, observed_at, primary_id, primary_observed_at, 'same_observation', distance_m
    FROM nearest
    ORDER BY primary_id, primary_observed_at, distance_m, id
    ON CONFLICT DO NOTHING
    RETURNING detection_id, observed_at
),
matched AS (
    INSERT INTO detection_matches (workspace_id, detection_id, observed_at, forest_class,
                                   forest_distance_m, forest_feature_id, unit_paths, hierarchy,
                                   hierarchy_unit_paths, triage_reason, layer_version_ids, matched_at,
                                   beat_paths, boundary_snapshot)
    SELECT m.workspace_id, m.detection_id, m.observed_at, m.forest_class,
           m.forest_distance_m, m.forest_feature_id, m.unit_paths, m.hierarchy,
           m.hierarchy_unit_paths, m.triage_reason, m.layer_version_ids, clock_timestamp(),
           m.beat_paths, m.boundary_snapshot
    FROM inserted d
    JOIN workspaces w ON $6::uuid IS NULL OR w.id = $6::uuid
    CROSS JOIN LATERAL classify_detection(w.id, d.id, d.observed_at, d.geom) m
    -- A linked record shares the earlier record's matches.
    WHERE NOT EXISTS (SELECT 1 FROM linked l WHERE (l.detection_id, l.observed_at) = (d.id, d.observed_at))
    ON CONFLICT DO NOTHING
    RETURNING detection_matches AS match
),
routes AS MATERIALIZED (
    SELECT match, route.*,
           CASE WHEN $2::text IN ('firms', 'noaa_nodd') THEN
               lsasaf_confirm(match, d.geom, route.queue, route.hierarchy_id, route.unit_path)
           END AS existing_alert
    FROM matched CROSS JOIN LATERAL alert_route(match) route
    JOIN inserted d ON (d.id,d.observed_at) = ((match).detection_id,(match).observed_at)
),
routed AS (
    -- The alert insert trigger opens its workflow in this transaction.
    INSERT INTO alerts (workspace_id, detection_id, observed_at, hierarchy, queue, hierarchy_id, unit_path,
                        hierarchy_unit_paths, unit_paths, forest_class, triage_reason, layer_version_ids, boundary_snapshot)
    SELECT (match).workspace_id, (match).detection_id, (match).observed_at, (match).hierarchy, queue, hierarchy_id, unit_path,
           (match).hierarchy_unit_paths, (match).unit_paths, (match).forest_class, (match).triage_reason,
           (match).layer_version_ids, (match).boundary_snapshot
    FROM routes WHERE existing_alert IS NULL
    ON CONFLICT DO NOTHING
)
SELECT (SELECT count(*) FROM inserted)::integer, (SELECT count(*) FROM matched)::integer,
       (SELECT count(*) FROM linked)::integer;
