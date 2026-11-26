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
                            ingest_run_id)
    SELECT $2, $3, source_key, satellite, instrument, observed_at,
           ST_SetSRID(ST_Point(longitude, latitude), 4326), scan_m, track_m,
           brightness_k, frp_mw, NULLIF(confidence, ''), NULLIF(day_night, ''), COALESCE(raw, '{}'),
           $5
    FROM input
    ON CONFLICT DO NOTHING
    RETURNING id, observed_at, geom, satellite, scan_m, track_m
),
-- The same satellite observation can arrive in two products of one source,
-- such as VIIRS_SNPP_NRT and later VIIRS_SNPP_SP ($4). Look for an earlier
-- record from the same satellite at the same minute, within half a pixel
-- (at least 187.5 metres). Each record links to its nearest such record, and
-- each earlier record takes at most one link, the nearest.
nearest AS (
    SELECT i.id, i.observed_at, e.id AS primary_id, e.observed_at AS primary_observed_at, e.distance_m
    FROM inserted i
    CROSS JOIN LATERAL (
        SELECT e.id, e.observed_at, ST_Distance(e.geom::geography, i.geom::geography) AS distance_m
        FROM detections e
        WHERE NULLIF($4::text, '') IS NOT NULL AND e.source = $2 AND e.product = $4::text
          AND e.observed_at = i.observed_at AND e.satellite IS NOT DISTINCT FROM i.satellite
          AND ST_DWithin(e.geom::geography, i.geom::geography,
                         GREATEST(COALESCE(i.scan_m, 0), COALESCE(i.track_m, 0), 375) / 2)
          AND NOT EXISTS (
              SELECT 1 FROM detection_links l
              WHERE (l.detection_id, l.observed_at) = (e.id, e.observed_at)
                 OR (l.primary_id, l.primary_observed_at) = (e.id, e.observed_at))
        ORDER BY distance_m, e.id
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
    CROSS JOIN workspaces w
    CROSS JOIN LATERAL classify_detection(w.id, d.id, d.observed_at, d.geom) m
    -- A linked record shares the earlier record's matches.
    WHERE NOT EXISTS (SELECT 1 FROM linked l WHERE (l.detection_id, l.observed_at) = (d.id, d.observed_at))
    ON CONFLICT DO NOTHING
    RETURNING detection_matches AS match
),
routed AS (
    -- The alert insert trigger opens its workflow in this transaction.
    INSERT INTO alerts (workspace_id, detection_id, observed_at, hierarchy, queue, hierarchy_id, unit_path,
                        hierarchy_unit_paths, unit_paths, forest_class, triage_reason, layer_version_ids, boundary_snapshot)
    SELECT (match).workspace_id, (match).detection_id, (match).observed_at, (match).hierarchy, route.*,
           (match).hierarchy_unit_paths, (match).unit_paths, (match).forest_class, (match).triage_reason,
           (match).layer_version_ids, (match).boundary_snapshot
    FROM matched CROSS JOIN LATERAL alert_route(match) route
    ON CONFLICT DO NOTHING
)
SELECT (SELECT count(*) FROM inserted)::integer, (SELECT count(*) FROM matched)::integer,
       (SELECT count(*) FROM linked)::integer;
