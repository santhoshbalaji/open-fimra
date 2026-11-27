WITH samples AS (
    SELECT r.source, r.product,
           CASE
               WHEN r.source = 'firms' THEN
                   CASE upper(d.satellite)
                       WHEN 'N20' THEN 'NOAA-20'
                       WHEN 'N21' THEN 'NOAA-21'
                       WHEN 'A' THEN 'Aqua'
                       WHEN 'AQUA' THEN 'Aqua'
                       WHEN 'T' THEN 'Terra'
                       WHEN 'TERRA' THEN 'Terra'
                       WHEN 'N' THEN 'Suomi NPP'
                       ELSE d.satellite
                   END
               ELSE d.satellite
           END AS satellite,
           r.fetched_at,
           extract(epoch FROM r.fetched_at - d.observed_at)::float8 AS seen_seconds,
           extract(epoch FROM m.matched_at - d.observed_at)::float8 AS match_seconds
    FROM ingest_runs r
    JOIN detections d ON d.ingest_run_id = r.id
    JOIN workspaces w ON w.id = $1
    LEFT JOIN detection_matches m
      ON (m.workspace_id, m.detection_id, m.observed_at) = (w.id, d.id, d.observed_at)
     AND m.matched_at >= r.fetched_at AND m.matched_at <= $2
    WHERE r.previous_fetched_at IS NOT NULL
      AND r.fetched_at > $2 - interval '168 hours' AND r.fetched_at <= $2
      AND d.observed_at >= r.window_start AND d.observed_at <= r.fetched_at
      AND (d.workspace_id IS NULL OR d.workspace_id = w.id)
      AND (m.detection_id IS NOT NULL OR detection_in_workspace(w, d.geom))
)
SELECT source, product, COALESCE(satellite, 'Unknown'), hours,
       count(*)::integer, percentile_cont(0.5) WITHIN GROUP (ORDER BY seen_seconds),
       percentile_cont(0.95) WITHIN GROUP (ORDER BY seen_seconds), max(seen_seconds),
       count(match_seconds)::integer, percentile_cont(0.5) WITHIN GROUP (ORDER BY match_seconds),
       percentile_cont(0.95) WITHIN GROUP (ORDER BY match_seconds), max(match_seconds)
FROM samples
CROSS JOIN (VALUES (24), (168)) AS windows(hours)
WHERE fetched_at > $2 - hours * interval '1 hour'
GROUP BY source, product, satellite, hours
ORDER BY source, product, satellite, hours;
