-- +goose Up
ALTER TABLE workspace_settings_secrets DROP CONSTRAINT workspace_settings_secrets_name_check,
    ADD CHECK (name IN ('firms_map_key', 'state_extract_signing_key'));

CREATE TABLE state_extract_authorities (
    workspace_id uuid PRIMARY KEY REFERENCES workspaces,
    state_code text NOT NULL UNIQUE CHECK (state_code ~ '^[A-Z][A-Z0-9-]{1,31}$'),
    unit_code_system text NOT NULL CHECK (unit_code_system ~ '^[a-z][a-z0-9-]{1,31}$')
);

CREATE TABLE state_extract_keys (
    workspace_id uuid NOT NULL REFERENCES state_extract_authorities,
    key_id text NOT NULL CHECK (key_id ~ '^[a-f0-9]{32}$'),
    public_key bytea NOT NULL CHECK (octet_length(public_key) = 32),
    PRIMARY KEY (workspace_id, key_id)
);

CREATE TABLE state_extract_windows (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES state_extract_authorities,
    days daterange NOT NULL CHECK (NOT isempty(days) AND NOT lower_inf(days) AND NOT upper_inf(days)),
    UNIQUE (id, workspace_id),
    EXCLUDE USING gist (workspace_id WITH =, days WITH &&)
);

ALTER TABLE exports
    DROP CONSTRAINT exports_kind_check, DROP CONSTRAINT exports_format_check, DROP CONSTRAINT exports_check1,
    ADD COLUMN state_window bigint,
    ADD COLUMN state_revision int,
    ADD COLUMN state_supersedes uuid UNIQUE REFERENCES exports,
    ADD COLUMN state_receipt jsonb,
    ADD CONSTRAINT exports_kind_check CHECK (kind IN ('detections', 'alerts', 'submissions', 'state_extract')),
    ADD CONSTRAINT exports_format_check CHECK (format IN ('gpkg','shapefile','kml','geojson','flatgeobuf','csv_wkt','csv_lonlat','xlsx','json')),
    ADD CONSTRAINT exports_ready_file CHECK ((status = 'ready' AND kind <> 'state_extract') =
        (file_key IS NOT NULL AND file_bytes IS NOT NULL AND feature_count IS NOT NULL)),
    ADD CONSTRAINT state_extract_window_fk FOREIGN KEY (state_window, workspace_id) REFERENCES state_extract_windows (id, workspace_id),
    ADD CONSTRAINT state_extract_revision_key UNIQUE (state_window, state_revision),
    ADD CONSTRAINT state_extract_shape CHECK (
        (kind = 'state_extract' AND state_window IS NOT NULL AND state_revision IS NOT NULL AND state_revision > 0
         AND (state_supersedes IS NULL) = (state_revision = 1) AND row_count = 2
         AND format = 'json' AND expires_at = 'infinity' AND status IN ('pending','ready')
         AND file_key IS NULL AND file_bytes IS NULL AND feature_count IS NULL
         AND (status = 'ready') = (state_receipt IS NOT NULL))
        OR (kind <> 'state_extract' AND format <> 'json'
            AND num_nonnulls(state_window, state_revision, state_supersedes, state_receipt) = 0));

ALTER TABLE export_rows ALTER COLUMN detection_id DROP NOT NULL, ALTER COLUMN observed_at DROP NOT NULL,
    ADD COLUMN state_document bytea,
    ADD CONSTRAINT state_extract_row_shape CHECK (
        (state_document IS NULL AND detection_id IS NOT NULL AND observed_at IS NOT NULL)
        OR (state_document IS NOT NULL AND ordinal IN (1,2) AND
            num_nonnulls(detection_id, observed_at, alert_id, submission_id, boundary_snapshot, dispatch) = 0));

-- The existing insert trigger still checks workspace, row count and transaction.
-- +goose StatementBegin
CREATE FUNCTION state_extract_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'export_rows' THEN
        IF TG_OP = 'DELETE' THEN
            IF OLD.state_document IS NOT NULL THEN
                RAISE EXCEPTION 'State extract rows cannot be deleted' USING ERRCODE = 'check_violation';
            END IF;
            RETURN OLD;
        END IF;
        IF NEW.state_document IS NOT NULL AND
            (SELECT kind FROM exports WHERE id = NEW.export_id) IS DISTINCT FROM 'state_extract' THEN
            RAISE EXCEPTION 'Invalid state extract row' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF OLD.kind = 'state_extract' THEN
            RAISE EXCEPTION 'State extract history cannot be deleted' USING ERRCODE = 'check_violation';
        END IF;
        RETURN OLD;
    END IF;
    IF NEW.kind <> 'state_extract' THEN RETURN NEW; END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'pending' OR (NEW.state_revision > 1 AND NOT EXISTS (
            SELECT FROM exports p WHERE p.id = NEW.state_supersedes AND p.state_window = NEW.state_window
              AND p.state_revision = NEW.state_revision - 1 AND p.status = 'ready')) THEN
            RAISE EXCEPTION 'Invalid state extract revision' USING ERRCODE = 'check_violation';
        END IF;
    ELSIF OLD.status <> 'pending' OR NEW.status <> 'ready'
        OR (NEW.state_window, NEW.state_revision, NEW.state_supersedes)
            IS DISTINCT FROM (OLD.state_window, OLD.state_revision, OLD.state_supersedes)
        OR (SELECT count(*) FROM export_rows WHERE export_id = NEW.id AND state_document IS NOT NULL) <> NEW.row_count THEN
        RAISE EXCEPTION 'State extract revisions cannot be changed' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION state_extract_must_finish() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT FROM exports WHERE id = NEW.id AND status = 'pending') THEN
        RAISE EXCEPTION 'A state extract must finish in its creating transaction' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER state_extract_must_finish
    AFTER INSERT ON exports DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (NEW.kind = 'state_extract') EXECUTE FUNCTION state_extract_must_finish();

CREATE TRIGGER state_extract_revision_guard BEFORE INSERT OR UPDATE OR DELETE ON exports
    FOR EACH ROW EXECUTE FUNCTION state_extract_guard();
CREATE TRIGGER state_extract_row_guard BEFORE INSERT OR DELETE ON export_rows
    FOR EACH ROW EXECUTE FUNCTION state_extract_guard();
CREATE TRIGGER state_extract_authority_fixed BEFORE UPDATE OR DELETE ON state_extract_authorities
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();
CREATE TRIGGER state_extract_keys_fixed BEFORE UPDATE OR DELETE ON state_extract_keys
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();
CREATE TRIGGER state_extract_windows_fixed BEFORE UPDATE OR DELETE ON state_extract_windows
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM state_extract_authorities) THEN
        RAISE EXCEPTION 'Downgrading would discard state extract history.';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER state_extract_must_finish ON exports;
DROP FUNCTION state_extract_must_finish();
DROP TRIGGER state_extract_row_guard ON export_rows;
DROP TRIGGER state_extract_revision_guard ON exports;
DROP FUNCTION state_extract_guard();
ALTER TABLE export_rows DROP CONSTRAINT state_extract_row_shape, DROP COLUMN state_document,
    ALTER COLUMN detection_id SET NOT NULL, ALTER COLUMN observed_at SET NOT NULL;
ALTER TABLE exports DROP CONSTRAINT state_extract_shape, DROP CONSTRAINT state_extract_revision_key,
    DROP CONSTRAINT state_extract_window_fk, DROP CONSTRAINT exports_kind_check, DROP CONSTRAINT exports_format_check,
    DROP CONSTRAINT exports_ready_file, DROP COLUMN state_window, DROP COLUMN state_revision,
    DROP COLUMN state_supersedes, DROP COLUMN state_receipt,
    ADD CONSTRAINT exports_kind_check CHECK (kind IN ('detections','alerts','submissions')),
    ADD CONSTRAINT exports_format_check CHECK (format IN ('gpkg','shapefile','kml','geojson','flatgeobuf','csv_wkt','csv_lonlat','xlsx')),
    ADD CONSTRAINT exports_check1 CHECK ((status = 'ready') =
        (file_key IS NOT NULL AND file_bytes IS NOT NULL AND feature_count IS NOT NULL));
DROP TABLE state_extract_windows, state_extract_keys, state_extract_authorities;
ALTER TABLE workspace_settings_secrets DROP CONSTRAINT workspace_settings_secrets_name_check,
    ADD CONSTRAINT workspace_settings_secrets_name_check CHECK (name IN ('firms_map_key'));
