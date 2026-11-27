-- +goose Up
CREATE TABLE lsasaf_scans (
    workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
    slot timestamptz NOT NULL,
    first_listed_at timestamptz NOT NULL,
    validated_at timestamptz,
    PRIMARY KEY (workspace_id, slot)
);

CREATE INDEX lsasaf_scans_listed_idx ON lsasaf_scans (workspace_id, first_listed_at DESC);

CREATE TABLE lsasaf_cases (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
    line smallint NOT NULL,
    pixel smallint NOT NULL,
    first_slot timestamptz NOT NULL,
    last_slot timestamptz NOT NULL,
    alert_id bigint REFERENCES alerts ON DELETE SET NULL,
    confirmed_at timestamptz,
    UNIQUE (workspace_id, line, pixel, first_slot)
);
CREATE INDEX lsasaf_cases_alert_idx ON lsasaf_cases (alert_id) WHERE alert_id IS NOT NULL;
CREATE INDEX lsasaf_cases_recent_idx ON lsasaf_cases (workspace_id, last_slot DESC);

CREATE TABLE lsasaf_pixels (
    workspace_id uuid NOT NULL,
    detection_id bigint NOT NULL,
    slot timestamptz NOT NULL,
    case_id bigint REFERENCES lsasaf_cases ON DELETE SET NULL,
    footprint geometry(Polygon, 4326) NOT NULL,
    PRIMARY KEY (workspace_id, detection_id, slot),
    FOREIGN KEY (workspace_id, detection_id, slot) REFERENCES detection_matches ON DELETE CASCADE
);
CREATE INDEX lsasaf_pixels_case_idx ON lsasaf_pixels (case_id, slot DESC);
CREATE INDEX lsasaf_pixels_footprint_idx ON lsasaf_pixels USING gist (footprint);

-- Re-matching and previews use point geometry. Keep coarse evidence on its
-- footprint path, including when an operator publishes new boundaries.
CREATE OR REPLACE VIEW matchable_detections AS
SELECT d.id, d.observed_at, d.geom, d.workspace_id
FROM detections d
WHERE d.source <> 'lsasaf' AND NOT EXISTS (
    SELECT 1 FROM detection_links l WHERE (l.detection_id, l.observed_at) = (d.id, d.observed_at)
);

-- Coarse alerts always enter control-room triage. Rules that assign a lead
-- to a point detection require a precise observation.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION capture_new_alert_dispatch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RETURN capture_dispatch(NEW, NOT EXISTS (
        SELECT FROM lsasaf_pixels p
        WHERE (p.workspace_id, p.detection_id, p.slot) = (NEW.workspace_id, NEW.detection_id, NEW.observed_at)
    ));
END;
$$;
-- +goose StatementEnd

-- Coarse evidence does not participate in the general point-radius grouping rule.
ALTER FUNCTION group_alert(alerts) RENAME TO group_alert_before_lsasaf;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION group_alert(a alerts) RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE
    rule record;
    point geography;
    found bigint[];
    existing bigint;
    target incidents;
BEGIN
    SELECT incident_id INTO existing FROM incident_members WHERE alert_id = a.id;
    IF existing IS NOT NULL THEN
        RETURN existing;
    END IF;
    IF a.intake_incident_id IS NOT NULL THEN
        SELECT i.* INTO STRICT target FROM incidents i WHERE i.id=a.intake_incident_id
          AND i.workspace_id=a.workspace_id AND i.merged_into IS NULL;
        UPDATE incidents SET first_observed_at=LEAST(first_observed_at,a.observed_at),
            last_observed_at=GREATEST(last_observed_at,a.observed_at) WHERE id=target.id;
        INSERT INTO incident_members(alert_id,incident_id,first_incident_id) VALUES (a.id,target.id,target.id);
        RETURN target.id;
    END IF;
    SELECT r.* INTO rule FROM workspaces w CROSS JOIN LATERAL incident_rule(w.settings) r WHERE w.id = a.workspace_id;
    SELECT d.geom::geography INTO point FROM detections d WHERE (d.id, d.observed_at) = (a.detection_id, a.observed_at);

    SELECT array_agg(i.id ORDER BY i.first_observed_at, i.id) INTO found
    FROM incidents i
    JOIN workflow_instances wi ON wi.incident_id = i.id
    JOIN workflow_steps s ON (s.template_id, s.slug) = (wi.template_id, wi.current_step)
    WHERE i.workspace_id = a.workspace_id AND i.merged_into IS NULL AND NOT s.terminal
      AND NOT EXISTS (SELECT FROM lsasaf_pixels p
          WHERE (p.workspace_id,p.detection_id,p.slot) = (a.workspace_id,a.detection_id,a.observed_at))
      AND i.first_observed_at <= a.observed_at + rule.time_window
      AND i.last_observed_at >= a.observed_at - rule.time_window
      AND EXISTS (
          SELECT FROM incident_members m
          JOIN alerts o ON o.id = m.alert_id
          JOIN detections d ON (d.id, d.observed_at) = (o.detection_id, o.observed_at)
          WHERE m.incident_id = i.id
            AND o.observed_at BETWEEN a.observed_at - rule.time_window AND a.observed_at + rule.time_window
            AND d.source <> 'lsasaf'
            AND ST_DWithin(d.geom::geography, point, rule.distance_m)
      );

    IF found IS NULL THEN
        INSERT INTO incidents(workspace_id, first_observed_at, last_observed_at)
        VALUES (a.workspace_id, a.observed_at, a.observed_at) RETURNING * INTO target;
        PERFORM open_incident_workflow(target);
    ELSE
        target.id := found[1];
        -- An officer's grouping decision takes precedence over a later bridge.
        IF cardinality(found) > 1 AND EXISTS (
            SELECT FROM incident_membership_changes c
            WHERE c.from_incident_id=ANY(found) OR c.to_incident_id=ANY(found)
        ) THEN
            found := found[1:1];
        END IF;
        IF cardinality(found) > 1 THEN
            UPDATE incident_members SET incident_id = target.id WHERE incident_id = ANY (found[2:]);
            UPDATE incidents SET merged_into = target.id, merged_at = COALESCE(merged_at, now())
            WHERE id = ANY (found[2:]) OR merged_into = ANY (found[2:]);
            INSERT INTO audit_log(workspace_id, actor_kind, action, target_type, target_id, details)
            VALUES (a.workspace_id, 'system', 'incident.merge', 'incident', target.id::text,
                    jsonb_build_object('merged', to_jsonb(found[2:]), 'alertId', a.id));
        END IF;
        UPDATE incidents i SET first_observed_at = LEAST(i.first_observed_at, b.first_seen, a.observed_at),
                               last_observed_at = GREATEST(i.last_observed_at, b.last_seen, a.observed_at)
        FROM (SELECT min(first_observed_at) AS first_seen, max(last_observed_at) AS last_seen
              FROM incidents WHERE id = ANY (found)) b
        WHERE i.id = target.id;
    END IF;
    INSERT INTO incident_members(alert_id, incident_id, first_incident_id) VALUES (a.id, target.id, target.id);
    RETURN target.id;
END;
$$;
-- +goose StatementEnd

-- If a point falls into exactly one open footprint case, improve its location.
-- Reuse the alert and workflow if persistence has already made a duty alert.
-- +goose StatementBegin
CREATE FUNCTION lsasaf_confirm(m detection_matches, point geometry, queue text, hierarchy uuid, path ltree, target_incident bigint DEFAULT NULL)
RETURNS bigint LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    candidates bigint[];
    watch lsasaf_cases;
    a alerts;
BEGIN
    SELECT array_agg(c.id) INTO candidates
    FROM lsasaf_cases c
    WHERE c.workspace_id = m.workspace_id AND c.confirmed_at IS NULL
      AND (target_incident IS NULL OR EXISTS (
          SELECT FROM incident_members im WHERE im.alert_id = c.alert_id AND im.incident_id = target_incident
      ))
      AND c.last_slot >= m.observed_at - interval '2 hours'
      AND c.first_slot <= m.observed_at + interval '2 hours'
      AND EXISTS (
          SELECT FROM lsasaf_pixels p
          WHERE p.case_id = c.id AND p.slot BETWEEN m.observed_at - interval '2 hours'
                                               AND m.observed_at + interval '2 hours'
            AND ST_Covers(p.footprint, point)
      );
    IF cardinality(candidates) IS DISTINCT FROM 1 THEN
        RETURN NULL;
    END IF;

    SELECT * INTO watch FROM lsasaf_cases WHERE id = candidates[1] FOR UPDATE;
    IF watch.alert_id IS NULL THEN
        -- The normal insert supplies the alert ID after the statement.
        RETURN NULL;
    END IF;
    SELECT * INTO STRICT a FROM alerts WHERE id = watch.alert_id FOR UPDATE;
    a.detection_id := m.detection_id;
    a.observed_at := m.observed_at;
    IF a.assignment IS NULL THEN
        a.queue := queue;
        a.hierarchy_id := hierarchy;
        a.unit_path := path;
        a.hierarchy := m.hierarchy;
        a.unit_paths := m.unit_paths;
        a.hierarchy_unit_paths := m.hierarchy_unit_paths;
        a.forest_class := m.forest_class;
        a.triage_reason := m.triage_reason;
        a.layer_version_ids := m.layer_version_ids;
        a.boundary_snapshot := m.boundary_snapshot;
        a := capture_dispatch(a, true);
    END IF;
    UPDATE alerts SET detection_id = a.detection_id, observed_at = a.observed_at,
        queue = a.queue, hierarchy_id = a.hierarchy_id, unit_path = a.unit_path,
        hierarchy = a.hierarchy, unit_paths = a.unit_paths, hierarchy_unit_paths = a.hierarchy_unit_paths,
        forest_class = a.forest_class, triage_reason = a.triage_reason, layer_version_ids = a.layer_version_ids,
        boundary_snapshot = a.boundary_snapshot, dispatch_lead = a.dispatch_lead, dispatch_support = a.dispatch_support
    WHERE id = a.id;
    UPDATE lsasaf_cases SET confirmed_at = clock_timestamp() WHERE id = watch.id;
    UPDATE incidents i SET first_observed_at = LEAST(i.first_observed_at, a.observed_at),
        last_observed_at = GREATEST(i.last_observed_at, a.observed_at)
    FROM incident_members im WHERE im.alert_id = a.id AND im.incident_id = i.id;
    -- Existing delivery rows are unique by recipient and channel. Newly
    -- responsible officers can be notified after the point supplies a route.
    IF EXISTS (SELECT FROM workflow_instances i JOIN workflow_steps s
               ON (s.template_id,s.slug) = (i.template_id,i.current_step)
               WHERE i.alert_id = a.id AND NOT s.terminal) THEN
        PERFORM enqueue_alert_notifications(a, 'alert_new', a.id);
    END IF;
    RETURN a.id;
END;
$$;
-- +goose StatementEnd

-- Link watches that had no duty alert when an independent alert arrived.
-- This also handles the independent observation arriving before the ListProduct.
-- +goose StatementBegin
CREATE FUNCTION lsasaf_link_existing(workspace uuid, start_at timestamptz, end_at timestamptz)
RETURNS void LANGUAGE sql VOLATILE AS $$
    WITH candidates AS (
        SELECT DISTINCT c.id AS case_id, im.incident_id, a.id AS alert_id, a.observed_at,
               abs(extract(epoch FROM a.observed_at - p.slot)) AS difference
        FROM lsasaf_cases c
        JOIN lsasaf_pixels p ON p.case_id = c.id
        JOIN alerts a ON a.workspace_id = c.workspace_id AND a.observed_at
            BETWEEN p.slot - interval '2 hours' AND p.slot + interval '2 hours'
        JOIN incident_members im ON im.alert_id = a.id
        JOIN workflow_instances wi ON wi.incident_id = im.incident_id
        JOIN workflow_steps ws ON (ws.template_id,ws.slug) = (wi.template_id,wi.current_step) AND NOT ws.terminal
        JOIN detections d ON (d.id, d.observed_at) = (a.detection_id, a.observed_at)
        WHERE c.workspace_id = workspace AND c.alert_id IS NULL AND c.confirmed_at IS NULL
          AND c.last_slot >= start_at - interval '2 hours' AND c.first_slot <= end_at + interval '2 hours'
          AND p.slot BETWEEN start_at - interval '2 hours' AND end_at + interval '2 hours'
          AND d.source IN ('firms', 'noaa_nodd', 'manual') AND ST_Covers(p.footprint, d.geom)
    ),
    ambiguous_cases AS (
        SELECT case_id FROM candidates GROUP BY case_id HAVING count(DISTINCT incident_id) > 1
    ),
    ambiguous_incidents AS (
        SELECT incident_id FROM candidates GROUP BY incident_id HAVING count(DISTINCT case_id) > 1
    ),
    chosen AS (
        SELECT DISTINCT ON (case_id) case_id, alert_id
        FROM candidates
        WHERE case_id NOT IN (SELECT case_id FROM ambiguous_cases)
          AND incident_id NOT IN (SELECT incident_id FROM ambiguous_incidents)
        ORDER BY case_id, difference, observed_at, alert_id
    )
    UPDATE lsasaf_cases c SET alert_id = chosen.alert_id, confirmed_at = clock_timestamp()
    FROM chosen WHERE c.id = chosen.case_id;
$$;
-- +goose StatementEnd

-- Run after group_incidents, which makes the incident membership visible.
-- +goose StatementBegin
CREATE FUNCTION link_new_alerts_to_lsasaf() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM lsasaf_link_existing(a.workspace_id, a.first_at, a.last_at)
    FROM (
        SELECT a.workspace_id, min(a.observed_at) AS first_at, max(a.observed_at) AS last_at
        FROM added a JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
        WHERE d.source IN ('firms', 'noaa_nodd', 'manual')
        GROUP BY a.workspace_id
    ) a;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER link_lsasaf AFTER INSERT ON alerts REFERENCING NEW TABLE AS added
    FOR EACH STATEMENT EXECUTE FUNCTION link_new_alerts_to_lsasaf();

-- Even an installation with custom triage push rules must wait for a point
-- before a coarse duty alert can reach a field phone.
-- +goose StatementBegin
CREATE FUNCTION lsasaf_wait_for_precise_push() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT FROM alerts a JOIN lsasaf_pixels p
           ON (p.workspace_id,p.detection_id,p.slot) = (a.workspace_id,a.detection_id,a.observed_at)
           WHERE a.id = NEW.alert_id) THEN
        RETURN NULL;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER lsasaf_precise_push BEFORE INSERT ON notifications FOR EACH ROW
    WHEN (NEW.channel = 'push' AND NEW.alert_id IS NOT NULL)
    EXECUTE FUNCTION lsasaf_wait_for_precise_push();

-- +goose Down
DROP TRIGGER link_lsasaf ON alerts;
DROP FUNCTION link_new_alerts_to_lsasaf();
DROP TRIGGER lsasaf_precise_push ON notifications;
DROP FUNCTION lsasaf_wait_for_precise_push();
DROP FUNCTION lsasaf_link_existing(uuid, timestamptz, timestamptz);
DROP FUNCTION lsasaf_confirm(detection_matches, geometry, text, uuid, ltree, bigint);
DROP FUNCTION group_alert(alerts);
ALTER FUNCTION group_alert_before_lsasaf(alerts) RENAME TO group_alert;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION capture_new_alert_dispatch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RETURN capture_dispatch(NEW, true);
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE VIEW matchable_detections AS
SELECT d.id, d.observed_at, d.geom, d.workspace_id
FROM detections d
WHERE NOT EXISTS (
    SELECT 1 FROM detection_links l WHERE (l.detection_id, l.observed_at) = (d.id, d.observed_at)
);

DELETE FROM alerts a USING lsasaf_pixels p
WHERE (a.workspace_id, a.detection_id, a.observed_at) = (p.workspace_id, p.detection_id, p.slot);
DROP TABLE lsasaf_pixels, lsasaf_cases, lsasaf_scans;
DELETE FROM detections WHERE source = 'lsasaf';
DELETE FROM ingest_runs WHERE source = 'lsasaf';
