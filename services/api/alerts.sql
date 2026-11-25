WITH grants AS MATERIALIZED (
    SELECT a.workspace_id, a.hierarchy_id, a.unit_path, u.depth,
           u.depth = 0 OR u.depth <= division.depth AS can_review
    FROM role_assignments a
    JOIN users caller ON caller.id = a.user_id AND caller.oidc_subject = $1
    JOIN workspaces w ON w.id = a.workspace_id AND w.slug = $2
    JOIN units u ON u.hierarchy_id = a.hierarchy_id AND u.current_path = a.unit_path
    LEFT JOIN hierarchy_levels division ON division.hierarchy_id = u.hierarchy_id AND division.slug = 'division'
),
visible AS (
    SELECT a.id, a.queue, a.workspace_id, a.observed_at, a.detection_id
    FROM alerts a JOIN detection_matches m USING (workspace_id, detection_id, observed_at)
    WHERE EXISTS (
        SELECT 1 FROM grants g WHERE g.workspace_id = a.workspace_id
          AND (a.queue = 'unit' OR g.can_review)
          AND match_under_grant(m.hierarchy_unit_paths, g.hierarchy_id, g.unit_path, g.depth)
    )
      AND ($3::bigint IS NULL OR a.id = $3)
      AND ($3 IS NOT NULL OR a.observed_at BETWEEN now() - make_interval(hours => $4) AND now())
      AND ($6 = '' OR a.status = $6)
      AND ($7 = '' OR m.forest_class = $7)
      AND ($8::ltree IS NULL OR m.unit_paths <@ $8)
      AND ($12::bigint IS NULL OR a.detection_id = $12)
),
page AS (
    SELECT * FROM visible a
    WHERE ($5 = '' OR a.queue = $5)
      AND ($9::timestamptz IS NULL OR (a.observed_at, a.id) < ($9, $10::bigint))
    ORDER BY a.observed_at DESC, a.id DESC LIMIT $11
),
details AS (
    SELECT p.id::text AS id, p.detection_id::text AS detection_id, p.queue, a.status, a.created_at,
           a.hierarchy_id, a.unit_path::text, a.hierarchy, p.observed_at AS observed_time,
           floor(extract(epoch FROM p.observed_at))::bigint AS observed_at,
           ST_X(d.geom) AS longitude, ST_Y(d.geom) AS latitude,
           m.forest_class, CASE WHEN p.queue = 'triage' THEN COALESCE(m.triage_reason, 'no_unit') END AS triage,
           d.source, d.product, d.confidence, d.frp_mw,
           line.levels #>> '{beat,name}' AS beat_name, line.levels #>> '{beat,code}' AS beat_code,
           line.levels #>> '{range,name}' AS range_name, line.levels #>> '{division,name}' AS division_name
    FROM page p JOIN alerts a ON a.id = p.id
    JOIN detection_matches m ON (m.workspace_id, m.detection_id, m.observed_at) = (p.workspace_id, p.detection_id, p.observed_at)
    JOIN detections d ON (d.id, d.observed_at) = (p.detection_id, p.observed_at)
    LEFT JOIN hierarchies h ON h.id = a.hierarchy_id
    LEFT JOIN LATERAL (
        SELECT value AS levels FROM jsonb_each(a.hierarchy)
        ORDER BY (key = h.slug) DESC, key LIMIT 1
    ) line ON true
)
SELECT EXISTS (SELECT 1 FROM grants), EXISTS (SELECT 1 FROM grants WHERE can_review),
       (SELECT COALESCE(jsonb_object_agg(queue, n), '{}') FROM
           (SELECT queue, count(*) AS n FROM visible GROUP BY queue) counts),
       COALESCE((SELECT jsonb_agg(to_jsonb(details) ORDER BY observed_time DESC, id::bigint DESC)
                 FROM details), '[]')
