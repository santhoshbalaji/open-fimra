-- File exports (PLAN.md section 5.6). The API saves the rows a user may see in
-- export_rows, in the same transaction as the request. The geo worker writes
-- the file from those rows only, so it can never add a row the user may not see.

-- +goose Up
CREATE TABLE exports (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The requester's OpenID Connect subject. Only this user may download the
    -- file. It is never written into the file.
    requested_by     text NOT NULL,
    kind             text NOT NULL CHECK (kind IN ('detections', 'alerts')),
    format           text NOT NULL CHECK (format IN ('gpkg', 'shapefile', 'kml', 'geojson', 'flatgeobuf',
                                                     'csv_wkt', 'csv_lonlat', 'xlsx')),
    -- The filters as applied, including the absolute time window.
    filters          jsonb NOT NULL,
    -- The requester's roles and units at request time, for the file's note.
    -- Role names and unit names only: no names or contact details of people.
    requester_roles  jsonb NOT NULL,
    row_count        integer NOT NULL CHECK (row_count >= 0),
    status           text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'failed', 'expired')),
    created_at       timestamptz NOT NULL DEFAULT now(),
    -- The transaction that created the export. Only it may add export rows.
    created_xact     xid8 NOT NULL DEFAULT pg_current_xact_id(),
    expires_at       timestamptz NOT NULL,
    finished_at      timestamptz,
    file_key         text CHECK (file_key ~ '^[0-9a-f-]{36}\.(gpkg|zip|kmz|xlsx)$'),
    file_bytes       bigint CHECK (file_bytes > 0),
    feature_count    integer CHECK (feature_count >= 0),
    error_message    text,
    CHECK ((status = 'pending') = (finished_at IS NULL)),
    CHECK ((status = 'ready') = (file_key IS NOT NULL AND file_bytes IS NOT NULL AND feature_count IS NOT NULL)),
    CHECK ((status = 'failed') = (error_message IS NOT NULL)),
    -- A file can lose rows that were deleted after the request, but never gain any.
    CONSTRAINT exports_never_widen CHECK (feature_count <= row_count)
);

CREATE INDEX exports_pending_idx ON exports (created_at, id) WHERE status = 'pending';
CREATE INDEX exports_expiry_idx ON exports (expires_at) WHERE status IN ('pending', 'ready', 'failed');

-- The frozen row set, in file order. Rows are removed with their match or alert,
-- and when the export expires.
CREATE TABLE export_rows (
    export_id     uuid NOT NULL REFERENCES exports (id) ON DELETE CASCADE,
    ordinal       integer NOT NULL CHECK (ordinal > 0),
    workspace_id  uuid NOT NULL,
    detection_id  bigint NOT NULL,
    observed_at   timestamptz NOT NULL,
    alert_id      bigint REFERENCES alerts (id) ON DELETE CASCADE,
    PRIMARY KEY (export_id, ordinal),
    FOREIGN KEY (workspace_id, detection_id, observed_at) REFERENCES detection_matches ON DELETE CASCADE
);

CREATE INDEX export_rows_match_idx ON export_rows (workspace_id, detection_id, observed_at);
CREATE INDEX export_rows_alert_idx ON export_rows (alert_id) WHERE alert_id IS NOT NULL;

-- Export rows can be added only by the transaction that created the export,
-- only while it is pending, only in its workspace, and only up to its row count.
-- +goose StatementBegin
CREATE FUNCTION export_rows_check_added() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM (SELECT DISTINCT export_id FROM added) a
        LEFT JOIN exports e ON e.id = a.export_id
        WHERE e.id IS NULL OR e.status <> 'pending' OR e.created_xact <> pg_current_xact_id()
           OR e.row_count <> (SELECT count(*) FROM export_rows r WHERE r.export_id = e.id)
    ) OR EXISTS (
        SELECT 1 FROM added a JOIN exports e ON e.id = a.export_id
        WHERE a.workspace_id <> e.workspace_id OR (e.kind = 'alerts') <> (a.alert_id IS NOT NULL)
    ) THEN
        RAISE EXCEPTION 'export rows are frozen when the export is requested'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER export_rows_frozen
    AFTER INSERT ON export_rows
    REFERENCING NEW TABLE AS added
    FOR EACH STATEMENT EXECUTE FUNCTION export_rows_check_added();

-- +goose StatementBegin
CREATE FUNCTION export_rows_reject_update() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'export rows cannot be changed' USING ERRCODE = 'insufficient_privilege';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER export_rows_no_update
    BEFORE UPDATE ON export_rows
    FOR EACH ROW EXECUTE FUNCTION export_rows_reject_update();

-- What was asked for, by whom and how many rows were frozen cannot change later.
-- +goose StatementBegin
CREATE FUNCTION exports_keep_request() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.workspace_id, NEW.requested_by, NEW.kind, NEW.format, NEW.filters,
        NEW.requester_roles, NEW.row_count, NEW.created_at, NEW.created_xact)
       IS DISTINCT FROM
       (OLD.id, OLD.workspace_id, OLD.requested_by, OLD.kind, OLD.format, OLD.filters,
        OLD.requester_roles, OLD.row_count, OLD.created_at, OLD.created_xact) THEN
        RAISE EXCEPTION 'an export request cannot be changed' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER exports_request_fixed
    BEFORE UPDATE ON exports
    FOR EACH ROW EXECUTE FUNCTION exports_keep_request();

-- +goose Down
DROP TABLE export_rows;
DROP TABLE exports;
DROP FUNCTION exports_keep_request();
DROP FUNCTION export_rows_reject_update();
DROP FUNCTION export_rows_check_added();
