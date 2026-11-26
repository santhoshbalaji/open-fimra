-- +goose Up
CREATE TABLE daily_return_policies (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
    hierarchy_id uuid NOT NULL,
    depth smallint NOT NULL CHECK (depth > 0),
    season daterange NOT NULL CHECK (NOT isempty(season) AND NOT lower_inf(season) AND NOT upper_inf(season)),
    time_zone text NOT NULL,
    cutoff time(0) NOT NULL CHECK (cutoff < '24:00'),
    approver_role text NOT NULL CHECK (approver_role IN ('state_admin', 'division_officer')),
    FOREIGN KEY (workspace_id, hierarchy_id) REFERENCES hierarchies (workspace_id, id),
    FOREIGN KEY (hierarchy_id, depth) REFERENCES hierarchy_levels,
    EXCLUDE USING gist (workspace_id WITH =, season WITH &&)
);

ALTER TABLE situation_reports
    ADD COLUMN daily_policy_id bigint REFERENCES daily_return_policies,
    ADD COLUMN daily_revision integer CHECK (daily_revision > 0),
    ADD COLUMN daily_previous_id uuid REFERENCES situation_reports,
    ADD CONSTRAINT daily_return_revision UNIQUE (daily_policy_id, unit_path, period_from, daily_revision),
    ADD CONSTRAINT daily_return_shape CHECK (
        (daily_policy_id IS NULL AND daily_revision IS NULL AND daily_previous_id IS NULL)
        OR (daily_policy_id IS NOT NULL AND daily_revision IS NOT NULL AND hierarchy_id IS NOT NULL
            AND (daily_previous_id IS NULL) = (daily_revision = 1) AND period_from = period_to
            AND status = 'draft' AND expires_at = 'infinity' AND content ? 'dailyReturn'));

CREATE TABLE daily_return_decisions (
    report_id uuid PRIMARY KEY REFERENCES situation_reports ON DELETE CASCADE,
    decision text NOT NULL CHECK (decision IN ('approved', 'returned')),
    decided_by text NOT NULL,
    decided_name text NOT NULL,
    decided_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    note text NOT NULL CHECK (length(note) <= 2000 AND (decision = 'approved' OR btrim(note) <> ''))
);

CREATE TRIGGER daily_return_decisions_fixed BEFORE UPDATE OR DELETE ON daily_return_decisions
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();

-- +goose StatementBegin
CREATE FUNCTION check_daily_return_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF (NEW.daily_policy_id, NEW.daily_revision, NEW.daily_previous_id)
           IS DISTINCT FROM (OLD.daily_policy_id, OLD.daily_revision, OLD.daily_previous_id) THEN
            RAISE EXCEPTION 'Daily return revision cannot change' USING ERRCODE = 'check_violation';
        END IF;
    ELSIF NEW.daily_policy_id IS NOT NULL THEN
        IF NOT EXISTS (
            SELECT FROM daily_return_policies p WHERE p.id = NEW.daily_policy_id
            AND p.workspace_id = NEW.workspace_id AND p.hierarchy_id = NEW.hierarchy_id AND p.season @> NEW.period_from
        ) OR (NEW.daily_revision > 1 AND NOT EXISTS (
            SELECT FROM situation_reports p JOIN daily_return_decisions d ON d.report_id = p.id
            WHERE p.id = NEW.daily_previous_id AND p.daily_policy_id = NEW.daily_policy_id
              AND p.unit_path = NEW.unit_path AND p.period_from = NEW.period_from AND p.daily_revision = NEW.daily_revision - 1
        )) THEN
            RAISE EXCEPTION 'Invalid daily return revision' USING ERRCODE = 'check_violation';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER daily_return_revision_fixed BEFORE INSERT OR UPDATE ON situation_reports
    FOR EACH ROW EXECUTE FUNCTION check_daily_return_revision();

-- The named date is the closing day. Each local wall-clock boundary is
-- converted separately, including on days when the offset changes.
CREATE FUNCTION daily_return_cutoff(day date, cutoff time, zone text)
RETURNS timestamptz LANGUAGE sql STABLE STRICT AS
'SELECT (day + cutoff) AT TIME ZONE zone';

CREATE FUNCTION daily_return_units(hierarchy uuid, level_depth smallint, boundary timestamptz)
RETURNS SETOF units LANGUAGE sql STABLE STRICT AS '
    SELECT DISTINCT ON (u.path) u.* FROM units u
    WHERE u.hierarchy_id = hierarchy AND u.depth = level_depth
      AND u.valid_from <= boundary AND (u.valid_to IS NULL OR u.valid_to > boundary)
    ORDER BY u.path, u.valid_from DESC, u.id DESC';

CREATE TABLE daily_return_notices (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    policy_id bigint NOT NULL REFERENCES daily_return_policies ON DELETE CASCADE,
    day date NOT NULL,
    unit_path ltree NOT NULL,
    unit_name text NOT NULL,
    event text NOT NULL CHECK (event IN ('daily_return_due', 'daily_return_missing')),
    UNIQUE (policy_id, day, unit_path, event)
);

ALTER TABLE notification_rules DROP CONSTRAINT notification_rules_event_check,
    ADD CONSTRAINT notification_rules_event_check CHECK (event IN ('alert_new','alert_overdue','feed_open',
        'incident_overdue','incident_escalated','daily_return_due','daily_return_missing')),
    ADD CONSTRAINT daily_return_rule CHECK (event NOT IN ('daily_return_due','daily_return_missing')
        OR (queue IS NULL AND forest_class IS NULL AND channel IN ('email', 'in_app')));

ALTER TABLE notifications DROP CONSTRAINT notifications_event_check,
    DROP CONSTRAINT notifications_check,
    ADD COLUMN daily_notice_id bigint REFERENCES daily_return_notices ON DELETE CASCADE,
    ADD CONSTRAINT notifications_event_check CHECK (event IN ('alert_new','alert_overdue','feed_open',
        'incident_overdue','incident_escalated','daily_return_due','daily_return_missing')),
    ADD CONSTRAINT notifications_check CHECK (
        num_nonnulls(alert_id, feed_alert_id, daily_notice_id) = 1
        AND (event = 'feed_open') = (feed_alert_id IS NOT NULL)
        AND (event IN ('daily_return_due','daily_return_missing')) = (daily_notice_id IS NOT NULL));

CREATE OR REPLACE FUNCTION notification_visible(n notifications) RETURNS boolean LANGUAGE sql STABLE AS '
    SELECT EXISTS (
        SELECT FROM alert_grants g LEFT JOIN alerts a ON a.id = n.alert_id
        LEFT JOIN daily_return_notices d ON d.id = n.daily_notice_id
        LEFT JOIN daily_return_policies p ON p.id = d.policy_id
        WHERE g.workspace_id = n.workspace_id AND g.user_id = n.recipient_id
          AND CASE
              WHEN d.id IS NOT NULL THEN g.depth = 0
                 OR (g.hierarchy_id = p.hierarchy_id AND d.unit_path <@ g.unit_path)
              WHEN n.alert_id IS NULL THEN g.depth = 0
              ELSE alert_path_visible(a.queue, a.hierarchy_unit_paths, g.can_review, g.hierarchy_id, g.unit_path, g.depth)
          END
    )';

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM daily_return_policies) THEN
        RAISE EXCEPTION 'Downgrading would discard daily return history.';
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE FUNCTION notification_visible(n notifications) RETURNS boolean LANGUAGE sql STABLE AS '
    SELECT EXISTS (
        SELECT FROM alert_grants g LEFT JOIN alerts a ON a.id = n.alert_id
        WHERE g.workspace_id = n.workspace_id AND g.user_id = n.recipient_id
          AND (CASE WHEN n.alert_id IS NULL THEN g.depth = 0 ELSE
              alert_path_visible(a.queue, a.hierarchy_unit_paths, g.can_review, g.hierarchy_id, g.unit_path, g.depth) END)
    )';

ALTER TABLE notifications DROP CONSTRAINT notifications_check, DROP COLUMN daily_notice_id,
    DROP CONSTRAINT notifications_event_check,
    ADD CONSTRAINT notifications_event_check CHECK (event IN ('alert_new','alert_overdue','feed_open','incident_overdue','incident_escalated')),
    ADD CONSTRAINT notifications_check CHECK ((event = 'feed_open' AND alert_id IS NULL AND feed_alert_id IS NOT NULL)
        OR (event <> 'feed_open' AND alert_id IS NOT NULL AND feed_alert_id IS NULL));
DELETE FROM notification_rules WHERE event IN ('daily_return_due','daily_return_missing');
ALTER TABLE notification_rules DROP CONSTRAINT daily_return_rule, DROP CONSTRAINT notification_rules_event_check,
    ADD CONSTRAINT notification_rules_event_check CHECK (event IN ('alert_new','alert_overdue','feed_open','incident_overdue','incident_escalated'));

DROP TABLE daily_return_notices;
DROP FUNCTION daily_return_units(uuid, smallint, timestamptz);
DROP FUNCTION daily_return_cutoff(date, time, text);
DROP TRIGGER daily_return_revision_fixed ON situation_reports;
DROP FUNCTION check_daily_return_revision();
DROP TABLE daily_return_decisions;
ALTER TABLE situation_reports DROP CONSTRAINT daily_return_shape, DROP CONSTRAINT daily_return_revision,
    DROP COLUMN daily_previous_id, DROP COLUMN daily_revision, DROP COLUMN daily_policy_id;
DROP TABLE daily_return_policies;
