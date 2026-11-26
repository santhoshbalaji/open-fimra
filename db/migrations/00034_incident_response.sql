-- +goose Up
CREATE EXTENSION IF NOT EXISTS btree_gist;
ALTER TABLE workspaces ADD COLUMN extended_incident_response boolean NOT NULL DEFAULT false;
ALTER TABLE workspaces ALTER COLUMN extended_incident_response SET DEFAULT true;

ALTER TABLE workflow_templates DROP CONSTRAINT workflow_templates_workspace_id_key;
ALTER TABLE workflow_templates ADD COLUMN version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    ADD UNIQUE NULLS NOT DISTINCT (workspace_id, version);

INSERT INTO workflow_templates (name, version) VALUES ('Default', 2);
INSERT INTO workflow_steps(template_id, slug, label, terminal, report_pending)
SELECT v2.id, s.slug, s.label, s.terminal, s.report_pending
FROM workflow_steps s JOIN workflow_templates v1 ON v1.id=s.template_id
CROSS JOIN workflow_templates v2
WHERE v1.workspace_id IS NULL AND v1.version=1 AND v2.workspace_id IS NULL AND v2.version=2
UNION ALL
SELECT id, slug, label, terminal, false
FROM workflow_templates CROSS JOIN (VALUES
    ('assigned', 'Assigned', false), ('flash_update', 'Flash update', false),
    ('response_update', 'Response update', false), ('watch', 'Watch', false),
    ('reopened', 'Reopened', false), ('non_forest', 'Non-forest', true),
    ('unsafe', 'Unsafe', true), ('outside_jurisdiction', 'Outside jurisdiction', true)
) steps(slug, label, terminal)
WHERE workspace_id IS NULL AND version=2;

INSERT INTO workflow_transitions
SELECT v2.id, t.from_step, t.to_step, t.allowed_roles, t.location_required, t.time_limit
FROM workflow_transitions t JOIN workflow_templates v1 ON v1.id=t.template_id
CROSS JOIN workflow_templates v2
WHERE v1.workspace_id IS NULL AND v1.version=1 AND v2.workspace_id IS NULL AND v2.version=2
  AND t.to_step NOT IN ('false_alarm','duplicate','controlled_burn','not_reachable')
  AND (t.from_step,t.to_step) NOT IN (('acknowledged','team_sent'),('reached_site','controlled'));

INSERT INTO workflow_transitions
SELECT id, from_step, to_step,
    CASE WHEN officers_only THEN ARRAY['division_officer','state_admin']
         ELSE ARRAY['field_staff','division_officer','state_admin'] END,
    to_step='controlled', time_limit::interval
FROM workflow_templates CROSS JOIN (VALUES
    ('acknowledged','assigned',true,'30 minutes'),
    ('assigned','team_sent',false,'1 hour'),
    ('reached_site','flash_update',false,NULL),
    ('flash_update','controlled',false,NULL),
    ('flash_update','response_update',false,NULL),
    ('controlled','response_update',false,NULL),
    ('extinguished','watch',false,NULL),
    ('watch','first_report',false,'24 hours'),
    ('reopened','acknowledged',false,'30 minutes')
) moves(from_step,to_step,officers_only,time_limit)
WHERE workspace_id IS NULL AND version=2;

INSERT INTO workflow_transitions
SELECT s.template_id, s.slug, outcome.slug, ARRAY['division_officer','state_admin'], false, NULL
FROM workflow_steps s JOIN workflow_steps outcome USING (template_id)
JOIN workflow_templates t ON t.id=s.template_id
WHERE t.workspace_id IS NULL AND t.version=2 AND
    ((NOT s.terminal AND s.slug<>'response_update' AND outcome.terminal AND outcome.slug<>'closed')
     OR (outcome.slug='reopened' AND (s.terminal OR s.slug='watch')));

ALTER TABLE step_log ADD COLUMN actual_at timestamptz,
    ADD COLUMN actual_at_supplied boolean NOT NULL DEFAULT false,
    ADD COLUMN estimated_at timestamptz,
    ADD COLUMN response_note jsonb CHECK (jsonb_typeof(response_note) = 'object');
ALTER TABLE alerts ADD COLUMN intake_incident_id bigint REFERENCES incidents;

CREATE TABLE fire_intake (
    workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
    request_id uuid NOT NULL,
    alert_id bigint NOT NULL UNIQUE REFERENCES alerts ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users,
    source text NOT NULL CHECK (source IN ('phone','radio','staff','public')),
    observed_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    note text NOT NULL CHECK (length(note) <= 2000),
    request_hash text NOT NULL,
    PRIMARY KEY (workspace_id, request_id)
);

CREATE TABLE incident_membership_changes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
    operation text NOT NULL CHECK (operation IN ('merge','split')),
    from_incident_id bigint NOT NULL REFERENCES incidents,
    to_incident_id bigint NOT NULL REFERENCES incidents,
    CHECK (from_incident_id <> to_incident_id),
    alert_ids bigint[] NOT NULL CHECK (cardinality(alert_ids) > 0),
    user_id uuid NOT NULL REFERENCES users,
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 2000),
    changed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX incident_changes_from_idx ON incident_membership_changes(from_incident_id);
CREATE INDEX incident_changes_to_idx ON incident_membership_changes(to_incident_id);
CREATE TRIGGER incident_changes_append_only BEFORE UPDATE OR DELETE ON incident_membership_changes
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();
CREATE TRIGGER incident_changes_no_truncate BEFORE TRUNCATE ON incident_membership_changes
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_reject_change();

CREATE TABLE incident_season_targets (
    workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
    season daterange NOT NULL CHECK (NOT isempty(season) AND NOT lower_inf(season) AND NOT upper_inf(season)),
    step text NOT NULL CHECK (step IN ('new','reopened','acknowledged','assigned','team_sent')),
    target_minutes integer NOT NULL CHECK (target_minutes BETWEEN 1 AND 10080),
    escalation_minutes integer CHECK (escalation_minutes > target_minutes AND escalation_minutes <= 10080),
    EXCLUDE USING gist (workspace_id WITH =, step WITH =, season WITH &&)
);

ALTER TABLE notification_rules DROP CONSTRAINT notification_rules_event_check,
    ADD CONSTRAINT notification_rules_event_check CHECK (event IN ('alert_new','alert_overdue','feed_open','incident_overdue','incident_escalated'));
ALTER TABLE notifications DROP CONSTRAINT notifications_event_check,
    ADD CONSTRAINT notifications_event_check CHECK (event IN ('alert_new','alert_overdue','feed_open','incident_overdue','incident_escalated'));

-- +goose StatementBegin
CREATE FUNCTION seed_incident_notification_rules(workspace uuid) RETURNS void LANGUAGE sql AS $$
    INSERT INTO notification_rules(workspace_id,event,recipients,channel)
    SELECT workspace,event,recipients,channel
    FROM (VALUES ('incident_overdue','reviewers'),('incident_escalated','root')) r(event,recipients)
    CROSS JOIN (VALUES ('in_app'),('email')) c(channel)
    ON CONFLICT DO NOTHING;
$$;
-- +goose StatementEnd
SELECT seed_incident_notification_rules(id) FROM workspaces;

-- +goose StatementBegin
CREATE FUNCTION workspace_incident_notification_defaults() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM seed_incident_notification_rules(NEW.id);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER incident_notification_defaults AFTER INSERT ON workspaces
    FOR EACH ROW EXECUTE FUNCTION workspace_incident_notification_defaults();

CREATE TABLE incident_overdue_escalations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Step IDs are immutable. A foreign key would mask the append-only TRUNCATE guard.
    step_id bigint NOT NULL,
    level smallint NOT NULL CHECK (level IN (1,2)),
    sent_at timestamptz NOT NULL,
    UNIQUE (step_id, level)
);

-- +goose StatementBegin
CREATE FUNCTION workspace_workflow_template(workspace uuid) RETURNS bigint LANGUAGE sql STABLE AS $$
    SELECT t.id FROM workspaces w JOIN workflow_templates t
      ON t.version=CASE WHEN w.extended_incident_response THEN 2 ELSE 1 END
      AND (t.workspace_id=w.id OR t.workspace_id IS NULL)
    WHERE w.id=workspace ORDER BY t.workspace_id NULLS LAST LIMIT 1;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION open_workflow(a alerts) RETURNS void LANGUAGE sql AS $$
    WITH opened AS (
        INSERT INTO workflow_instances(alert_id,template_id,current_step,entered_at)
        VALUES (a.id, workspace_workflow_template(a.workspace_id), 'new', a.created_at)
        RETURNING *
    )
    INSERT INTO step_log(alert_id,template_id,to_step,occurred_at,location)
    SELECT o.alert_id,o.template_id,o.current_step,o.entered_at,d.geom
    FROM opened o JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at);
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION open_incident_workflow(incident incidents) RETURNS void LANGUAGE sql AS $$
    WITH opened AS (
        INSERT INTO workflow_instances(incident_id,template_id,current_step,entered_at)
        VALUES (incident.id, workspace_workflow_template(incident.workspace_id), 'new', incident.created_at)
        RETURNING *
    )
    INSERT INTO step_log(incident_id,template_id,to_step,occurred_at)
    SELECT incident_id,template_id,current_step,entered_at FROM opened;
$$;
-- +goose StatementEnd

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

-- +goose StatementBegin
CREATE FUNCTION incident_response_target(workspace uuid, template bigint, current_step text, entered_at timestamptz)
RETURNS TABLE(level smallint, due_at timestamptz) LANGUAGE sql STABLE AS $$
    WITH active_season AS (
        SELECT target_minutes,escalation_minutes FROM incident_season_targets s
        WHERE s.workspace_id=workspace AND s.step=current_step AND s.season @> (entered_at AT TIME ZONE 'UTC')::date
    )
    SELECT t.level, entered_at + make_interval(mins => t.minutes)
    FROM active_season s
    CROSS JOIN LATERAL (VALUES (1::smallint, s.target_minutes), (2::smallint,s.escalation_minutes)) t(level,minutes)
    WHERE t.minutes IS NOT NULL
    UNION ALL
    SELECT 1::smallint, alert_due_at(template,current_step,entered_at)
    WHERE NOT EXISTS (SELECT FROM active_season) AND alert_due_at(template,current_step,entered_at) IS NOT NULL;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION workflow_template_extended(template bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT workspace_id IS NULL AND version=2 FROM workflow_templates WHERE id=template;
$$;
-- +goose StatementEnd

-- +goose Down
-- A downgrade must not erase workflow or intake history.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM workflow_instances wi JOIN workflow_templates t ON t.id=wi.template_id
               WHERE t.workspace_id IS NULL AND t.version=2)
       OR EXISTS (SELECT FROM fire_intake) OR EXISTS (SELECT FROM incident_membership_changes) THEN
        RAISE EXCEPTION 'Downgrading would discard incident response history.';
    END IF;
END;
$$;
-- +goose StatementEnd

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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION open_incident_workflow(incident incidents) RETURNS void LANGUAGE sql AS $$
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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION open_workflow(a alerts) RETURNS void LANGUAGE sql AS $$
    WITH opened AS (
        INSERT INTO workflow_instances(alert_id,template_id,current_step,entered_at)
        VALUES (a.id, (SELECT t.id FROM workflow_templates t
                WHERE t.workspace_id IS NULL OR t.workspace_id=a.workspace_id
                ORDER BY t.workspace_id NULLS LAST LIMIT 1), 'new', a.created_at)
        RETURNING *
    )
    INSERT INTO step_log(alert_id,template_id,to_step,occurred_at,location)
    SELECT o.alert_id,o.template_id,o.current_step,o.entered_at,d.geom
    FROM opened o JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at);
$$;
-- +goose StatementEnd

DROP FUNCTION workflow_template_extended(bigint);
DROP FUNCTION incident_response_target(uuid,bigint,text,timestamptz), workspace_workflow_template(uuid);
DROP TRIGGER incident_notification_defaults ON workspaces;
DROP FUNCTION workspace_incident_notification_defaults(), seed_incident_notification_rules(uuid);
DELETE FROM notification_rules WHERE event IN ('incident_overdue','incident_escalated');
ALTER TABLE notification_rules DROP CONSTRAINT notification_rules_event_check,
    ADD CONSTRAINT notification_rules_event_check CHECK (event IN ('alert_new','alert_overdue','feed_open'));
ALTER TABLE notifications DROP CONSTRAINT notifications_event_check,
    ADD CONSTRAINT notifications_event_check CHECK (event IN ('alert_new','alert_overdue','feed_open'));
DROP TABLE incident_overdue_escalations, incident_season_targets, incident_membership_changes, fire_intake;
ALTER TABLE alerts DROP COLUMN intake_incident_id;
ALTER TABLE step_log DROP COLUMN actual_at, DROP COLUMN actual_at_supplied, DROP COLUMN estimated_at, DROP COLUMN response_note;
DELETE FROM form_assignments WHERE template_id IN (SELECT id FROM workflow_templates WHERE workspace_id IS NULL AND version=2);
DELETE FROM workflow_transitions WHERE template_id IN (SELECT id FROM workflow_templates WHERE workspace_id IS NULL AND version=2);
DELETE FROM workflow_steps WHERE template_id IN (SELECT id FROM workflow_templates WHERE workspace_id IS NULL AND version=2);
DELETE FROM workflow_templates WHERE workspace_id IS NULL AND version=2;
ALTER TABLE workflow_templates DROP COLUMN version, ADD UNIQUE NULLS NOT DISTINCT (workspace_id);
ALTER TABLE workspaces DROP COLUMN extended_incident_response;
