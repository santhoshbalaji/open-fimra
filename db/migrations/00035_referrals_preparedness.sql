-- +goose Up
CREATE VIEW operations_forms AS
SELECT * FROM (VALUES
    ('offence', 'offence-referral', false),
    ('relief', 'relief-referral', false),
    ('preparedness', 'preparedness-work', false),
    ('training', 'fire-training', false),
    ('readiness', 'readiness-check', false),
    ('burn', 'prescribed-burn-authorisation', false),
    ('burn', 'prescribed-burn-completion', true),
    ('after-action', 'after-action-finding', false),
    ('patrol', 'patrol-log', false)
) f(kind, form_key, completion_only);

CREATE TABLE referral_grants (
    workspace_id uuid NOT NULL,
    hierarchy_id uuid NOT NULL,
    unit_path ltree NOT NULL,
    user_id uuid NOT NULL REFERENCES users,
    kind text NOT NULL CHECK (kind IN ('offence', 'relief')),
    PRIMARY KEY (workspace_id, user_id, kind, hierarchy_id, unit_path),
    FOREIGN KEY (workspace_id, hierarchy_id) REFERENCES hierarchies (workspace_id, id),
    FOREIGN KEY (hierarchy_id, unit_path) REFERENCES units (hierarchy_id, current_path)
        DEFERRABLE INITIALLY DEFERRED
);

ALTER TABLE incidents ADD UNIQUE (workspace_id, id);
ALTER TABLE alerts ADD UNIQUE (workspace_id, id);

CREATE TABLE operations_records (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL,
    hierarchy_id uuid NOT NULL,
    unit_path ltree NOT NULL,
    kind text NOT NULL CHECK (kind IN ('offence', 'relief', 'preparedness', 'training', 'readiness', 'burn', 'after-action', 'patrol')),
    incident_id bigint,
    owner_id uuid NOT NULL REFERENCES users,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((incident_id IS NOT NULL) = (kind IN ('offence', 'relief'))),
    UNIQUE (workspace_id, id),
    UNIQUE (workspace_id, id, kind),
    FOREIGN KEY (workspace_id, incident_id) REFERENCES incidents (workspace_id, id),
    FOREIGN KEY (workspace_id, hierarchy_id) REFERENCES hierarchies (workspace_id, id)
);
CREATE INDEX operations_records_page_idx ON operations_records (workspace_id, id);
CREATE INDEX operations_records_incident_idx ON operations_records (incident_id) WHERE incident_id IS NOT NULL;
CREATE TRIGGER operations_records_append_only BEFORE UPDATE OR DELETE ON operations_records
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();

CREATE TABLE prescribed_burn_alerts (
    workspace_id uuid NOT NULL,
    alert_id bigint PRIMARY KEY,
    record_id uuid NOT NULL,
    kind text GENERATED ALWAYS AS ('burn'::text) STORED,
    FOREIGN KEY (workspace_id, alert_id) REFERENCES alerts (workspace_id, id),
    FOREIGN KEY (workspace_id, record_id, kind) REFERENCES operations_records (workspace_id, id, kind)
);
CREATE INDEX prescribed_burn_alerts_record_idx ON prescribed_burn_alerts (record_id);

ALTER TABLE submissions
    ALTER COLUMN alert_id DROP NOT NULL,
    ALTER COLUMN template_id DROP NOT NULL,
    ALTER COLUMN step DROP NOT NULL,
    ADD COLUMN operations_record_id uuid,
    ADD COLUMN completes_record boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT submissions_operations_record_fkey FOREIGN KEY (workspace_id, operations_record_id)
        REFERENCES operations_records (workspace_id, id),
    ADD CONSTRAINT submissions_one_context CHECK (
        (alert_id IS NOT NULL AND template_id IS NOT NULL AND step IS NOT NULL AND operations_record_id IS NULL AND NOT completes_record)
        OR (alert_id IS NULL AND template_id IS NULL AND step IS NULL AND operations_record_id IS NOT NULL)
    );
CREATE INDEX submissions_operations_record_idx ON submissions (operations_record_id, received_at DESC, id DESC)
    WHERE operations_record_id IS NOT NULL;

ALTER TABLE attachments
    ALTER COLUMN alert_id DROP NOT NULL,
    ADD COLUMN operations_record_id uuid,
    ADD CONSTRAINT attachments_operations_record_fkey FOREIGN KEY (workspace_id, operations_record_id)
        REFERENCES operations_records (workspace_id, id),
    ADD CONSTRAINT attachments_one_context CHECK (num_nonnulls(alert_id, operations_record_id) = 1);
CREATE INDEX attachments_operations_record_idx ON attachments (operations_record_id) WHERE operations_record_id IS NOT NULL;

-- The normal report and export queries join through alert_id. Keep these
-- forms out of that path even if an assignment predates this migration.
-- +goose StatementBegin
CREATE FUNCTION check_operations_submission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    form_key_for_version text;
    record_kind text;
BEGIN
    SELECT f.form_key INTO form_key_for_version FROM form_versions v JOIN forms f ON f.id = v.form_id
    WHERE v.id = NEW.form_version_id AND f.workspace_id = NEW.workspace_id;
    IF NEW.operations_record_id IS NULL THEN
        IF EXISTS (SELECT FROM operations_forms o WHERE o.form_key = form_key_for_version AND o.kind <> 'patrol') THEN
            RAISE EXCEPTION 'this form requires an operations record' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    SELECT kind INTO record_kind FROM operations_records
    WHERE id = NEW.operations_record_id AND workspace_id = NEW.workspace_id;
    IF NOT EXISTS (
        SELECT FROM operations_forms o WHERE o.form_key = form_key_for_version AND o.kind = record_kind
          AND (record_kind <> 'burn' OR o.completion_only = NEW.completes_record)
    ) THEN
        RAISE EXCEPTION 'this form does not belong to the record' USING ERRCODE = 'check_violation';
    END IF;
    IF record_kind IN ('offence', 'relief') AND (
        COALESCE(jsonb_typeof(NEW.answers->'public_summary'), '') <> 'string'
        OR length(btrim(NEW.answers->>'public_summary')) NOT BETWEEN 1 AND 500
        OR NOT NEW.answers ? 'case_reference'
        OR EXISTS (SELECT FROM jsonb_each(NEW.answers - 'public_summary') x(key, value)
            WHERE (key <> 'case_reference'
                AND NOT (record_kind = 'offence' AND key IN ('evidence_reference', 'enquiry_reference'))
                AND NOT (record_kind = 'relief' AND key IN ('beneficiary_reference', 'payment_reference')))
              OR jsonb_typeof(value) <> 'string'
              OR value #>> '{}' !~ '^[A-Za-z0-9][A-Za-z0-9._:/-]{0,119}$')
    ) THEN
        RAISE EXCEPTION 'a referral needs a summary for review and external references' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER check_operations_submission BEFORE INSERT ON submissions
    FOR EACH ROW EXECUTE FUNCTION check_operations_submission();

-- +goose StatementBegin
CREATE FUNCTION check_operations_assignment() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT FROM forms f JOIN operations_forms o USING (form_key)
               WHERE f.id = NEW.form_id AND o.kind <> 'patrol') THEN
        RAISE EXCEPTION 'this form requires an operations record' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER check_operations_assignment BEFORE INSERT OR UPDATE ON form_assignments
    FOR EACH ROW EXECUTE FUNCTION check_operations_assignment();

-- +goose StatementBegin
CREATE FUNCTION check_operations_attachment() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT FROM submissions s JOIN attachments a ON a.id = NEW.attachment_id
        WHERE s.id = NEW.submission_id AND s.workspace_id = a.workspace_id
          AND s.alert_id IS NOT DISTINCT FROM a.alert_id
          AND s.operations_record_id IS NOT DISTINCT FROM a.operations_record_id
    ) THEN
        RAISE EXCEPTION 'the attachment belongs to another record' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER check_operations_attachment BEFORE INSERT ON submission_attachments
    FOR EACH ROW EXECUTE FUNCTION check_operations_attachment();

-- +goose StatementBegin
CREATE FUNCTION check_referral_file() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT FROM operations_records WHERE id = NEW.operations_record_id AND kind IN ('offence', 'relief')) THEN
        RAISE EXCEPTION 'referrals keep evidence references only' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER check_referral_file BEFORE INSERT ON attachments FOR EACH ROW EXECUTE FUNCTION check_referral_file();

-- A permanent workspace assignment gives basic access by unit. A separate
-- scoped grant is required for the full contents of each referral kind.
-- +goose StatementBegin
CREATE FUNCTION operations_scope(subject text, workspace uuid, hierarchy uuid, path ltree)
RETURNS TABLE(user_id uuid, officer boolean) LANGUAGE sql STABLE AS $$
    SELECT g.user_id, bool_or(g.role_slug = 'state_admin' OR (g.role_slug = 'division_officer' AND g.can_review))
    FROM permanent_alert_grants g
    WHERE g.oidc_subject = subject AND g.workspace_id = workspace
      AND ((g.depth = 0 AND g.role_slug = 'state_admin') OR (g.hierarchy_id = hierarchy AND g.unit_path @> path))
    GROUP BY g.user_id;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION operations_roles(subject text, workspace_slug text, record_id uuid)
RETURNS TABLE(user_id uuid, officer boolean, full_access boolean) LANGUAGE sql STABLE AS $$
    SELECT g.user_id, g.officer, r.kind NOT IN ('offence', 'relief') OR EXISTS (
        SELECT FROM referral_grants p
        WHERE p.workspace_id = r.workspace_id AND p.user_id = g.user_id AND p.kind = r.kind
          AND p.hierarchy_id = r.hierarchy_id AND p.unit_path @> r.unit_path
    )
    FROM operations_records r JOIN workspaces w ON w.id = r.workspace_id
    CROSS JOIN LATERAL operations_scope(subject, w.id, r.hierarchy_id, r.unit_path) g
    WHERE w.slug = workspace_slug AND r.id = record_id
    UNION ALL
    SELECT v.user_id, false, false
    FROM operations_records r JOIN workspaces w ON w.id = r.workspace_id
    CROSS JOIN LATERAL (
        SELECT user_id FROM incident_alert_roles(subject, workspace_slug, r.incident_id) LIMIT 1
    ) v
    WHERE w.slug = workspace_slug AND r.id = record_id
      AND NOT EXISTS (SELECT FROM operations_scope(subject, w.id, r.hierarchy_id, r.unit_path));
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_approval() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO audit_log(workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
    SELECT s.workspace_id, 'user', u.oidc_subject,
           CASE NEW.decision WHEN 'approved' THEN 'submission.approve' ELSE 'submission.return' END,
           'submission', s.id::text, jsonb_build_object('approvalId', NEW.id)
           || CASE WHEN EXISTS (SELECT FROM operations_records r
               WHERE r.id = s.operations_record_id AND r.kind IN ('offence', 'relief'))
               THEN '{}'::jsonb ELSE jsonb_build_object('note', NEW.note) END
    FROM submissions s JOIN users u ON u.id = NEW.user_id WHERE s.id = NEW.submission_id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM operations_records) OR EXISTS (SELECT FROM referral_grants) THEN
        RAISE EXCEPTION 'Downgrading would discard operations records or referral grants.';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP FUNCTION operations_roles(text, text, uuid), operations_scope(text, uuid, uuid, ltree);
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_approval() RETURNS trigger LANGUAGE plpgsql AS $$
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
DROP TRIGGER IF EXISTS check_referral_file ON attachments;
DROP FUNCTION IF EXISTS check_referral_file();
DROP TRIGGER check_operations_attachment ON submission_attachments;
DROP TRIGGER check_operations_assignment ON form_assignments;
DROP TRIGGER check_operations_submission ON submissions;
DROP FUNCTION check_operations_attachment(), check_operations_assignment(), check_operations_submission();
ALTER TABLE attachments DROP CONSTRAINT attachments_one_context, DROP COLUMN operations_record_id,
    ALTER COLUMN alert_id SET NOT NULL;
ALTER TABLE submissions DROP CONSTRAINT submissions_one_context, DROP COLUMN operations_record_id, DROP COLUMN completes_record,
    ALTER COLUMN alert_id SET NOT NULL, ALTER COLUMN template_id SET NOT NULL, ALTER COLUMN step SET NOT NULL;
DROP TABLE prescribed_burn_alerts, operations_records, referral_grants;
ALTER TABLE alerts DROP CONSTRAINT alerts_workspace_id_id_key;
ALTER TABLE incidents DROP CONSTRAINT incidents_workspace_id_id_key;
DROP VIEW operations_forms;
