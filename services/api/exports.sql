-- Freezes the rows the caller may see into a new export, in one statement.
-- The grant rule is the one in tiles.sql for detections and alerts.sql for
-- alerts: the alert_grants view and match_under_grant(), on the match's paths
-- for detections and on the alert's own snapshot for alerts. Review queues
-- need a grant at division level or above.
-- $1 subject, $2 workspace slug, $3 kind, $4 format, $5 hours, $6 forest classes,
-- $7 queue ('' for all), $8 unit path prefix (or NULL), $9 workflow step ('' for all),
-- $10 maximum rows, $11 lifetime of the file.
WITH grants AS MATERIALIZED (
    SELECT g.*, u.code, u.name, level.slug AS level
    FROM alert_grants g
    JOIN units u ON u.hierarchy_id = g.hierarchy_id AND u.current_path = g.unit_path
    JOIN hierarchy_levels level ON level.hierarchy_id = u.hierarchy_id AND level.depth = u.depth
    WHERE g.oidc_subject = $1 AND g.slug = $2
),
detection_rows AS (
    SELECT m.workspace_id, m.detection_id, m.observed_at, NULL::bigint AS alert_id
    FROM detection_matches m
    JOIN detections d ON (d.id, d.observed_at) = (m.detection_id, m.observed_at)
    WHERE $3 = 'detections'
      AND d.observed_at BETWEEN now() - make_interval(hours => $5) AND now()
      AND m.observed_at BETWEEN now() - make_interval(hours => $5) AND now()
      AND m.forest_class = ANY($6::text[])
      AND ($8::ltree IS NULL OR m.unit_paths <@ $8)
      AND EXISTS (
          SELECT 1 FROM grants g WHERE g.workspace_id = m.workspace_id AND
              match_under_grant(m.hierarchy_unit_paths, g.hierarchy_id, g.unit_path, g.depth)
      )
),
alert_rows AS (
    -- Alerts keep their own routing snapshot, as in alerts.sql.
    SELECT a.workspace_id, a.detection_id, a.observed_at, a.id AS alert_id
    FROM alerts a JOIN workflow_instances i ON i.alert_id = a.id
    WHERE $3 = 'alerts'
      AND EXISTS (
          SELECT 1 FROM grants g WHERE g.workspace_id = a.workspace_id
            AND (a.queue = 'unit' OR g.can_review)
            AND match_under_grant(a.hierarchy_unit_paths, g.hierarchy_id, g.unit_path, g.depth)
      )
      AND a.observed_at BETWEEN now() - make_interval(hours => $5) AND now()
      AND ($7 = '' OR a.queue = $7)
      AND ($9 = '' OR i.current_step = $9)
      AND a.forest_class = ANY($6::text[])
      AND ($8::ltree IS NULL OR a.unit_paths <@ $8)
),
permitted AS (
    SELECT row_number() OVER (ORDER BY observed_at DESC, detection_id DESC, alert_id DESC)::integer AS ordinal, *
    FROM (SELECT * FROM detection_rows UNION ALL SELECT * FROM alert_rows) picked
),
total AS (SELECT count(*)::integer AS n FROM permitted),
step AS (
    SELECT $9 = '' OR EXISTS (
        SELECT FROM workflow_steps s JOIN workflow_templates t ON t.id = s.template_id
        WHERE s.slug = $9 AND (t.workspace_id IS NULL OR t.workspace_id IN (SELECT workspace_id FROM grants))
    ) AS known
),
created AS (
    INSERT INTO exports (workspace_id, requested_by, kind, format, filters, requester_roles, row_count, expires_at)
    SELECT w.id, $1, $3, $4,
           jsonb_build_object(
               'hours', $5,
               'from', to_char((now() - make_interval(hours => $5)) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
               'to', to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
               'forestClasses', to_jsonb($6::text[]), 'queue', NULLIF($7, ''),
               'unitPath', $8::text, 'step', NULLIF($9, '')),
           (SELECT jsonb_agg(DISTINCT jsonb_build_object('role', g.role_slug, 'level', g.level,
                                                        'unitCode', g.code, 'unitName', g.name))
            FROM grants g),
           total.n, now() + $11::interval
    FROM workspaces w, total
    WHERE w.slug = $2 AND EXISTS (SELECT 1 FROM grants) AND total.n <= $10 AND (SELECT known FROM step)
    RETURNING id, filters
),
frozen AS (
    INSERT INTO export_rows (export_id, ordinal, workspace_id, detection_id, observed_at, alert_id)
    SELECT c.id, p.ordinal, p.workspace_id, p.detection_id, p.observed_at, p.alert_id
    FROM created c CROSS JOIN permitted p
    RETURNING 1
)
SELECT EXISTS (SELECT 1 FROM grants), (SELECT known FROM step), (SELECT n FROM total),
       (SELECT id FROM created), (SELECT filters FROM created),
       (SELECT count(*) FROM frozen)
