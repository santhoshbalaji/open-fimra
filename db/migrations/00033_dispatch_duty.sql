-- +goose Up
CREATE TABLE dispatch_rules (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
    priority integer NOT NULL,
    effective_from timestamptz NOT NULL,
    effective_to timestamptz,
    overlay_layer text,
    overlay_code text,
    overlay_zone text CHECK (overlay_zone IN ('core', 'buffer')),
    lead_hierarchy_id uuid NOT NULL,
    lead_path ltree NOT NULL,
    UNIQUE (workspace_id, id),
    FOREIGN KEY (workspace_id, lead_hierarchy_id) REFERENCES hierarchies (workspace_id, id) ON DELETE CASCADE,
    CHECK (effective_to IS NULL OR effective_from < effective_to),
    CHECK (overlay_layer IS NOT NULL OR (overlay_code IS NULL AND overlay_zone IS NULL))
);
CREATE INDEX dispatch_rules_workspace_idx ON dispatch_rules (workspace_id, priority DESC);

CREATE TABLE dispatch_rule_support (
    workspace_id uuid NOT NULL,
    rule_id bigint NOT NULL,
    hierarchy_id uuid NOT NULL,
    unit_path ltree NOT NULL,
    PRIMARY KEY (rule_id, hierarchy_id, unit_path),
    FOREIGN KEY (workspace_id, rule_id) REFERENCES dispatch_rules (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, hierarchy_id) REFERENCES hierarchies (workspace_id, id) ON DELETE CASCADE
);

ALTER TABLE alerts ADD COLUMN original_dispatch jsonb,
    ADD COLUMN dispatch_lead jsonb,
    ADD COLUMN dispatch_support jsonb NOT NULL DEFAULT '[]';

CREATE TABLE alert_support_requests (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    alert_id bigint NOT NULL REFERENCES alerts ON DELETE CASCADE,
    target jsonb NOT NULL,
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 500),
    requested_by uuid NOT NULL REFERENCES users,
    requested_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX alert_support_requests_target_idx
    ON alert_support_requests (alert_id, (target->>'hierarchyId'), (target->>'path'));

CREATE TABLE duty_shifts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
    kind text NOT NULL CHECK (kind IN ('control_room', 'squad', 'watcher')),
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    cancelled_at timestamptz,
    UNIQUE (workspace_id, id),
    CHECK (starts_at < ends_at)
);
CREATE INDEX duty_shifts_workspace_idx ON duty_shifts (workspace_id, starts_at);
CREATE TABLE duty_members (
    shift_id bigint NOT NULL REFERENCES duty_shifts ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users ON DELETE CASCADE,
    PRIMARY KEY (shift_id, user_id)
);
CREATE INDEX duty_members_user_idx ON duty_members (user_id, shift_id);
CREATE TABLE duty_service_units (
    workspace_id uuid NOT NULL,
    shift_id bigint NOT NULL,
    hierarchy_id uuid NOT NULL,
    unit_path ltree NOT NULL,
    PRIMARY KEY (shift_id, hierarchy_id, unit_path),
    FOREIGN KEY (workspace_id, shift_id) REFERENCES duty_shifts (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, hierarchy_id) REFERENCES hierarchies (workspace_id, id) ON DELETE CASCADE
);

CREATE VIEW operational_role_assignments AS
SELECT user_id, workspace_id, hierarchy_id, unit_path, role_slug FROM role_assignments
UNION
SELECT m.user_id, s.workspace_id, coverage.hierarchy_id, coverage.unit_path,
       CASE WHEN s.kind = 'control_room' AND u.depth <= division.depth
            THEN 'division_officer' ELSE 'field_staff' END
FROM duty_members m
JOIN duty_shifts s ON s.id = m.shift_id
JOIN duty_service_units coverage ON coverage.shift_id = s.id
JOIN units u ON (u.hierarchy_id, u.current_path) = (coverage.hierarchy_id, coverage.unit_path)
LEFT JOIN hierarchy_levels division ON division.hierarchy_id = coverage.hierarchy_id AND division.slug = 'division'
WHERE s.cancelled_at IS NULL AND s.starts_at <= statement_timestamp() AND statement_timestamp() < s.ends_at
  AND u.depth > 0
  AND EXISTS (SELECT FROM role_assignments home WHERE home.user_id = m.user_id AND home.workspace_id = s.workspace_id);

-- Device administration and template management stay with substantive roles.
CREATE VIEW permanent_alert_grants AS
SELECT a.workspace_id, w.slug, a.hierarchy_id, a.unit_path, a.role_slug, u.depth,
       caller.oidc_subject, caller.id AS user_id,
       u.depth=0 OR COALESCE(u.depth<=division.depth,false) AS can_review
FROM role_assignments a JOIN users caller ON caller.id=a.user_id
JOIN workspaces w ON w.id=a.workspace_id
JOIN units u ON u.hierarchy_id=a.hierarchy_id AND u.current_path=a.unit_path
LEFT JOIN hierarchy_levels division ON division.hierarchy_id=u.hierarchy_id AND division.slug='division';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION form_manager(subject text, form bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT FROM forms f JOIN permanent_alert_grants g ON g.workspace_id = f.workspace_id AND g.oidc_subject = subject
        WHERE f.id = form
          AND ((g.role_slug = 'state_admin' AND g.depth = 0)
               OR (f.level = 'division' AND g.can_review AND g.role_slug IN ('state_admin', 'division_officer')
                   AND g.hierarchy_id = f.owner_hierarchy_id AND f.owner_unit_path <@ g.unit_path)))
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION manageable_devices(subject text, workspace_slug text)
RETURNS TABLE(device_id uuid, workspace_id uuid) LANGUAGE sql STABLE AS $$
    SELECT DISTINCT d.id, admin.workspace_id
    FROM permanent_alert_grants admin
    JOIN role_assignments owner ON owner.workspace_id = admin.workspace_id
      AND ((admin.role_slug = 'state_admin' AND admin.depth = 0)
        OR (owner.hierarchy_id = admin.hierarchy_id AND owner.unit_path <@ admin.unit_path))
    JOIN devices d ON d.user_id = owner.user_id
    WHERE admin.oidc_subject = subject AND admin.slug = workspace_slug
      AND (admin.role_slug = 'state_admin' OR (admin.role_slug = 'division_officer' AND admin.can_review));
$$;
-- +goose StatementEnd

-- Permanent roles retain their existing review behavior. A duty field grant
-- alone does not grant review, even when a service area covers a division.
CREATE OR REPLACE VIEW alert_grants AS
SELECT a.workspace_id, w.slug, a.hierarchy_id, a.unit_path, a.role_slug, u.depth,
       caller.oidc_subject, caller.id AS user_id,
       u.depth=0 OR (COALESCE(u.depth<=division.depth,false) AND
           (a.role_slug = 'division_officer' OR EXISTS (SELECT FROM role_assignments home
               WHERE (home.user_id, home.workspace_id, home.hierarchy_id, home.unit_path, home.role_slug) =
                     (a.user_id, a.workspace_id, a.hierarchy_id, a.unit_path, a.role_slug)))) AS can_review
FROM operational_role_assignments a JOIN users caller ON caller.id=a.user_id
JOIN workspaces w ON w.id=a.workspace_id
JOIN units u ON u.hierarchy_id=a.hierarchy_id AND u.current_path=a.unit_path
LEFT JOIN hierarchy_levels division ON division.hierarchy_id=u.hierarchy_id AND division.slug='division';

-- +goose StatementBegin
CREATE FUNCTION dispatch_unit(u units) RETURNS jsonb LANGUAGE sql IMMUTABLE STRICT AS $$
    SELECT jsonb_build_object('id', u.id::text, 'name', u.name, 'hierarchyId', u.hierarchy_id,
                              'path', u.path::text, 'boundaryVersionId', u.layer_version_id);
$$;
-- +goose StatementEnd

-- Keep the selected units and any matched paths beneath them. A unit
-- requested for support can see the alert even when its boundary is elsewhere.
-- +goose StatementBegin
CREATE FUNCTION dispatch_paths(lead jsonb, support jsonb, matched jsonb)
RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
    WITH selected AS (
        SELECT lead AS unit WHERE lead IS NOT NULL
        UNION ALL SELECT value FROM jsonb_array_elements(support)
    ), paths AS (
        SELECT unit->>'hierarchyId' AS hierarchy, unit->>'path' AS path FROM selected
        UNION
        SELECT unit->>'hierarchyId', p.path FROM selected
        CROSS JOIN LATERAL jsonb_array_elements_text(matched -> (unit->>'hierarchyId')) p(path)
        WHERE p.path::ltree <@ (unit->>'path')::ltree
    )
    SELECT COALESCE(jsonb_object_agg(hierarchy, paths), '{}')
    FROM (SELECT hierarchy, jsonb_agg(path ORDER BY path) AS paths FROM paths GROUP BY hierarchy) grouped;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION dispatch_flat_paths(paths jsonb) RETURNS ltree[] LANGUAGE sql IMMUTABLE AS $$
    SELECT ARRAY(SELECT DISTINCT p.path::ltree FROM jsonb_each(paths) h
                 CROSS JOIN LATERAL jsonb_array_elements_text(h.value) p(path) ORDER BY 1);
$$;
-- +goose StatementEnd

-- Use the unit versions available when the detection was classified. A
-- replay can create a newly eligible alert with current routing paths.
-- +goose StatementBegin
CREATE FUNCTION dispatch_matched_units(a alerts) RETURNS SETOF units LANGUAGE sql STABLE AS $$
    SELECT DISTINCT ON (u.hierarchy_id, u.path) u.*
    FROM jsonb_each(a.hierarchy_unit_paths) scope
    CROSS JOIN LATERAL jsonb_array_elements_text(scope.value) p(path)
    JOIN units u ON u.hierarchy_id = scope.key::uuid AND u.path = p.path::ltree
    WHERE u.depth > 0 AND (u.layer_version_id = ANY(a.layer_version_ids)
        OR (u.valid_from <= a.observed_at AND (u.valid_to IS NULL OR a.observed_at < u.valid_to)))
    ORDER BY u.hierarchy_id, u.path, (u.layer_version_id = ANY(a.layer_version_ids)) DESC NULLS LAST, u.valid_from DESC;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION capture_dispatch(a alerts, apply_rules boolean) RETURNS alerts LANGUAGE plpgsql STABLE AS $$
DECLARE
    matched jsonb := a.hierarchy_unit_paths;
    candidates jsonb;
    contenders jsonb;
    winning dispatch_rules;
    rule_snapshot jsonb;
    overlay_versions jsonb;
BEGIN
    WITH units AS MATERIALIZED (SELECT * FROM dispatch_matched_units(a))
    SELECT COALESCE(jsonb_agg(dispatch_unit(u) ORDER BY u.hierarchy_id, u.path), '[]'),
           (SELECT dispatch_unit(lead) FROM units lead
            WHERE (lead.hierarchy_id, lead.path) = (a.hierarchy_id, a.unit_path))
    INTO candidates, a.dispatch_lead
    FROM units u;

    IF a.unit_path IS NOT NULL AND a.dispatch_lead IS NULL THEN
        SELECT dispatch_unit(u) INTO a.dispatch_lead FROM units u
        WHERE (u.hierarchy_id, u.path) = (a.hierarchy_id, a.unit_path)
        ORDER BY (u.valid_to IS NULL) DESC, u.valid_from DESC LIMIT 1;
    END IF;

    IF apply_rules THEN
        WITH eligible AS (
            SELECT r.*, max(r.priority) OVER () AS highest
            FROM dispatch_rules r
            WHERE r.workspace_id = a.workspace_id AND r.effective_from <= a.observed_at
              AND (r.effective_to IS NULL OR a.observed_at < r.effective_to)
              AND CASE WHEN r.overlay_layer IS NULL THEN
                    match_under_grant(matched, r.lead_hierarchy_id, r.lead_path, 1::smallint)
                  ELSE EXISTS (
                    SELECT FROM jsonb_array_elements(COALESCE(a.boundary_snapshot->'overlays', '[]')) o
                    WHERE o->>'layer' = r.overlay_layer AND (r.overlay_code IS NULL OR o->>'code' = r.overlay_code)
                      AND (r.overlay_zone IS NULL OR o->>'zone' = r.overlay_zone)) END
        )
        SELECT COALESCE(jsonb_agg(id::text ORDER BY id), '[]') INTO contenders FROM eligible WHERE priority = highest;

        IF jsonb_array_length(contenders) > 0 THEN
            a.dispatch_lead := NULL;
            IF jsonb_array_length(contenders) = 1 THEN
                SELECT * INTO winning FROM dispatch_rules WHERE id = (contenders->>0)::bigint;
                SELECT COALESCE(jsonb_agg(DISTINCT o->'versionId'), '[]') INTO overlay_versions
                FROM jsonb_array_elements(COALESCE(a.boundary_snapshot->'overlays', '[]')) o
                WHERE o->>'layer' = winning.overlay_layer AND (winning.overlay_code IS NULL OR o->>'code' = winning.overlay_code)
                  AND (winning.overlay_zone IS NULL OR o->>'zone' = winning.overlay_zone);
                rule_snapshot := jsonb_build_object('id', winning.id::text, 'name', winning.name,
                    'priority', winning.priority, 'effectiveFrom', winning.effective_from,
                    'effectiveTo', winning.effective_to, 'boundaryVersionIds', overlay_versions);
                SELECT dispatch_unit(u) INTO a.dispatch_lead FROM dispatch_matched_units(a) u
                WHERE (u.hierarchy_id, u.path) = (winning.lead_hierarchy_id, winning.lead_path);
            END IF;
            IF a.dispatch_lead IS NULL THEN
                a.queue := 'triage';
                a.hierarchy_id := NULL;
                a.unit_path := NULL;
                a.triage_reason := CASE WHEN winning.id IS NULL THEN 'dispatch_rule_conflict' ELSE 'dispatch_unit_unavailable' END;
            ELSE
                SELECT COALESCE(jsonb_agg(dispatch_unit(u) ORDER BY u.hierarchy_id, u.path), '[]')
                INTO a.dispatch_support
                FROM dispatch_rule_support s JOIN LATERAL (
                    SELECT u.* FROM units u WHERE (u.hierarchy_id, u.path) = (s.hierarchy_id, s.unit_path)
                      AND u.depth > 0 AND u.valid_from <= a.observed_at AND (u.valid_to IS NULL OR a.observed_at < u.valid_to)
                    ORDER BY u.valid_from DESC LIMIT 1
                ) u ON true
                WHERE s.rule_id = winning.id
                  AND (u.hierarchy_id, u.path) <> (winning.lead_hierarchy_id, winning.lead_path);
                a.hierarchy_id := winning.lead_hierarchy_id;
                a.unit_path := winning.lead_path;
                a.queue := CASE WHEN a.forest_class = 'near_edge' AND EXISTS (
                    SELECT FROM workspaces w WHERE w.id = a.workspace_id AND w.review_near_edge
                ) THEN 'edge_review' ELSE 'unit' END;
                a.triage_reason := NULL;
                a.hierarchy_unit_paths := dispatch_paths(a.dispatch_lead, a.dispatch_support, matched);
                a.unit_paths := dispatch_flat_paths(a.hierarchy_unit_paths);
            END IF;
        END IF;
    END IF;
    a.original_dispatch := jsonb_build_object('queue', a.queue, 'hierarchyId', a.hierarchy_id, 'unitPath', a.unit_path,
        'lead', a.dispatch_lead, 'supporting', a.dispatch_support, 'rule', rule_snapshot,
        'ruleCandidates', COALESCE(contenders, '[]'), 'candidates', candidates, 'candidatePaths', matched,
        'triageReason', a.triage_reason,
        'boundaryVersionIds', COALESCE(a.boundary_snapshot->'layerVersionIds', to_jsonb(a.layer_version_ids)));
    RETURN a;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION capture_new_alert_dispatch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RETURN capture_dispatch(NEW, true);
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER capture_dispatch BEFORE INSERT ON alerts FOR EACH ROW EXECUTE FUNCTION capture_new_alert_dispatch();

-- +goose StatementBegin
CREATE FUNCTION can_dispatch_alert(a alerts, g alert_grants) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
    SELECT g.workspace_id = a.workspace_id AND g.can_review AND g.role_slug IN ('state_admin', 'division_officer')
       AND match_under_grant(
           CASE WHEN a.queue = 'unit' THEN dispatch_paths(a.dispatch_lead, '[]', a.original_dispatch->'candidatePaths')
                ELSE a.hierarchy_unit_paths END,
           g.hierarchy_id, g.unit_path, g.depth);
$$;
-- +goose StatementEnd

-- A detection also follows a lead or support assignment made under dispatch.
-- Unchanged beat routing still uses the detection's own current match.
-- +goose StatementBegin
CREATE FUNCTION dispatch_detection_visible(a alerts, can_review boolean, hierarchy uuid, path ltree, depth smallint)
RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (a.original_dispatch->>'rule' IS NOT NULL OR a.assignment->>'action' = 'transfer'
            OR jsonb_array_length(a.dispatch_support) > 0)
        AND alert_path_visible(a.queue, a.hierarchy_unit_paths, can_review, hierarchy, path, depth);
$$;
-- +goose StatementEnd

-- Files prepared through temporary coverage remain available only while
-- the grants used to prepare them are still available.
ALTER TABLE exports ADD COLUMN duty_grants text[] NOT NULL DEFAULT '{}';
-- +goose StatementBegin
CREATE FUNCTION dispatch_grant_keys(workspace uuid, subject text, duty_only boolean)
RETURNS text[] LANGUAGE sql STABLE AS $$
    SELECT ARRAY(
        SELECT concat(g.hierarchy_id, ':', g.unit_path, ':', g.role_slug, ':', g.can_review)
        FROM alert_grants g
        WHERE g.workspace_id = workspace AND g.oidc_subject = subject
          AND (NOT duty_only OR NOT EXISTS (SELECT FROM role_assignments a
              WHERE (a.user_id, a.workspace_id, a.hierarchy_id, a.unit_path, a.role_slug) =
                    (g.user_id, g.workspace_id, g.hierarchy_id, g.unit_path, g.role_slug)))
    );
$$;
-- +goose StatementEnd

-- Existing manual assignments keep their current lead. Recover their original
-- queue and paths from the assignment audit rather than the mutable match.
WITH historical AS (
    SELECT a.id, capture_dispatch(a, false) AS current_state,
           CASE WHEN log.id IS NOT NULL THEN capture_dispatch(
               jsonb_populate_record(a, jsonb_build_object(
                   'queue', log.details #> '{previous,queue}',
                   'hierarchy_id', log.details #> '{previous,hierarchyId}',
                   'unit_path', log.details #> '{previous,unitPath}',
                   'hierarchy_unit_paths', log.details #> '{previous,hierarchyUnitPaths}')), false)
           END AS original_state
    FROM alerts a LEFT JOIN audit_log log ON log.id = (a.assignment->>'auditId')::bigint
)
UPDATE alerts a SET dispatch_lead = COALESCE(
        (SELECT dispatch_unit(u) FROM units u WHERE u.id = (a.assignment->>'unitId')::bigint),
        (h.current_state).dispatch_lead),
    original_dispatch = COALESCE((h.original_state).original_dispatch, (h.current_state).original_dispatch)
FROM historical h WHERE h.id = a.id;

-- +goose Down
DROP TRIGGER capture_dispatch ON alerts;
DROP FUNCTION can_dispatch_alert(alerts, alert_grants), dispatch_detection_visible(alerts, boolean, uuid, ltree, smallint),
    dispatch_grant_keys(uuid, text, boolean);
ALTER TABLE exports DROP COLUMN duty_grants;

-- Preserve the current lead as the assignment understood by the earlier
-- schema. Remove support-only paths from alerts touched by dispatch.
WITH previous_schema AS (
    SELECT a.id,
        CASE WHEN a.assignment IS NOT NULL THEN dispatch_paths(a.dispatch_lead, '[]', '{}')
             WHEN a.original_dispatch->>'rule' IS NOT NULL AND a.dispatch_lead IS NOT NULL THEN
                dispatch_paths(a.dispatch_lead, '[]', a.original_dispatch->'candidatePaths')
             ELSE a.original_dispatch->'candidatePaths' END AS paths
    FROM alerts a WHERE jsonb_array_length(a.dispatch_support) > 0
        OR a.assignment->>'action' IS NOT NULL
)
UPDATE alerts a SET hierarchy_unit_paths = p.paths, unit_paths = dispatch_flat_paths(p.paths)
FROM previous_schema p WHERE a.id = p.id;
DROP FUNCTION capture_new_alert_dispatch(), capture_dispatch(alerts, boolean), dispatch_matched_units(alerts);
DROP FUNCTION dispatch_flat_paths(jsonb), dispatch_paths(jsonb, jsonb, jsonb), dispatch_unit(units);
ALTER TABLE alerts DROP COLUMN original_dispatch, DROP COLUMN dispatch_lead, DROP COLUMN dispatch_support;
DROP TABLE alert_support_requests;

CREATE OR REPLACE VIEW alert_grants AS
SELECT a.workspace_id, w.slug, a.hierarchy_id, a.unit_path, a.role_slug, u.depth,
       caller.oidc_subject, caller.id AS user_id,
       u.depth=0 OR COALESCE(u.depth<=division.depth,false) AS can_review
FROM role_assignments a JOIN users caller ON caller.id=a.user_id
JOIN workspaces w ON w.id=a.workspace_id
JOIN units u ON u.hierarchy_id=a.hierarchy_id AND u.current_path=a.unit_path
LEFT JOIN hierarchy_levels division ON division.hierarchy_id=u.hierarchy_id AND division.slug='division';
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION form_manager(subject text, form bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT FROM forms f JOIN alert_grants g ON g.workspace_id = f.workspace_id AND g.oidc_subject = subject
        WHERE f.id = form
          AND ((g.role_slug = 'state_admin' AND g.depth = 0)
               OR (f.level = 'division' AND g.can_review AND g.role_slug IN ('state_admin', 'division_officer')
                   AND g.hierarchy_id = f.owner_hierarchy_id AND f.owner_unit_path <@ g.unit_path)))
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION manageable_devices(subject text, workspace_slug text)
RETURNS TABLE(device_id uuid, workspace_id uuid) LANGUAGE sql STABLE AS $$
    SELECT DISTINCT d.id, admin.workspace_id
    FROM alert_grants admin
    JOIN role_assignments owner ON owner.workspace_id = admin.workspace_id
      AND ((admin.role_slug = 'state_admin' AND admin.depth = 0)
        OR (owner.hierarchy_id = admin.hierarchy_id AND owner.unit_path <@ admin.unit_path))
    JOIN devices d ON d.user_id = owner.user_id
    WHERE admin.oidc_subject = subject AND admin.slug = workspace_slug
      AND (admin.role_slug = 'state_admin' OR (admin.role_slug = 'division_officer' AND admin.can_review));
$$;
-- +goose StatementEnd
DROP VIEW operational_role_assignments, permanent_alert_grants;
DROP TABLE duty_service_units, duty_members, duty_shifts;
DROP TABLE dispatch_rule_support, dispatch_rules;
