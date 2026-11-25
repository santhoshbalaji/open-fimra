-- Situation reports (PLAN.md section 5.5). A report is the control-room
-- dashboard's numbers for one unit and a day or date range, with incidents,
-- field reports and a map. The API works out the report with the caller's
-- grants and saves it here in the request's transaction. The geo worker only
-- draws the saved report as a PDF, so it can never add something the caller
-- may not see.

-- +goose Up
-- Response times for every milestone in a situation report. New columns go
-- at the end, so the dashboard's earlier columns keep their meaning: time from
-- the alert being raised to the first step-log entry for that step.
CREATE OR REPLACE VIEW alert_response_times AS
SELECT a.workspace_id, a.id AS alert_id,
    min(l.occurred_at) FILTER (WHERE l.to_step='acknowledged') - a.created_at AS to_acknowledged,
    min(l.occurred_at) FILTER (WHERE l.to_step='reached_site') - a.created_at AS to_reached_site,
    min(l.occurred_at) FILTER (WHERE l.to_step='controlled') - a.created_at AS to_controlled,
    min(l.occurred_at) FILTER (WHERE l.to_step='team_sent') - a.created_at AS to_team_sent,
    min(l.occurred_at) FILTER (WHERE l.to_step='extinguished') - a.created_at AS to_extinguished,
    -- From the satellite observation to the alert being raised.
    a.created_at - a.observed_at AS to_alerted
FROM alerts a JOIN step_log l ON l.alert_id=a.id GROUP BY a.id;

CREATE TABLE situation_reports (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The requester's OpenID Connect subject. Only this user may read the
    -- report. It is never written into the file.
    requested_by  text NOT NULL,
    -- The unit the report covers. With no unit, it covers everything the
    -- requester may see.
    hierarchy_id  uuid REFERENCES hierarchies (id) ON DELETE CASCADE,
    unit_path     ltree,
    -- Calendar days in India Standard Time, both included.
    period_from   date NOT NULL,
    period_to     date NOT NULL,
    -- The report exactly as the web page shows it. The worker draws only this.
    content       jsonb NOT NULL CHECK (jsonb_typeof(content) = 'object'),
    status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'failed', 'expired')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    finished_at   timestamptz,
    pdf           bytea CHECK (octet_length(pdf) BETWEEN 1 AND 20971520),
    page_count    integer CHECK (page_count > 0),
    error_message text,
    CHECK (period_from <= period_to),
    CHECK ((hierarchy_id IS NULL) = (unit_path IS NULL)),
    CHECK ((status = 'pending') = (finished_at IS NULL)),
    CHECK ((status = 'ready') = (pdf IS NOT NULL AND page_count IS NOT NULL)),
    CHECK ((status = 'failed') = (error_message IS NOT NULL)),
    -- An expired report keeps its request but not its content or file.
    CHECK (status <> 'expired' OR (content = '{}' AND pdf IS NULL))
);

CREATE INDEX situation_reports_pending_idx ON situation_reports (created_at, id) WHERE status = 'pending';
CREATE INDEX situation_reports_expiry_idx ON situation_reports (expires_at) WHERE status IN ('pending', 'ready', 'failed');

-- What was asked for, by whom and what it showed cannot change later. The
-- content can only be cleared when the report expires.
-- +goose StatementBegin
CREATE FUNCTION situation_reports_keep_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.workspace_id, NEW.requested_by, NEW.hierarchy_id, NEW.unit_path, NEW.period_from,
        NEW.period_to, NEW.created_at)
       IS DISTINCT FROM
       (OLD.id, OLD.workspace_id, OLD.requested_by, OLD.hierarchy_id, OLD.unit_path, OLD.period_from,
        OLD.period_to, OLD.created_at)
       OR (NEW.content IS DISTINCT FROM OLD.content AND NEW.status <> 'expired') THEN
        RAISE EXCEPTION 'a situation report request cannot be changed' USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER situation_reports_request_fixed BEFORE UPDATE ON situation_reports
    FOR EACH ROW EXECUTE FUNCTION situation_reports_keep_request();

-- +goose Down
DROP TABLE situation_reports;
DROP FUNCTION situation_reports_keep_request();
DROP VIEW alert_response_times;
CREATE VIEW alert_response_times AS
SELECT a.workspace_id, a.id AS alert_id,
    min(l.occurred_at) FILTER (WHERE l.to_step='acknowledged') - a.created_at AS to_acknowledged,
    min(l.occurred_at) FILTER (WHERE l.to_step='reached_site') - a.created_at AS to_reached_site,
    min(l.occurred_at) FILTER (WHERE l.to_step='controlled') - a.created_at AS to_controlled
FROM alerts a JOIN step_log l ON l.alert_id=a.id GROUP BY a.id;
