-- +goose Up
ALTER TABLE workspaces ADD COLUMN review_near_edge boolean NOT NULL DEFAULT true;

CREATE TABLE alerts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL,
    detection_id bigint NOT NULL,
    observed_at timestamptz NOT NULL,
    queue text NOT NULL CHECK (queue IN ('unit', 'edge_review', 'triage')),
    hierarchy_id uuid,
    hierarchy jsonb NOT NULL,
    unit_path ltree,
    status text NOT NULL DEFAULT 'new' CHECK (status = 'new'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, detection_id),
    FOREIGN KEY (workspace_id, detection_id, observed_at)
        REFERENCES detection_matches ON DELETE CASCADE,
    CHECK ((hierarchy_id IS NULL) = (unit_path IS NULL)),
    CHECK ((queue = 'triage') = (unit_path IS NULL))
);
CREATE INDEX alerts_recent_idx ON alerts (workspace_id, observed_at DESC, id DESC);

-- The match owns the historical paths. Even a backfill after a boundary edit
-- must use those paths, not intersect the point with today's unit shapes.
-- +goose StatementBegin
CREATE FUNCTION alert_route(m detection_matches)
RETURNS TABLE(queue text, hierarchy_id uuid, unit_path ltree)
LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN m.triage_reason IS NOT NULL OR beat.path IS NULL THEN 'triage'
                WHEN m.forest_class = 'near_edge' AND w.review_near_edge THEN 'edge_review'
                ELSE 'unit' END, beat.hierarchy_id, beat.path
    FROM workspaces w
    LEFT JOIN LATERAL (
        SELECT h.id AS hierarchy_id, p.path::ltree AS path
        FROM hierarchies h JOIN hierarchy_levels l ON l.hierarchy_id = h.id AND l.slug = 'beat'
        CROSS JOIN LATERAL jsonb_array_elements_text(m.hierarchy_unit_paths -> h.id::text) p(path)
        WHERE m.triage_reason IS NULL AND h.workspace_id = w.id AND nlevel(p.path::ltree) = l.depth + 1
        ORDER BY l.depth DESC, h.id, p.path LIMIT 1
    ) beat ON true
    WHERE w.id = m.workspace_id AND m.forest_class IN ('inside', 'near_edge');
$$;
-- +goose StatementEnd

INSERT INTO alerts (workspace_id, detection_id, observed_at, hierarchy, queue, hierarchy_id, unit_path)
SELECT m.workspace_id, m.detection_id, m.observed_at, m.hierarchy, r.*
FROM detection_matches m CROSS JOIN LATERAL alert_route(m) r
ON CONFLICT DO NOTHING;

-- One path predicate for both alerts and detection tiles.
-- +goose StatementBegin
CREATE FUNCTION match_under_grant(paths jsonb, hierarchy_id uuid, unit_path ltree, depth smallint)
RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT depth = 0 OR EXISTS (
        SELECT 1 FROM jsonb_array_elements_text(paths -> hierarchy_id::text) p(path)
        WHERE p.path::ltree <@ unit_path
    );
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION match_under_grant(jsonb, uuid, ltree, smallint);
DROP FUNCTION alert_route(detection_matches);
DROP TABLE alerts;
ALTER TABLE workspaces DROP COLUMN review_near_edge;
