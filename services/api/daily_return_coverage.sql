WITH expected AS (
    SELECT u.path, u.name, u.code
    FROM daily_return_units($1, $2, least($4, now())) u
    WHERE ($5::ltree IS NULL OR u.path <@ $5)
      AND EXISTS (
        SELECT FROM permanent_alert_grants g WHERE g.workspace_id = $6 AND g.oidc_subject = $7
          AND (g.depth = 0 OR (g.hierarchy_id = $1 AND u.path <@ g.unit_path))
      )
), coverage AS (
    SELECT e.*, r.id, r.daily_revision, r.content #> '{dailyReturn,nil}' AS nil, d.decision,
           CASE WHEN r.id IS NOT NULL THEN 'reported' WHEN now() >= $4 THEN 'missing' ELSE 'pending' END AS status
    FROM expected e LEFT JOIN LATERAL (
        SELECT r.* FROM situation_reports r WHERE r.daily_policy_id = $8 AND r.period_from = $3 AND r.unit_path = e.path
        ORDER BY r.daily_revision DESC LIMIT 1
    ) r ON true LEFT JOIN daily_return_decisions d ON d.report_id = r.id
), chosen AS (
    SELECT DISTINCT ON (r.unit_path, r.period_from) r.*
    FROM situation_reports r
    LEFT JOIN daily_return_decisions d ON d.report_id = r.id
    WHERE NOT $9::boolean AND r.daily_policy_id = $8 AND r.period_from <= $3 AND d.decision IS DISTINCT FROM 'returned'
      AND ($5::ltree IS NULL OR r.unit_path <@ $5)
      AND EXISTS (
        SELECT FROM permanent_alert_grants g WHERE g.workspace_id = $6 AND g.oidc_subject = $7
          AND (g.depth = 0 OR (g.hierarchy_id = $1 AND r.unit_path <@ g.unit_path))
      )
    ORDER BY r.unit_path, r.period_from, (d.decision IS NOT DISTINCT FROM 'approved') DESC, r.daily_revision DESC
), expanded AS (
    SELECT c.period_from, c.created_at, f.*
    FROM chosen c CROSS JOIN LATERAL jsonb_to_recordset(c.content #> '{dailyReturn,facts}')
        AS f(kind text, key text, metric text, amount numeric, at timestamptz)
), daily AS (
    SELECT DISTINCT ON (kind, key) metric, amount
    FROM expanded WHERE period_from = $3
    ORDER BY kind, key, (metric LIKE 'area.%.approved') DESC, at DESC NULLS LAST, created_at DESC
), season AS (
    SELECT DISTINCT ON (kind, key, CASE WHEN kind IN ('outcome','verification','received') THEN period_from END) metric, amount
    FROM expanded WHERE period_from = $3 OR kind NOT IN ('active','overdue','overdue_alert')
    ORDER BY kind, key, CASE WHEN kind IN ('outcome','verification','received') THEN period_from END,
        (metric LIKE 'area.%.approved') DESC, at DESC NULLS LAST, period_from DESC, created_at DESC
)
SELECT jsonb_build_object('units', coalesce((SELECT jsonb_agg(jsonb_build_object(
      'path', path::text, 'name', name, 'code', code, 'status', status, 'nil', nil,
      'revision', daily_revision, 'decision', decision) ORDER BY path) FROM coverage), '[]'),
    'expected', (SELECT count(*) FROM coverage),
    'reported', (SELECT count(*) FROM coverage WHERE status = 'reported'),
    'missing', (SELECT count(*) FROM coverage WHERE status = 'missing'),
    'daily', coalesce((SELECT jsonb_object_agg(metric, total) FROM (SELECT metric, sum(amount) AS total FROM daily GROUP BY metric) t), '{}'),
    'season', coalesce((SELECT jsonb_object_agg(metric, total) FROM (SELECT metric, sum(amount) AS total FROM season GROUP BY metric) t), '{}'))
