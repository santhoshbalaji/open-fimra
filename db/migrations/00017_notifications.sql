-- Workspace notification rules, replay-safe inbox and email delivery outbox.
-- +goose Up
ALTER TABLE users ADD COLUMN notification_email text,
                  ADD COLUMN notification_email_muted boolean NOT NULL DEFAULT false;

CREATE TABLE notification_rules (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
    event text NOT NULL CHECK (event IN ('alert_new', 'alert_overdue', 'feed_open')),
    recipients text NOT NULL CHECK (recipients IN ('field_staff', 'division_officer', 'reviewers', 'root')),
    channel text NOT NULL CHECK (channel IN ('in_app', 'email')),
    queue text CHECK (queue IN ('unit', 'edge_review', 'triage')),
    forest_class text CHECK (forest_class IN ('inside', 'near_edge')),
    CHECK (queue IS NULL OR forest_class IS NULL),
    CHECK (event <> 'feed_open' OR (recipients = 'root' AND queue IS NULL AND forest_class IS NULL)),
    UNIQUE NULLS NOT DISTINCT (workspace_id, event, recipients, channel, queue, forest_class)
);

CREATE TABLE notifications (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces ON DELETE CASCADE,
    event text NOT NULL CHECK (event IN ('alert_new', 'alert_overdue', 'feed_open')),
    -- The source ID is the alert, step log entry or feed-health alert, respectively.
    event_id bigint NOT NULL,
    alert_id bigint REFERENCES alerts ON DELETE CASCADE,
    feed_alert_id bigint REFERENCES feed_health_alerts ON DELETE CASCADE,
    recipient_id uuid NOT NULL REFERENCES users ON DELETE CASCADE,
    channel text NOT NULL CHECK (channel IN ('in_app', 'email')),
    status text NOT NULL CHECK (status IN ('pending', 'sending', 'delivered', 'skipped', 'failed', 'uncertain')),
    created_at timestamptz NOT NULL DEFAULT now(),
    read_at timestamptz,
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((event = 'feed_open' AND alert_id IS NULL AND feed_alert_id IS NOT NULL)
        OR (event <> 'feed_open' AND alert_id IS NOT NULL AND feed_alert_id IS NULL)),
    UNIQUE (workspace_id, event, event_id, recipient_id, channel)
);
CREATE INDEX notifications_inbox_idx ON notifications (recipient_id, (read_at IS NOT NULL), id DESC)
    WHERE channel = 'in_app';
CREATE INDEX notifications_outbox_idx ON notifications (next_attempt_at, id)
    WHERE channel = 'email' AND status IN ('pending', 'sending');

CREATE TABLE notification_attempts (
    notification_id bigint NOT NULL REFERENCES notifications ON DELETE CASCADE,
    attempt integer NOT NULL,
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    status text NOT NULL CHECK (status IN ('sending', 'delivered', 'retry', 'failed', 'uncertain')),
    error text,
    PRIMARY KEY (notification_id, attempt)
);

-- Step IDs are immutable. A foreign key would mask the workflow log's own
-- append-only guard on TRUNCATE.
CREATE TABLE notification_overdue_steps (
    step_id bigint PRIMARY KEY
);

-- Both the alert list and notification routing use saved paths and the same review check.
-- +goose StatementBegin
CREATE FUNCTION alert_path_visible(queue text, paths jsonb, can_review boolean,
    hierarchy_id uuid, unit_path ltree, depth smallint)
RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (queue = 'unit' OR can_review) AND match_under_grant(paths, hierarchy_id, unit_path, depth);
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION seed_notification_rules(workspace uuid) RETURNS void LANGUAGE sql AS $$
    INSERT INTO notification_rules(workspace_id, event, recipients, channel, queue)
    SELECT workspace, event, recipients, channel, queue
    FROM (VALUES ('alert_new', 'field_staff', 'unit'), ('alert_new', 'division_officer', 'unit'),
        ('alert_new', 'reviewers', 'edge_review'), ('alert_new', 'reviewers', 'triage'),
        ('alert_overdue', 'division_officer', NULL), ('feed_open', 'root', NULL)) r(event, recipients, queue)
    CROSS JOIN (VALUES ('in_app'), ('email')) c(channel);
$$;
-- +goose StatementEnd
SELECT seed_notification_rules(id) FROM workspaces;

-- +goose StatementBegin
CREATE FUNCTION workspace_notification_defaults() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM seed_notification_rules(NEW.id);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER notification_defaults AFTER INSERT ON workspaces
    FOR EACH ROW EXECUTE FUNCTION workspace_notification_defaults();

-- +goose StatementBegin
CREATE FUNCTION enqueue_alert_notifications(a alerts, kind text, source_id bigint) RETURNS void LANGUAGE sql AS $$
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

-- +goose StatementBegin
CREATE FUNCTION notify_alert_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM enqueue_alert_notifications(NEW, 'alert_new', NEW.id);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER notify_alert_insert AFTER INSERT ON alerts FOR EACH ROW EXECUTE FUNCTION notify_alert_insert();

-- +goose StatementBegin
CREATE FUNCTION notify_feed_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.resolved_at IS NULL THEN
        INSERT INTO notifications(workspace_id, event, event_id, feed_alert_id, recipient_id, channel, status)
        SELECT NEW.workspace_id, 'feed_open', NEW.id, NEW.id, g.user_id, r.channel,
               CASE r.channel WHEN 'email' THEN 'pending' ELSE 'delivered' END
        FROM notification_rules r JOIN alert_grants g USING (workspace_id)
        JOIN users u ON u.id = g.user_id
        WHERE r.workspace_id = NEW.workspace_id AND r.event = 'feed_open' AND g.depth = 0
          AND (r.channel = 'in_app' OR (u.notification_email IS NOT NULL AND NOT u.notification_email_muted))
        ON CONFLICT DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER notify_feed_insert AFTER INSERT ON feed_health_alerts
    FOR EACH ROW EXECUTE FUNCTION notify_feed_insert();

-- Recheck current grants on inbox reads and before sending email.
-- +goose StatementBegin
CREATE FUNCTION notification_visible(n notifications) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT FROM alert_grants g LEFT JOIN alerts a ON a.id = n.alert_id
        WHERE g.workspace_id = n.workspace_id AND g.user_id = n.recipient_id
          AND (CASE WHEN n.alert_id IS NULL THEN g.depth = 0 ELSE
              alert_path_visible(a.queue, a.hierarchy_unit_paths, g.can_review, g.hierarchy_id, g.unit_path, g.depth) END)
    );
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION notification_visible(notifications);
DROP TRIGGER notify_feed_insert ON feed_health_alerts;
DROP FUNCTION notify_feed_insert();
DROP TRIGGER notify_alert_insert ON alerts;
DROP FUNCTION notify_alert_insert(), enqueue_alert_notifications(alerts, text, bigint);
DROP TRIGGER notification_defaults ON workspaces;
DROP FUNCTION workspace_notification_defaults(), seed_notification_rules(uuid);
DROP FUNCTION alert_path_visible(text, jsonb, boolean, uuid, ltree, smallint);
DROP TABLE notification_overdue_steps, notification_attempts, notifications, notification_rules;
ALTER TABLE users DROP COLUMN notification_email, DROP COLUMN notification_email_muted;
