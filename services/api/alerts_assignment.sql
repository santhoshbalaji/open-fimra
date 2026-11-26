-- An officer can assign within their review area. Once assigned, an officer
-- covering the lead can hand it over or request help anywhere in the workspace.
WITH officer_grants AS MATERIALIZED (
    SELECT g.*
    FROM alert_grants g JOIN alerts a ON a.workspace_id = g.workspace_id
    WHERE g.oidc_subject = $1 AND g.slug = $2 AND a.id = $3
      AND can_dispatch_alert(a, g)
),
choices AS (
    SELECT u.id, u.hierarchy_id, u.path, u.name, u.depth, parent.name AS parent_name, usr.display_name,
           usr.id AS user_id, h.slug AS hierarchy, l.labels->>'en' AS level, dispatch_unit(u) AS unit,
           COALESCE((a.original_dispatch->'candidatePaths'->h.id::text) ? u.path::text, false) AS matched
    FROM alerts a
    JOIN hierarchies h ON h.workspace_id = a.workspace_id
    JOIN units u ON u.hierarchy_id = h.id AND u.valid_to IS NULL AND u.depth > 0
    JOIN hierarchy_levels l ON (l.hierarchy_id, l.depth) = (u.hierarchy_id, u.depth)
    LEFT JOIN units parent ON parent.hierarchy_id = u.hierarchy_id
        AND parent.current_path = subpath(u.path, 0, -1)
    JOIN users usr ON usr.id = (SELECT user_id FROM officer_grants LIMIT 1)
    WHERE a.id = $3
      AND ($4::bigint IS NULL OR u.id = $4)
      AND (a.hierarchy_id, a.unit_path) IS DISTINCT FROM (u.hierarchy_id, u.path)
      AND CASE WHEN $6::boolean THEN a.queue = 'unit' AND ($4 IS NOT NULL OR NOT EXISTS (
             SELECT FROM alert_support_requests r WHERE r.alert_id = a.id
               AND r.target->>'hierarchyId' = u.hierarchy_id::text AND r.target->>'path' = u.path::text))
           ELSE a.queue = 'unit' OR EXISTS (SELECT FROM officer_grants g WHERE g.depth = 0 OR
                     (g.hierarchy_id = u.hierarchy_id AND u.path <@ g.unit_path)) END
)
