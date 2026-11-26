-- Both the alert and destination must be covered by the same officer grant.
WITH officer_grants AS MATERIALIZED (
    SELECT g.*
    FROM alert_grants g JOIN alerts a ON a.workspace_id = g.workspace_id
    WHERE g.oidc_subject = $1 AND g.slug = $2 AND a.id = $3
      AND g.can_review AND g.role_slug IN ('state_admin', 'division_officer')
      AND match_under_grant(a.hierarchy_unit_paths, g.hierarchy_id, g.unit_path, g.depth)
),
choices AS (
    SELECT u.id, u.hierarchy_id, u.path, u.name, u.depth, parent.name AS parent_name, usr.display_name,
           h.slug AS hierarchy, l.labels->>'en' AS level,
           COALESCE(u.hierarchy_id = a.hierarchy_id AND u.path = a.unit_path, false) OR COALESCE((a.hierarchy_unit_paths->h.id::text) ? u.path::text, false) AS matched
    FROM alerts a
    JOIN hierarchies h ON h.workspace_id = a.workspace_id
    JOIN units u ON u.hierarchy_id = h.id AND u.valid_to IS NULL AND u.depth > 0
    JOIN hierarchy_levels l ON (l.hierarchy_id, l.depth) = (u.hierarchy_id, u.depth)
    LEFT JOIN units parent ON parent.hierarchy_id = u.hierarchy_id
        AND parent.current_path = subpath(u.path, 0, -1)
    JOIN users usr ON usr.id = (SELECT user_id FROM officer_grants LIMIT 1)
    WHERE a.id = $3 AND a.queue IN ('triage', 'edge_review')
      AND ($4::bigint IS NULL OR u.id = $4)
      AND EXISTS (SELECT FROM officer_grants g WHERE g.depth = 0 OR
                  (g.hierarchy_id = u.hierarchy_id AND u.path <@ g.unit_path))
)
