-- +goose Up
ALTER TABLE workspaces ADD COLUMN kind text NOT NULL DEFAULT 'state' CHECK (kind IN ('state','national')),
    ADD UNIQUE (id,kind);

INSERT INTO roles (slug,description) VALUES
    ('national_ministry','Reads state-shared aggregate and scheme returns.'),
    ('national_survey','Reads state-shared detection comparisons and feedback.'),
    ('national_disaster','Reads state-authorised major incident updates and aid requests.'),
    ('national_nodal','Submits and shares returns and updates for the assigned state.'),
    ('national_public','Reads state-approved public aggregate releases.');

CREATE TABLE national_states (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL,
    workspace_kind text NOT NULL DEFAULT 'national' CHECK (workspace_kind = 'national'),
    hierarchy_id uuid NOT NULL,
    unit_path ltree NOT NULL CHECK (nlevel(unit_path) = 2),
    state_code text NOT NULL CHECK (state_code ~ '^[A-Z][A-Z0-9-]{1,31}$'),
    unit_code_system text NOT NULL CHECK (unit_code_system ~ '^[a-z][a-z0-9-]{1,31}$'),
    location_decimals smallint NOT NULL DEFAULT 0 CHECK (location_decimals BETWEEN 0 AND 1),
    FOREIGN KEY (workspace_id,workspace_kind) REFERENCES workspaces (id,kind),
    FOREIGN KEY (workspace_id,hierarchy_id) REFERENCES hierarchies (workspace_id,id),
    FOREIGN KEY (hierarchy_id,unit_path) REFERENCES units (hierarchy_id,current_path) DEFERRABLE INITIALLY DEFERRED,
    UNIQUE (workspace_id,state_code),
    UNIQUE (hierarchy_id,unit_path)
);

-- Trust is configured by the receiving operator, out of band from submissions.
CREATE TABLE national_state_keys (
    state_id bigint NOT NULL REFERENCES national_states,
    key_id text NOT NULL CHECK (key_id ~ '^[0-9a-f]{32}$'),
    public_key bytea NOT NULL CHECK (octet_length(public_key) = 32),
    PRIMARY KEY (state_id,key_id),
    CHECK (key_id = left(encode(sha256(public_key),'hex'),32))
);

CREATE VIEW national_state_grants AS
SELECT s.id AS state_id, s.workspace_id, w.slug, u.oidc_subject, a.role_slug
FROM national_states s
JOIN workspaces w ON w.id=s.workspace_id
JOIN role_assignments a ON (a.workspace_id,a.hierarchy_id,a.unit_path)=(s.workspace_id,s.hierarchy_id,s.unit_path)
JOIN users u ON u.id=a.user_id
WHERE a.role_slug IN ('national_ministry','national_survey','national_disaster','national_nodal','national_public');

CREATE TABLE national_windows (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    state_id bigint NOT NULL REFERENCES national_states,
    days daterange NOT NULL CHECK (NOT isempty(days) AND NOT lower_inf(days) AND NOT upper_inf(days)
        AND upper(days)-lower(days) <= 366),
    UNIQUE (id,state_id),
    ministry_shared boolean NOT NULL DEFAULT false,
    survey_shared boolean NOT NULL DEFAULT false,
    preparedness_shared boolean NOT NULL DEFAULT false,
    feedback_shared boolean NOT NULL DEFAULT false,
    EXCLUDE USING gist (state_id WITH =, days WITH &&)
);

-- Signed returns and trusted keys belong to a fixed reporting authority.
-- +goose StatementBegin
CREATE FUNCTION national_state_identity_fixed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.workspace_id,NEW.hierarchy_id,NEW.unit_path,NEW.state_code,NEW.unit_code_system)
       IS DISTINCT FROM (OLD.workspace_id,OLD.hierarchy_id,OLD.unit_path,OLD.state_code,OLD.unit_code_system)
       AND (EXISTS (SELECT FROM national_state_keys WHERE state_id=OLD.id)
            OR EXISTS (SELECT FROM national_windows WHERE state_id=OLD.id)) THEN
        RAISE EXCEPTION 'A registered reporting authority cannot be reassigned'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER national_state_identity_fixed BEFORE UPDATE ON national_states
    FOR EACH ROW EXECUTE FUNCTION national_state_identity_fixed();
CREATE TRIGGER national_window_dates_fixed BEFORE UPDATE OF state_id,days ON national_windows
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();

CREATE TABLE national_extracts (
    window_id bigint NOT NULL,
    state_id bigint NOT NULL,
    kind text NOT NULL CHECK (kind IN ('incident','preparedness')),
    id uuid NOT NULL,
    revision int NOT NULL CHECK (revision > 0),
    supersedes uuid,
    previous_revision int GENERATED ALWAYS AS (CASE WHEN revision > 1 THEN revision - 1 END) STORED,
    signed_document bytea NOT NULL,
    payload jsonb NOT NULL,
    receipt jsonb NOT NULL,
    FOREIGN KEY (window_id,state_id) REFERENCES national_windows (id,state_id),
    PRIMARY KEY (window_id,kind,revision),
    UNIQUE (state_id,kind,id),
    UNIQUE (window_id,kind,id),
    UNIQUE (window_id,kind,revision,id),
    FOREIGN KEY (window_id,kind,previous_revision,supersedes)
        REFERENCES national_extracts (window_id,kind,revision,id),
    CHECK ((revision = 1) = (supersedes IS NULL)),
    CHECK ((payload->>'id' = id::text AND payload->>'kind' = kind AND (payload->>'revision')::int = revision
        AND (payload->>'supersedes') IS NOT DISTINCT FROM supersedes::text
        AND receipt->>'extractId' = id::text AND receipt->>'kind' = kind
        AND (receipt->>'revision')::int = revision AND receipt->>'sha256' ~ '^[0-9a-f]{64}$') IS TRUE)
);

CREATE TRIGGER national_extracts_fixed BEFORE UPDATE OR DELETE ON national_extracts
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();

CREATE VIEW national_current_extracts AS
SELECT DISTINCT ON (window_id,kind) window_id,kind,id,revision,payload,receipt
FROM national_extracts ORDER BY window_id,kind,revision DESC;

-- Public approval pins a single incident revision. A correction withdraws the
-- previous public figures until the state approves the new revision.
CREATE TABLE national_publications (
    window_id bigint PRIMARY KEY,
    kind text GENERATED ALWAYS AS ('incident'::text) STORED,
    extract_id uuid NOT NULL,
    FOREIGN KEY (window_id,kind,extract_id) REFERENCES national_extracts (window_id,kind,id)
);

CREATE VIEW national_public_aggregates AS
SELECT p.window_id, jsonb_build_object(
    'source', x.payload->>'source',
    'state', s.state_code, 'geography', u.name,
    'from', lower(w.days), 'to', upper(w.days)-1,
    'confirmedFires', CASE WHEN (x.payload #>> '{totals,incidents,new}')::bigint >= 5
                           THEN (x.payload #>> '{totals,incidents,new}')::bigint END,
    'coverage', CASE WHEN x.payload #>> '{coverage,status}' = 'zero' THEN 'reported'
                     ELSE x.payload #>> '{coverage,status}' END
) AS aggregate
FROM national_publications p
JOIN national_current_extracts x ON (x.window_id,x.kind,x.id)=(p.window_id,p.kind,p.extract_id)
JOIN national_windows w ON w.id=p.window_id
JOIN national_states s ON s.id=w.state_id
JOIN units u ON (u.hierarchy_id,u.current_path)=(s.hierarchy_id,s.unit_path);

CREATE TABLE national_satellite_measures (
    window_id bigint PRIMARY KEY REFERENCES national_windows,
    source text NOT NULL CHECK (length(btrim(source)) BETWEEN 1 AND 120),
    product text NOT NULL CHECK (length(btrim(product)) BETWEEN 1 AND 120),
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL CHECK (ends_at > starts_at),
    expected int NOT NULL CHECK (expected > 0),
    reported int NOT NULL CHECK (reported BETWEEN 0 AND expected),
    detections bigint CHECK (detections BETWEEN 0 AND 9007199254740991),
    CHECK ((reported = 0) = (detections IS NULL))
);

CREATE TABLE national_feedback (
    window_id bigint PRIMARY KEY REFERENCES national_windows,
    prepared int NOT NULL CHECK (prepared >= 0),
    submitted int NOT NULL CHECK (submitted >= 0),
    accepted int NOT NULL CHECK (accepted >= 0),
    rejected int NOT NULL CHECK (rejected >= 0)
);

CREATE TABLE national_incident_updates (
    state_id bigint NOT NULL REFERENCES national_states,
    incident_ref text NOT NULL CHECK (incident_ref ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$'),
    shared_event_id uuid NOT NULL,
    fsi_id text CHECK (fsi_id ~ '^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$'),
    status text NOT NULL CHECK (status IN ('active','controlled','extinguished','closed')),
    severity text NOT NULL CHECK (severity IN ('major','severe','critical')),
    occurred_at timestamptz NOT NULL,
    next_update_at timestamptz CHECK (next_update_at > occurred_at),
    precision_decimals smallint NOT NULL CHECK (precision_decimals BETWEEN 0 AND 1),
    longitude numeric CHECK (longitude BETWEEN -180 AND 180 AND longitude = round(longitude,precision_decimals)),
    latitude numeric CHECK (latitude BETWEEN -90 AND 90 AND latitude = round(latitude,precision_decimals)),
    CHECK ((latitude IS NULL) = (longitude IS NULL)),
    threats text NOT NULL CHECK (length(threats) <= 500),
    aid_requested text NOT NULL CHECK (length(aid_requested) <= 500),
    aid_accepted text NOT NULL CHECK (length(aid_accepted) <= 500),
    provisional_impacts text NOT NULL CHECK (length(provisional_impacts) <= 500),
    shared boolean NOT NULL DEFAULT false,
    PRIMARY KEY (state_id,incident_ref),
    UNIQUE (state_id,shared_event_id)
);

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM national_states) THEN
        RAISE EXCEPTION 'Downgrading would discard national portal records.';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP TABLE national_incident_updates, national_feedback, national_satellite_measures;
DROP VIEW national_public_aggregates;
DROP TABLE national_publications;
DROP VIEW national_current_extracts, national_state_grants;
DROP TABLE national_extracts, national_windows, national_state_keys, national_states;
DROP FUNCTION national_state_identity_fixed();
DELETE FROM role_assignments WHERE role_slug IN ('national_ministry','national_survey','national_disaster','national_nodal','national_public');
DELETE FROM roles WHERE slug IN ('national_ministry','national_survey','national_disaster','national_nodal','national_public');
ALTER TABLE workspaces DROP COLUMN kind;
