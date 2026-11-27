-- Facts about each live feed of each workspace, as of $2. $1 limits the
-- result to one workspace, or is null for all of them. A feed is a source and
-- product that a workspace has polled at least once.
WITH feeds AS (
    SELECT r.workspace_id, r.source, r.product, min(r.started_at) AS first_run_at
    FROM ingest_runs r
    WHERE r.kind = 'poll' AND r.workspace_id IS NOT NULL
      AND ($1::uuid IS NULL OR r.workspace_id = $1::uuid) AND r.started_at <= $2
      AND (r.source <> 'firms' OR r.product = ANY($3::text[]))
    GROUP BY r.workspace_id, r.source, r.product
)
SELECT f.workspace_id::text, w.slug, w.settings, f.source, f.product, f.first_run_at,
       last_run.started_at, last_run.status, last_run.error_message,
       success.fetched_at, observed.latest_observed_at,
       (SELECT count(*) FROM ingest_runs r
        WHERE (r.workspace_id, r.kind, r.source, r.product) = (f.workspace_id, 'poll', f.source, f.product)
          AND r.status = 'failed' AND r.started_at > $2 - interval '24 hours' AND r.started_at <= $2)::integer,
       processing.records, processing.p95_seconds, processing.within_gate,
       first_seen.records, first_seen.p50_seconds, first_seen.p95_seconds,
       valid_scan.slot, COALESCE(scans.listed,0), scans.p50_seconds, scans.p95_seconds,
       COALESCE(missing.slots,0)
FROM feeds f
JOIN workspaces w ON w.id = f.workspace_id
LEFT JOIN LATERAL (
    SELECT r.started_at, r.status, r.error_message FROM ingest_runs r
    WHERE (r.workspace_id, r.kind, r.source, r.product) = (f.workspace_id, 'poll', f.source, f.product)
      AND r.started_at <= $2
    ORDER BY r.started_at DESC, r.id DESC LIMIT 1
) last_run ON true
LEFT JOIN LATERAL (
    SELECT r.fetched_at FROM ingest_runs r
    WHERE (r.workspace_id, r.kind, r.source, r.product) = (f.workspace_id, 'poll', f.source, f.product)
      AND r.status = 'succeeded' AND r.started_at <= $2
    ORDER BY r.started_at DESC, r.id DESC LIMIT 1
) success ON true
-- Each poll records the newest observation in its rolling window, so the
-- latest observation is usually in the last successful poll. Look back 90 days
-- for a feed that has been quiet for longer than the window.
LEFT JOIN LATERAL (
    SELECT max(r.latest_observed_at) AS latest_observed_at FROM ingest_runs r
    WHERE (r.workspace_id, r.kind, r.source, r.product) = (f.workspace_id, 'poll', f.source, f.product)
      AND (r.status = 'succeeded' OR (f.source = 'lsasaf' AND r.fetched_at IS NOT NULL))
      AND r.started_at > $2 - interval '90 days' AND r.started_at <= $2
) observed ON true
-- Processing time for the 20-minute release gate, over the last 7 days. A
-- record new in a comparable response could have been published as early as
-- the previous fetch, so the time from that fetch to the match is an upper
-- estimate of the time from publication to processing.
LEFT JOIN LATERAL (
    WITH delays AS (
        SELECT extract(epoch FROM m.matched_at - r.previous_fetched_at)::float8 AS seconds
        FROM ingest_runs r
        JOIN detections d ON d.ingest_run_id = r.id
        JOIN detection_matches m
          ON (m.workspace_id, m.detection_id, m.observed_at) = (f.workspace_id, d.id, d.observed_at)
        WHERE (r.workspace_id, r.kind, r.source, r.product) = (f.workspace_id, 'poll', f.source, f.product)
          AND f.source <> 'cdse'
          AND r.previous_fetched_at IS NOT NULL AND r.fetched_at > $2 - interval '7 days' AND r.fetched_at <= $2
          AND d.observed_at >= r.window_start AND d.observed_at <= r.fetched_at
          AND m.matched_at >= r.fetched_at AND m.matched_at <= $2
        UNION ALL
        -- CDSE provides publication timestamps and shares the inserting run
        -- across every workspace that classified the observation.
        SELECT extract(epoch FROM m.matched_at - (d.raw->>'published_at')::timestamptz)::float8
        FROM ingest_runs r
        JOIN detections d ON d.ingest_run_id = r.id
        JOIN detection_matches m
          ON (m.workspace_id, m.detection_id, m.observed_at) = (f.workspace_id, d.id, d.observed_at)
        WHERE f.source = 'cdse' AND (r.kind, r.source, r.product) = ('poll', f.source, f.product)
          AND r.fetched_at > $2 - interval '7 days' AND r.fetched_at <= $2
          AND (d.raw->>'published_at')::timestamptz <= m.matched_at AND m.matched_at <= $2
    )
    SELECT count(*)::integer AS records,
           percentile_cont(0.95) WITHIN GROUP (ORDER BY seconds) AS p95_seconds,
           avg((seconds <= 1200)::integer)::float8 AS within_gate
    FROM delays
) processing ON true
-- Acquisition to the response that first stored the record. This includes
-- aliases and is distinct from the release gate above.
LEFT JOIN LATERAL (
    SELECT count(*)::integer AS records,
           percentile_cont(0.50) WITHIN GROUP (ORDER BY lag.seconds) AS p50_seconds,
           percentile_cont(0.95) WITHIN GROUP (ORDER BY lag.seconds) AS p95_seconds
    FROM ingest_runs r
    JOIN detections d ON d.ingest_run_id = r.id
    CROSS JOIN LATERAL (SELECT extract(epoch FROM r.fetched_at - d.observed_at)::float8 AS seconds) lag
    WHERE (r.kind, r.source, r.product) = ('poll', f.source, f.product)
      AND (r.workspace_id = f.workspace_id OR (f.source = 'cdse' AND EXISTS (
          SELECT FROM detection_matches m
          WHERE (m.workspace_id, m.detection_id, m.observed_at) = (f.workspace_id, d.id, d.observed_at))))
      AND r.fetched_at > $2 - interval '7 days' AND r.fetched_at <= $2 AND lag.seconds >= 0
) first_seen ON true
LEFT JOIN LATERAL (
    SELECT count(*)::int AS listed,
           percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM s.first_listed_at - s.slot)) AS p50_seconds,
           percentile_cont(0.95) WITHIN GROUP (ORDER BY extract(epoch FROM s.first_listed_at - s.slot)) AS p95_seconds
    FROM lsasaf_scans s WHERE f.source = 'lsasaf' AND s.workspace_id = f.workspace_id
      AND s.first_listed_at BETWEEN $2 - interval '7 days' AND $2 AND s.first_listed_at >= s.slot
) scans ON f.source = 'lsasaf'
LEFT JOIN LATERAL (
    SELECT slot FROM lsasaf_scans s WHERE f.source = 'lsasaf' AND s.workspace_id = f.workspace_id
        AND s.validated_at <= $2 AND s.slot <= $2 ORDER BY slot DESC LIMIT 1
) valid_scan ON f.source = 'lsasaf'
-- Allow 45 minutes for publication. A successful HTTP poll can still leave a
-- gap when a listed file was missing or its scan quality failed validation.
LEFT JOIN LATERAL (
    SELECT count(*)::int AS slots
    FROM generate_series(date_bin(interval '15 minutes', GREATEST(f.first_run_at, $2 - interval '24 hours'),
                                    '2000-01-01'::timestamptz) + interval '15 minutes',
                         $2 - interval '45 minutes', interval '15 minutes') expected(slot)
    WHERE f.source = 'lsasaf' AND NOT EXISTS (
        SELECT FROM lsasaf_scans s WHERE s.workspace_id=f.workspace_id AND s.slot=expected.slot AND s.validated_at <= $2
    )
) missing ON f.source = 'lsasaf'
ORDER BY w.slug, f.source, f.product;
