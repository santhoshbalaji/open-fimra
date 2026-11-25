-- Incidents (PLAN.md section 5.3, step 6). A new alert joins an open incident
-- when it lies within the workspace's distance of any member and within its
-- time window of that member. An alert that bridges several open incidents
-- merges them. Each incident has its own workflow in the shared workflow
-- tables, and its member alerts keep theirs.

-- +goose Up
CREATE TABLE incidents (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- The earliest and latest member observations. The earliest decides
    -- which incident continues after a merge.
    first_observed_at timestamptz NOT NULL,
    last_observed_at timestamptz NOT NULL,
    -- A merged incident points to the incident that continues. Its own
    -- workflow history stays, but it takes no new steps or members.
    merged_into bigint REFERENCES incidents,
    merged_at timestamptz,
    CHECK ((merged_into IS NULL) = (merged_at IS NULL)),
    CHECK (merged_into <> id),
    CHECK (first_observed_at <= last_observed_at)
);
CREATE INDEX incidents_recent_idx ON incidents (workspace_id, last_observed_at DESC, id DESC)
    WHERE merged_into IS NULL;
CREATE INDEX incidents_merged_idx ON incidents (merged_into) WHERE merged_into IS NOT NULL;

CREATE TABLE incident_members (
    alert_id bigint PRIMARY KEY REFERENCES alerts ON DELETE CASCADE,
    incident_id bigint NOT NULL REFERENCES incidents,
    -- The incident the alert first joined, kept when incidents merge.
    first_incident_id bigint NOT NULL REFERENCES incidents,
    joined_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX incident_members_incident_idx ON incident_members (incident_id, alert_id);

-- One workflow instance and step log for alerts and incidents alike.
ALTER TABLE workflow_instances DROP CONSTRAINT workflow_instances_pkey;
ALTER TABLE workflow_instances
    ALTER COLUMN alert_id DROP NOT NULL,
    ADD COLUMN id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ADD COLUMN incident_id bigint REFERENCES incidents,
    ADD CONSTRAINT workflow_instances_alert_key UNIQUE (alert_id),
    ADD CONSTRAINT workflow_instances_incident_key UNIQUE (incident_id),
    ADD CONSTRAINT workflow_instances_incident_template_key UNIQUE (incident_id, template_id),
    ADD CONSTRAINT workflow_instances_one_subject CHECK (num_nonnulls(alert_id, incident_id) = 1);

ALTER TABLE step_log
    ALTER COLUMN alert_id DROP NOT NULL,
    ADD COLUMN incident_id bigint,
    -- On an alert entry: the incident step that moved this alert with it.
    ADD COLUMN incident_step_id bigint REFERENCES step_log,
    ADD CONSTRAINT step_log_incident_fkey FOREIGN KEY (incident_id, template_id)
        REFERENCES workflow_instances (incident_id, template_id),
    ADD CONSTRAINT step_log_incident_client_key UNIQUE (incident_id, client_id),
    ADD CONSTRAINT step_log_one_subject CHECK (num_nonnulls(alert_id, incident_id) = 1),
    ADD CONSTRAINT step_log_incident_step_on_alert CHECK (incident_step_id IS NULL OR alert_id IS NOT NULL);
CREATE INDEX step_log_incident_history_idx ON step_log (incident_id, id) WHERE incident_id IS NOT NULL;
CREATE INDEX step_log_incident_step_idx ON step_log (incident_step_id) WHERE incident_step_id IS NOT NULL;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_workflow_step() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.incident_id IS NOT NULL THEN
        INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,details)
        SELECT i.workspace_id, CASE WHEN NEW.user_id IS NULL THEN 'system' ELSE 'user' END,
               u.oidc_subject, 'incident.step','incident',i.id::text,
               jsonb_build_object('stepLogId',NEW.id,'from',NEW.from_step,'to',NEW.to_step)
        FROM incidents i LEFT JOIN users u ON u.id=NEW.user_id WHERE i.id=NEW.incident_id;
    ELSE
        INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,details)
        SELECT a.workspace_id, CASE WHEN NEW.user_id IS NULL THEN 'system' ELSE 'user' END,
               u.oidc_subject, 'alert.step','alert',a.id::text,
               jsonb_strip_nulls(jsonb_build_object('stepLogId',NEW.id,'from',NEW.from_step,'to',NEW.to_step,
                   'incidentStepId',NEW.incident_step_id))
        FROM alerts a LEFT JOIN users u ON u.id=NEW.user_id WHERE a.id=NEW.alert_id;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- The grouping rule. Values outside the accepted range fall back to the
-- defaults, so a typing mistake cannot switch grouping off.
-- +goose StatementBegin
CREATE FUNCTION incident_rule(settings jsonb, OUT distance_m float8, OUT time_window interval)
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE WHEN jsonb_typeof(settings #> '{incidents,distanceMetres}') = 'number'
                 AND (settings #>> '{incidents,distanceMetres}')::float8 BETWEEN 100 AND 20000
                THEN (settings #>> '{incidents,distanceMetres}')::float8 ELSE 1500 END,
           make_interval(hours => CASE WHEN jsonb_typeof(settings #> '{incidents,windowHours}') = 'number'
                 AND (settings #>> '{incidents,windowHours}')::numeric BETWEEN 1 AND 168
                 AND (settings #>> '{incidents,windowHours}')::numeric % 1 = 0
                THEN (settings #>> '{incidents,windowHours}')::integer ELSE 24 END);
$$;
-- +goose StatementEnd

-- An incident opens at New like an alert. The opening entry has no location,
-- because the first member may lie in a unit that some viewers cannot see.
-- +goose StatementBegin
CREATE FUNCTION open_incident_workflow(incident incidents) RETURNS void LANGUAGE sql AS $$
    WITH opened AS (
        INSERT INTO workflow_instances(incident_id,template_id,current_step,entered_at)
        VALUES (incident.id, (SELECT t.id FROM workflow_templates t
                WHERE t.workspace_id IS NULL OR t.workspace_id=incident.workspace_id
                ORDER BY t.workspace_id NULLS LAST LIMIT 1), 'new', incident.created_at)
        RETURNING *
    )
    INSERT INTO step_log(incident_id,template_id,to_step,occurred_at)
    SELECT incident_id,template_id,current_step,entered_at FROM opened;
$$;
-- +goose StatementEnd

-- Puts one new alert into an incident and returns its ID. Open incidents are
-- those not merged and not at a final step. Several matches merge into the
-- one with the earliest observation, then the lowest ID.
-- +goose StatementBegin
CREATE FUNCTION group_alert(a alerts) RETURNS bigint LANGUAGE plpgsql AS $$
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
    SELECT r.* INTO rule FROM workspaces w CROSS JOIN LATERAL incident_rule(w.settings) r WHERE w.id = a.workspace_id;
    SELECT d.geom::geography INTO point FROM detections d WHERE (d.id, d.observed_at) = (a.detection_id, a.observed_at);

    SELECT array_agg(i.id ORDER BY i.first_observed_at, i.id) INTO found
    FROM incidents i
    JOIN workflow_instances wi ON wi.incident_id = i.id
    JOIN workflow_steps s ON (s.template_id, s.slug) = (wi.template_id, wi.current_step)
    WHERE i.workspace_id = a.workspace_id AND i.merged_into IS NULL AND NOT s.terminal
      AND i.first_observed_at <= a.observed_at + rule.time_window
      AND i.last_observed_at >= a.observed_at - rule.time_window
      AND EXISTS (
          SELECT FROM incident_members m
          JOIN alerts o ON o.id = m.alert_id
          JOIN detections d ON (d.id, d.observed_at) = (o.detection_id, o.observed_at)
          WHERE m.incident_id = i.id
            AND o.observed_at BETWEEN a.observed_at - rule.time_window AND a.observed_at + rule.time_window
            AND ST_DWithin(d.geom::geography, point, rule.distance_m)
      );

    IF found IS NULL THEN
        INSERT INTO incidents(workspace_id, first_observed_at, last_observed_at)
        VALUES (a.workspace_id, a.observed_at, a.observed_at) RETURNING * INTO target;
        PERFORM open_incident_workflow(target);
    ELSE
        target.id := found[1];
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

-- A recipient gets one new-alert notification per incident and channel: the
-- first visible member notifies them, and later members that they can also
-- see do not. Each alert and recipient still has at most one row.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enqueue_alert_notifications(a alerts, kind text, source_id bigint) RETURNS void LANGUAGE sql AS $$
    INSERT INTO notifications(workspace_id, event, event_id, alert_id, recipient_id, channel, status)
    SELECT a.workspace_id, kind, source_id, a.id, g.user_id, r.channel,
           CASE r.channel WHEN 'email' THEN 'pending' ELSE 'delivered' END
    FROM notification_rules r JOIN alert_grants g USING (workspace_id)
    JOIN users u ON u.id = g.user_id
    WHERE r.workspace_id = a.workspace_id AND r.event = kind
      AND (r.queue IS NULL OR r.queue = a.queue)
      AND (r.forest_class IS NULL OR r.forest_class = a.forest_class)
      AND CASE r.recipients WHEN 'reviewers' THEN g.can_review WHEN 'root' THEN g.depth = 0
                            WHEN 'division_officer' THEN g.role_slug = 'division_officer' AND g.can_review
                            ELSE g.role_slug = r.recipients END
      AND alert_path_visible(a.queue, a.hierarchy_unit_paths, g.can_review, g.hierarchy_id, g.unit_path, g.depth)
      AND (r.channel = 'in_app' OR (u.notification_email IS NOT NULL AND NOT u.notification_email_muted))
      AND (kind <> 'alert_new' OR NOT EXISTS (
          SELECT FROM incident_members mine
          JOIN incident_members other ON other.incident_id = mine.incident_id AND other.alert_id <> a.id
          JOIN notifications earlier ON earlier.alert_id = other.alert_id AND earlier.event = 'alert_new'
          WHERE mine.alert_id = a.id AND earlier.recipient_id = g.user_id AND earlier.channel = r.channel))
    ON CONFLICT DO NOTHING;
$$;
-- +goose StatementEnd

-- Groups each statement's new alerts in observation order, then records their
-- notifications. This runs in the transaction that creates the alerts. The
-- workspace lock makes concurrent ingests and re-matches group one at a time.
DROP TRIGGER notify_alert_insert ON alerts;
-- +goose StatementBegin
CREATE FUNCTION alerts_group_incidents() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    workspace uuid;
    a alerts;
BEGIN
    FOR workspace IN SELECT DISTINCT workspace_id FROM added ORDER BY 1 LOOP
        PERFORM pg_advisory_xact_lock(hashtextextended('open-fimra:incidents:' || workspace::text, 0));
    END LOOP;
    FOR a IN SELECT * FROM added ORDER BY observed_at, id LOOP
        PERFORM group_alert(a);
        PERFORM enqueue_alert_notifications(a, 'alert_new', a.id);
    END LOOP;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER group_incidents AFTER INSERT ON alerts REFERENCING NEW TABLE AS added
    FOR EACH STATEMENT EXECUTE FUNCTION alerts_group_incidents();

-- A merged incident reads through the incident it merged into.
-- +goose StatementBegin
CREATE FUNCTION incident_alert_roles(subject text, workspace_slug text, incident bigint)
RETURNS TABLE(alert_id bigint, user_id uuid, role_slug text, officer boolean) LANGUAGE sql STABLE AS $$
    SELECT m.alert_id, r.user_id, r.role_slug, r.officer
    FROM incidents i JOIN workspaces w ON w.id = i.workspace_id AND w.slug = workspace_slug
    JOIN incident_members m ON m.incident_id = COALESCE(i.merged_into, i.id)
    CROSS JOIN LATERAL alert_roles(subject, workspace_slug, m.alert_id) r
    WHERE i.id = incident;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION incident_moves(subject text, workspace_slug text, incident bigint)
RETURNS TABLE(step text,label text,location_required boolean,can_override boolean,terminal boolean)
LANGUAGE sql STABLE AS $$
    SELECT t.to_step,s.label,t.location_required,bool_or(r.officer),s.terminal
    FROM incidents i JOIN workflow_instances wi ON wi.incident_id=i.id
    JOIN workflow_transitions t ON (t.template_id,t.from_step)=(wi.template_id,wi.current_step)
    JOIN workflow_steps s ON (s.template_id,s.slug)=(t.template_id,t.to_step)
    JOIN incident_alert_roles(subject,workspace_slug,incident) r ON r.role_slug=ANY(t.allowed_roles)
      AND (r.role_slug <> 'division_officer' OR r.officer)
    WHERE i.id=incident AND i.merged_into IS NULL
    GROUP BY t.to_step,s.label,t.location_required,s.terminal;
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION incident_moves(text,text,bigint);
DROP FUNCTION incident_alert_roles(text,text,bigint);
DROP TRIGGER group_incidents ON alerts;
DROP FUNCTION alerts_group_incidents();
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enqueue_alert_notifications(a alerts, kind text, source_id bigint) RETURNS void LANGUAGE sql AS $$
    INSERT INTO notifications(workspace_id, event, event_id, alert_id, recipient_id, channel, status)
    SELECT a.workspace_id, kind, source_id, a.id, g.user_id, r.channel,
           CASE r.channel WHEN 'email' THEN 'pending' ELSE 'delivered' END
    FROM notification_rules r JOIN alert_grants g USING (workspace_id)
    JOIN users u ON u.id = g.user_id
    WHERE r.workspace_id = a.workspace_id AND r.event = kind
      AND (r.queue IS NULL OR r.queue = a.queue)
      AND (r.forest_class IS NULL OR r.forest_class = a.forest_class)
      AND CASE r.recipients WHEN 'reviewers' THEN g.can_review WHEN 'root' THEN g.depth = 0
                            WHEN 'division_officer' THEN g.role_slug = 'division_officer' AND g.can_review
                            ELSE g.role_slug = r.recipients END
      AND alert_path_visible(a.queue, a.hierarchy_unit_paths, g.can_review, g.hierarchy_id, g.unit_path, g.depth)
      AND (r.channel = 'in_app' OR (u.notification_email IS NOT NULL AND NOT u.notification_email_muted))
    ON CONFLICT DO NOTHING;
$$;
-- +goose StatementEnd
CREATE TRIGGER notify_alert_insert AFTER INSERT ON alerts FOR EACH ROW EXECUTE FUNCTION notify_alert_insert();
DROP FUNCTION group_alert(alerts);
DROP FUNCTION open_incident_workflow(incidents);
DROP FUNCTION incident_rule(jsonb);
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_workflow_step() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,details)
    SELECT a.workspace_id, CASE WHEN NEW.user_id IS NULL THEN 'system' ELSE 'user' END,
           u.oidc_subject, 'alert.step','alert',a.id::text,
           jsonb_build_object('stepLogId',NEW.id,'from',NEW.from_step,'to',NEW.to_step)
    FROM alerts a LEFT JOIN users u ON u.id=NEW.user_id WHERE a.id=NEW.alert_id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP INDEX step_log_incident_step_idx, step_log_incident_history_idx;
ALTER TABLE step_log
    DROP CONSTRAINT step_log_incident_step_on_alert,
    DROP CONSTRAINT step_log_one_subject,
    DROP CONSTRAINT step_log_incident_client_key,
    DROP CONSTRAINT step_log_incident_fkey,
    DROP COLUMN incident_step_id;
-- The step log is append-only, so set its guard aside to remove incident entries.
ALTER TABLE step_log DISABLE TRIGGER step_log_append_only;
DELETE FROM step_log WHERE incident_id IS NOT NULL;
ALTER TABLE step_log ENABLE TRIGGER step_log_append_only;
ALTER TABLE step_log DROP COLUMN incident_id, ALTER COLUMN alert_id SET NOT NULL;
DELETE FROM workflow_instances WHERE incident_id IS NOT NULL;
ALTER TABLE workflow_instances
    DROP CONSTRAINT workflow_instances_one_subject,
    DROP CONSTRAINT workflow_instances_incident_template_key,
    DROP CONSTRAINT workflow_instances_incident_key,
    DROP CONSTRAINT workflow_instances_alert_key,
    DROP COLUMN incident_id,
    DROP COLUMN id,
    ALTER COLUMN alert_id SET NOT NULL,
    ADD PRIMARY KEY (alert_id);
DROP TABLE incident_members, incidents;
