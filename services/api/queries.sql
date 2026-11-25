-- name: UpsertUser :one
INSERT INTO users (oidc_subject, username, display_name)
VALUES ($1, $2, $3)
ON CONFLICT (oidc_subject) DO UPDATE
SET username = EXCLUDED.username,
    display_name = EXCLUDED.display_name,
    last_seen_at = GREATEST(users.last_seen_at, EXCLUDED.last_seen_at)
RETURNING id;

-- name: ListRoleAssignments :many
SELECT w.slug AS workspace_slug, w.name AS workspace_name, h.slug AS hierarchy_slug,
       a.role_slug, u.code AS unit_code, u.name AS unit_name,
       level.slug AS level_slug, u.path::text AS unit_path,
       (SELECT count(*)
        FROM units beat
        JOIN hierarchy_levels beat_level
          ON beat_level.hierarchy_id = beat.hierarchy_id AND beat_level.depth = beat.depth
        WHERE beat.hierarchy_id = a.hierarchy_id AND beat_level.slug = 'beat'
          AND beat.valid_to IS NULL AND beat.path <@ a.unit_path) AS beat_count
FROM role_assignments a
JOIN workspaces w ON w.id = a.workspace_id
JOIN hierarchies h ON h.id = a.hierarchy_id
JOIN units u ON u.hierarchy_id = a.hierarchy_id AND u.current_path = a.unit_path
JOIN hierarchy_levels level ON level.hierarchy_id = u.hierarchy_id AND level.depth = u.depth
WHERE a.user_id = $1
ORDER BY w.slug, h.slug, a.unit_path, a.role_slug;
