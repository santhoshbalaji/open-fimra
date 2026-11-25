-- Forms on the server (PLAN.md sections 5.4 and 8): forms, frozen published
-- versions, assignments to workflow steps, submissions, write-once
-- attachments, append-only approvals and a flat view per published version.

-- +goose Up
-- Flat views of published versions live in their own schema, so their
-- generated names can never clash with a table.
CREATE SCHEMA form_views;

CREATE TABLE forms (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces,
    -- The id in the form file, such as first-report.
    form_key text NOT NULL CHECK (length(form_key) BETWEEN 2 AND 60 AND form_key ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$'),
    created_by uuid REFERENCES users,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, form_key)
);

CREATE TABLE form_versions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    form_id bigint NOT NULL REFERENCES forms,
    version integer NOT NULL CHECK (version > 0),
    -- The exact text of the form file, and the same file parsed.
    source text NOT NULL,
    definition jsonb NOT NULL CHECK (jsonb_typeof(definition) = 'object'),
    -- The columns the validator lists for this file (form format, section 13).
    columns jsonb NOT NULL CHECK (jsonb_typeof(columns) = 'array'),
    -- National, state and division templates that extend each other come
    -- later. A version records the version it extends, if any.
    extends_version_id bigint REFERENCES form_versions,
    created_by uuid REFERENCES users,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    published_by uuid REFERENCES users,
    published_at timestamptz,
    UNIQUE (form_id, version),
    UNIQUE (id, form_id),
    CHECK (definition->>'version' = version::text),
    CHECK ((published_at IS NULL) = (published_by IS NULL)),
    CHECK (extends_version_id IS DISTINCT FROM id)
);

-- A published version never changes and is never deleted. A draft can be
-- edited, published once, or deleted.
-- +goose StatementBegin
CREATE FUNCTION form_version_frozen() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.published_at IS NOT NULL THEN
        RAISE EXCEPTION 'form version % is published and cannot change', OLD.id
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    IF NEW.form_id <> OLD.form_id OR NEW.version <> OLD.version THEN
        RAISE EXCEPTION 'a form version keeps its form and number' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER form_version_frozen BEFORE UPDATE OR DELETE ON form_versions
    FOR EACH ROW EXECUTE FUNCTION form_version_frozen();
CREATE TRIGGER form_version_no_truncate BEFORE TRUNCATE ON form_versions
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_reject_change();

-- +goose StatementBegin
CREATE FUNCTION audit_form_publish() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO audit_log(workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
    SELECT f.workspace_id, 'user', u.oidc_subject, 'form.publish', 'form_version', NEW.id::text,
           jsonb_build_object('form', f.form_key, 'version', NEW.version)
    FROM forms f JOIN users u ON u.id = NEW.published_by WHERE f.id = NEW.form_id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER audit_form_publish AFTER UPDATE OF published_at ON form_versions
    FOR EACH ROW WHEN (OLD.published_at IS NULL AND NEW.published_at IS NOT NULL)
    EXECUTE FUNCTION audit_form_publish();

-- The flat views of each published version: one for the submission and one
-- for each repeating group. The API creates them when it publishes.
CREATE TABLE form_version_views (
    form_version_id bigint NOT NULL REFERENCES form_versions,
    -- Empty for the submission view, otherwise the repeating group's key.
    repeat_key text NOT NULL CHECK (repeat_key = '' OR repeat_key ~ '^[a-z][a-z0-9_]{0,39}$'),
    view_name text NOT NULL UNIQUE CHECK (view_name ~ '^f[0-9]+(_[a-z][a-z0-9_]{0,39})?$'),
    PRIMARY KEY (form_version_id, repeat_key)
);

-- Which published version is filled in at a workflow step, optionally only
-- under some unit paths. A form has at most one version at each step.
CREATE TABLE form_assignments (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    form_id bigint NOT NULL,
    form_version_id bigint NOT NULL,
    template_id bigint NOT NULL,
    step text NOT NULL,
    -- With no unit paths the assignment covers the whole workspace.
    hierarchy_id uuid REFERENCES hierarchies,
    unit_paths ltree[] NOT NULL DEFAULT '{}',
    assigned_by uuid REFERENCES users,
    assigned_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (form_id, template_id, step),
    FOREIGN KEY (form_version_id, form_id) REFERENCES form_versions (id, form_id),
    FOREIGN KEY (template_id, step) REFERENCES workflow_steps,
    CHECK ((hierarchy_id IS NULL) = (cardinality(unit_paths) = 0)),
    CHECK (array_position(unit_paths, NULL) IS NULL)
);
CREATE INDEX form_assignments_step_idx ON form_assignments (template_id, step);

-- +goose StatementBegin
CREATE FUNCTION form_assignment_published() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT FROM form_versions WHERE id = NEW.form_version_id AND published_at IS NOT NULL) THEN
        RAISE EXCEPTION 'only a published form version can be assigned' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER form_assignment_published BEFORE INSERT OR UPDATE ON form_assignments
    FOR EACH ROW EXECUTE FUNCTION form_assignment_published();

-- Photos, signatures and files. The bytes live in the attachment store under
-- their SHA-256 hash, so they are never overwritten. The row is write-once.
CREATE TABLE attachments (
    -- Made by the phone, so a retried upload finds the first one.
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces,
    alert_id bigint NOT NULL REFERENCES alerts,
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes bigint NOT NULL CHECK (size_bytes > 0),
    content_type text NOT NULL CHECK (content_type ~ '^[a-z]+/[a-z0-9.+-]+$'),
    file_name text NOT NULL CHECK (length(file_name) BETWEEN 1 AND 200),
    uploaded_by uuid NOT NULL REFERENCES users,
    uploaded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX attachments_alert_idx ON attachments (alert_id);
CREATE TRIGGER attachments_append_only BEFORE UPDATE OR DELETE ON attachments
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();
CREATE TRIGGER attachments_no_truncate BEFORE TRUNCATE ON attachments
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_reject_change();

-- One filled-in copy of a form version. Submissions never change: a
-- correction is a new submission.
CREATE TABLE submissions (
    -- Made by the phone, so a retried submission finds the first one.
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces,
    form_version_id bigint NOT NULL REFERENCES form_versions,
    -- Every submission is filled in for an alert, at a step of the alert's
    -- workflow. The incident is the one the alert belonged to at the time.
    alert_id bigint NOT NULL REFERENCES alerts,
    incident_id bigint REFERENCES incidents,
    -- The workflow step the submission was filled in at.
    template_id bigint NOT NULL,
    step text NOT NULL,
    user_id uuid NOT NULL REFERENCES users,
    device_time timestamptz,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    location geometry(Point, 4326),
    -- The answers by field key, after the server's checks: hidden answers
    -- removed, calculations and alert values filled in by the server.
    answers jsonb NOT NULL CHECK (jsonb_typeof(answers) = 'object'),
    -- A hash of the request as sent, to tell a retry from a different request.
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    FOREIGN KEY (template_id, step) REFERENCES workflow_steps
);
CREATE INDEX submissions_alert_idx ON submissions (alert_id, step);
CREATE INDEX submissions_version_idx ON submissions (form_version_id);
CREATE INDEX submissions_incident_idx ON submissions (incident_id) WHERE incident_id IS NOT NULL;
CREATE TRIGGER submissions_append_only BEFORE UPDATE OR DELETE ON submissions
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();
CREATE TRIGGER submissions_no_truncate BEFORE TRUNCATE ON submissions
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_reject_change();

-- +goose StatementBegin
CREATE FUNCTION form_submission_published() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT FROM form_versions WHERE id = NEW.form_version_id AND published_at IS NOT NULL) THEN
        RAISE EXCEPTION 'submissions need a published form version' USING ERRCODE = 'check_violation';
    END IF;
    INSERT INTO audit_log(workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
    SELECT NEW.workspace_id, 'user', u.oidc_subject, 'submission.create', 'submission', NEW.id::text,
           jsonb_build_object('alertId', NEW.alert_id, 'formVersionId', NEW.form_version_id, 'step', NEW.step)
    FROM users u WHERE u.id = NEW.user_id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER form_submission_published BEFORE INSERT ON submissions
    FOR EACH ROW EXECUTE FUNCTION form_submission_published();

-- Which attachments each submission uses, by field.
CREATE TABLE submission_attachments (
    submission_id uuid NOT NULL REFERENCES submissions,
    attachment_id uuid NOT NULL REFERENCES attachments,
    field text NOT NULL CHECK (field ~ '^[a-z][a-z0-9_]{0,39}$'),
    PRIMARY KEY (submission_id, field, attachment_id)
);
CREATE INDEX submission_attachments_attachment_idx ON submission_attachments (attachment_id);
CREATE TRIGGER submission_attachments_append_only BEFORE UPDATE OR DELETE ON submission_attachments
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();
CREATE TRIGGER submission_attachments_no_truncate BEFORE TRUNCATE ON submission_attachments
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_reject_change();

-- An officer's decision on a submission: approved, or returned with a note.
-- One decision per submission, never changed.
CREATE TABLE approvals (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    submission_id uuid NOT NULL UNIQUE REFERENCES submissions,
    decision text NOT NULL CHECK (decision IN ('approved', 'returned')),
    note text NOT NULL DEFAULT '' CHECK (length(note) <= 2000),
    user_id uuid NOT NULL REFERENCES users,
    decided_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (decision = 'approved' OR btrim(note) <> '')
);
CREATE TRIGGER approvals_append_only BEFORE UPDATE OR DELETE ON approvals
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();
CREATE TRIGGER approvals_no_truncate BEFORE TRUNCATE ON approvals
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_reject_change();

-- +goose StatementBegin
CREATE FUNCTION audit_approval() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO audit_log(workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
    SELECT s.workspace_id, 'user', u.oidc_subject,
           CASE NEW.decision WHEN 'approved' THEN 'submission.approve' ELSE 'submission.return' END,
           'submission', s.id::text, jsonb_build_object('approvalId', NEW.id, 'note', NEW.note)
    FROM submissions s JOIN users u ON u.id = NEW.user_id WHERE s.id = NEW.submission_id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER audit_approval AFTER INSERT ON approvals FOR EACH ROW EXECUTE FUNCTION audit_approval();

-- The form assignments that apply to an alert, for its workflow template,
-- workspace and matched unit paths, and whether each has a submission that
-- counts: one for the same form and step that no officer has returned.
-- +goose StatementBegin
CREATE FUNCTION alert_forms(alert bigint)
RETURNS TABLE(assignment_id bigint, form_id bigint, form_key text, form_version_id bigint, version integer,
              title text, step text, submitted boolean)
LANGUAGE sql STABLE AS $$
    SELECT fa.id, f.id, f.form_key, v.id, v.version, v.definition #>> '{title,en}', fa.step,
           EXISTS (
               SELECT FROM submissions s JOIN form_versions sv ON sv.id = s.form_version_id
               WHERE s.alert_id = a.id AND sv.form_id = f.id AND s.step = fa.step
                 AND NOT EXISTS (SELECT FROM approvals ap WHERE ap.submission_id = s.id AND ap.decision = 'returned')
           )
    FROM alerts a
    JOIN workflow_instances i ON i.alert_id = a.id
    JOIN forms f ON f.workspace_id = a.workspace_id
    JOIN form_assignments fa ON fa.form_id = f.id AND fa.template_id = i.template_id
    JOIN form_versions v ON v.id = fa.form_version_id
    WHERE a.id = alert
      AND (cardinality(fa.unit_paths) = 0 OR EXISTS (
          SELECT FROM unnest(fa.unit_paths) p(path)
          WHERE match_under_grant(a.hierarchy_unit_paths, fa.hierarchy_id, p.path, 1::smallint)));
$$;
-- +goose StatementEnd

-- The forms an alert still needs before it can leave its current step for
-- another step. A move to a final step, such as false alarm, needs none.
-- +goose StatementBegin
CREATE FUNCTION alert_missing_forms(alert bigint, to_step text)
RETURNS TABLE(form_key text, title text) LANGUAGE sql STABLE AS $$
    SELECT f.form_key, f.title
    FROM workflow_instances i
    JOIN workflow_steps target ON (target.template_id, target.slug) = (i.template_id, to_step)
    CROSS JOIN LATERAL alert_forms(alert) f
    WHERE i.alert_id = alert AND NOT target.terminal AND f.step = i.current_step AND NOT f.submitted
    ORDER BY f.form_key;
$$;
-- +goose StatementEnd

-- +goose Down
-- The flat views read submissions, so they go first.
DROP SCHEMA form_views CASCADE;
DROP FUNCTION alert_missing_forms(bigint, text), alert_forms(bigint);
DROP TABLE approvals;
DROP FUNCTION audit_approval();
DROP TABLE submission_attachments, submissions;
DROP FUNCTION form_submission_published();
DROP TABLE attachments, form_assignments;
DROP FUNCTION form_assignment_published();
DROP TABLE form_version_views;
DROP TABLE form_versions, forms;
DROP FUNCTION audit_form_publish(), form_version_frozen();
