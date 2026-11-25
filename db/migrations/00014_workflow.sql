-- Default alert workflow, scoped templates, field evidence and response times.
-- +goose Up
ALTER TABLE alerts DROP COLUMN status;
ALTER TABLE workspaces ADD COLUMN workflow_radius_metres integer NOT NULL DEFAULT 1000
    CHECK (workflow_radius_metres > 0);

CREATE TABLE workflow_templates (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid REFERENCES workspaces,
    name text NOT NULL,
    UNIQUE NULLS NOT DISTINCT (workspace_id)
);
CREATE TABLE workflow_steps (
    template_id bigint REFERENCES workflow_templates,
    slug text CHECK (slug ~ '^[a-z][a-z0-9_]{0,63}$'),
    label text NOT NULL,
    terminal boolean NOT NULL DEFAULT false,
    report_pending boolean NOT NULL DEFAULT false,
    PRIMARY KEY (template_id, slug)
);
CREATE TABLE workflow_transitions (
    template_id bigint NOT NULL,
    from_step text NOT NULL,
    to_step text NOT NULL,
    allowed_roles text[] NOT NULL CHECK (cardinality(allowed_roles) > 0),
    location_required boolean NOT NULL,
    time_limit interval CHECK (time_limit > interval '0'),
    PRIMARY KEY (template_id, from_step, to_step),
    FOREIGN KEY (template_id, from_step) REFERENCES workflow_steps,
    FOREIGN KEY (template_id, to_step) REFERENCES workflow_steps,
    CHECK (from_step <> to_step)
);

INSERT INTO workflow_templates(name) VALUES ('Default');
INSERT INTO workflow_steps(template_id, slug, label, terminal, report_pending)
SELECT t.id, slug, label, slug IN ('closed','false_alarm','duplicate','controlled_burn','not_reachable'),
       slug IN ('first_report','final_report')
FROM workflow_templates t CROSS JOIN (VALUES
    ('new','New'), ('acknowledged','Acknowledged'), ('team_sent','Team sent'), ('reached_site','Reached site'),
    ('controlled','Controlled'), ('extinguished','Extinguished'), ('first_report','First report'),
    ('final_report','Final report'), ('verified','Verified'), ('closed','Closed'), ('false_alarm','False alarm'),
    ('duplicate','Duplicate'), ('controlled_burn','Controlled burn'), ('not_reachable','Not reachable')
) steps(slug,label);

INSERT INTO workflow_transitions
SELECT id, from_step, to_step,
    CASE WHEN to_step IN ('verified','closed') THEN ARRAY['division_officer','state_admin']
         ELSE ARRAY['field_staff','division_officer','state_admin'] END,
    to_step IN ('reached_site','controlled','extinguished'), deadline::interval
FROM workflow_templates CROSS JOIN (VALUES
    ('new','acknowledged','30 minutes'), ('acknowledged','team_sent','1 hour'),
    ('team_sent','reached_site','2 hours'), ('reached_site','controlled',NULL),
    ('controlled','extinguished',NULL), ('extinguished','first_report','24 hours'),
    ('first_report','final_report','72 hours'), ('final_report','verified','24 hours'),
    ('verified','closed','24 hours')
) moves(from_step,to_step,deadline);

INSERT INTO workflow_transitions
SELECT s.template_id, s.slug, outcome.slug, ARRAY['division_officer','state_admin'], false, NULL
FROM workflow_steps s JOIN workflow_steps outcome USING (template_id)
WHERE NOT s.terminal AND outcome.terminal AND outcome.slug <> 'closed';

CREATE TABLE workflow_instances (
    alert_id bigint PRIMARY KEY REFERENCES alerts,
    template_id bigint NOT NULL,
    current_step text NOT NULL,
    entered_at timestamptz NOT NULL,
    UNIQUE (alert_id, template_id),
    FOREIGN KEY (template_id, current_step) REFERENCES workflow_steps
);
CREATE TABLE step_log (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    alert_id bigint NOT NULL,
    template_id bigint NOT NULL,
    from_step text,
    to_step text NOT NULL,
    user_id uuid REFERENCES users,
    occurred_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    location geometry(Point,4326),
    note text NOT NULL DEFAULT '',
    override_reason text NOT NULL DEFAULT '',
    report_pending boolean NOT NULL DEFAULT false,
    client_id text,
    client_time timestamptz,
    UNIQUE (alert_id,client_id),
    FOREIGN KEY (alert_id,template_id) REFERENCES workflow_instances(alert_id,template_id),
    FOREIGN KEY (template_id,to_step) REFERENCES workflow_steps,
    FOREIGN KEY (template_id,from_step,to_step) REFERENCES workflow_transitions,
    CHECK ((user_id IS NULL) = (from_step IS NULL)),
    CHECK (from_step IS NOT NULL OR to_step = 'new')
);
CREATE INDEX step_log_history_idx ON step_log(alert_id,id);
CREATE TRIGGER step_log_append_only BEFORE UPDATE OR DELETE ON step_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();
CREATE TRIGGER step_log_no_truncate BEFORE TRUNCATE ON step_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_reject_change();

-- +goose StatementBegin
CREATE FUNCTION audit_workflow_step() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER audit_step AFTER INSERT ON step_log FOR EACH ROW EXECUTE FUNCTION audit_workflow_step();

-- Shared by existing alerts and every future insert, including backfill.
-- +goose StatementBegin
CREATE FUNCTION open_workflow(a alerts) RETURNS void LANGUAGE sql AS $$
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
SELECT open_workflow(a) FROM alerts a;
-- +goose StatementBegin
CREATE FUNCTION alert_open_workflow() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM open_workflow(NEW);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER open_workflow AFTER INSERT ON alerts FOR EACH ROW EXECUTE FUNCTION alert_open_workflow();

CREATE VIEW alert_response_times AS
SELECT a.workspace_id, a.id AS alert_id,
    min(l.occurred_at) FILTER (WHERE l.to_step='acknowledged') - a.created_at AS to_acknowledged,
    min(l.occurred_at) FILTER (WHERE l.to_step='reached_site') - a.created_at AS to_reached_site,
    min(l.occurred_at) FILTER (WHERE l.to_step='controlled') - a.created_at AS to_controlled
FROM alerts a JOIN step_log l ON l.alert_id=a.id GROUP BY a.id;

CREATE VIEW alert_grants AS
SELECT a.workspace_id, w.slug, a.hierarchy_id, a.unit_path, a.role_slug, u.depth,
       caller.oidc_subject, caller.id AS user_id,
       u.depth=0 OR COALESCE(u.depth<=division.depth,false) AS can_review
FROM role_assignments a JOIN users caller ON caller.id=a.user_id
JOIN workspaces w ON w.id=a.workspace_id
JOIN units u ON u.hierarchy_id=a.hierarchy_id AND u.current_path=a.unit_path
LEFT JOIN hierarchy_levels division ON division.hierarchy_id=u.hierarchy_id AND division.slug='division';

-- +goose StatementBegin
CREATE FUNCTION alert_roles(subject text, workspace_slug text, alert bigint)
RETURNS TABLE(user_id uuid, role_slug text, officer boolean) LANGUAGE sql STABLE AS $$
    SELECT g.user_id,g.role_slug,g.role_slug='state_admin' OR (g.role_slug='division_officer' AND g.can_review)
    FROM alert_grants g JOIN alerts a ON a.workspace_id=g.workspace_id
    JOIN detection_matches m ON (m.workspace_id,m.detection_id,m.observed_at)=(a.workspace_id,a.detection_id,a.observed_at)
    WHERE g.oidc_subject=subject AND g.slug=workspace_slug AND a.id=alert
      AND (a.queue='unit' OR g.can_review)
      AND match_under_grant(m.hierarchy_unit_paths,g.hierarchy_id,g.unit_path,g.depth);
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION alert_moves(subject text, workspace_slug text, alert bigint)
RETURNS TABLE(step text,label text,location_required boolean,can_override boolean,terminal boolean)
LANGUAGE sql STABLE AS $$
    SELECT t.to_step,s.label,t.location_required,bool_or(r.officer),s.terminal
    FROM workflow_instances i JOIN workflow_transitions t
      ON (t.template_id,t.from_step)=(i.template_id,i.current_step)
    JOIN workflow_steps s ON (s.template_id,s.slug)=(t.template_id,t.to_step)
    JOIN alert_roles(subject,workspace_slug,alert) r ON r.role_slug=ANY(t.allowed_roles)
      AND (r.role_slug <> 'division_officer' OR r.officer)
    WHERE i.alert_id=alert GROUP BY t.to_step,s.label,t.location_required,s.terminal;
$$;
-- +goose StatementEnd
-- +goose Down
DROP FUNCTION alert_moves(text,text,bigint);
DROP FUNCTION alert_roles(text,text,bigint);
DROP VIEW alert_grants, alert_response_times;
DROP TRIGGER open_workflow ON alerts;
DROP FUNCTION alert_open_workflow(), open_workflow(alerts);
DROP TABLE step_log;
DROP FUNCTION audit_workflow_step();
DROP TABLE workflow_instances, workflow_transitions, workflow_steps, workflow_templates;
ALTER TABLE workspaces DROP COLUMN workflow_radius_metres;
ALTER TABLE alerts ADD COLUMN status text NOT NULL DEFAULT 'new' CHECK (status='new');
