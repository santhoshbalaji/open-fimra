-- Field phones: device registration, push notifications and remote wipe
-- (PLAN.md sections 5.4 and 6).
--
-- A device is one sign-in of the field app on one phone. The app chooses its
-- ID when the user signs in, so sending the same registration twice is safe,
-- and it makes a new one after sign-out or a wipe. A device is never deleted
-- while its owner exists, only revoked or wiped, so the audit log can always
-- point at it.
--
-- Push messages carry only IDs and a short generic text. The app fetches the
-- details over the signed-in API.

-- +goose Up
CREATE TABLE devices (
    id                uuid PRIMARY KEY,
    user_id           uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    platform          text NOT NULL CHECK (platform IN ('android', 'ios', 'web')),
    app_version       text NOT NULL CHECK (app_version ~ '^[0-9A-Za-z.+_-]{1,40}$'),
    -- Where push messages go: a UnifiedPush endpoint URL, or a Firebase
    -- Cloud Messaging registration token when that optional service is on.
    push_provider     text CHECK (push_provider IN ('unifiedpush', 'fcm')),
    push_endpoint     text CHECK (length(push_endpoint) BETWEEN 1 AND 4096),
    -- Web Push keys (RFC 8291) for encrypting UnifiedPush messages.
    push_public_key   bytea CHECK (length(push_public_key) = 65),
    push_auth         bytea CHECK (length(push_auth) = 16),
    -- The sign-in session (the token's sid claim) the device last used.
    session_id        text CHECK (length(session_id) BETWEEN 1 AND 200),
    registered_at     timestamptz NOT NULL DEFAULT now(),
    last_seen_at      timestamptz NOT NULL DEFAULT now(),
    revoked_at        timestamptz,
    revoked_reason    text CHECK (revoked_reason IN ('owner', 'signed_out', 'replaced')),
    wipe_requested_at timestamptz,
    wipe_requested_by uuid REFERENCES users (id) ON DELETE SET NULL,
    -- When the API first told the device to wipe, and when the app said it had.
    wipe_signalled_at timestamptz,
    wiped_at          timestamptz,
    CHECK ((push_provider IS NULL) = (push_endpoint IS NULL)),
    CHECK ((push_public_key IS NULL) = (push_auth IS NULL)),
    CHECK (push_provider IS DISTINCT FROM 'unifiedpush' OR push_public_key IS NOT NULL),
    CHECK (push_public_key IS NULL OR push_provider = 'unifiedpush'),
    CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL)),
    CHECK (wipe_requested_at IS NOT NULL OR (wipe_signalled_at IS NULL AND wiped_at IS NULL))
);

CREATE INDEX devices_user_idx ON devices (user_id, last_seen_at DESC);
-- One push address belongs to one active device.
CREATE UNIQUE INDEX devices_active_push_key ON devices (push_endpoint)
    WHERE revoked_at IS NULL AND wipe_requested_at IS NULL;
-- A wiped device's sign-in session is refused even without its device ID.
CREATE INDEX devices_wiped_session_idx ON devices (session_id) WHERE wipe_requested_at IS NOT NULL;

-- +goose StatementBegin
-- The owner and registration time are fixed, a revocation stays, and a wipe
-- request cannot be withdrawn.
CREATE FUNCTION devices_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.user_id, NEW.registered_at) IS DISTINCT FROM (OLD.id, OLD.user_id, OLD.registered_at)
       OR (OLD.revoked_at IS NOT NULL AND (NEW.revoked_at, NEW.revoked_reason) IS DISTINCT FROM (OLD.revoked_at, OLD.revoked_reason))
       OR (OLD.wipe_requested_at IS NOT NULL AND NEW.wipe_requested_at IS DISTINCT FROM OLD.wipe_requested_at)
       OR (OLD.wiped_at IS NOT NULL AND NEW.wiped_at IS DISTINCT FROM OLD.wiped_at) THEN
        RAISE EXCEPTION 'a device keeps its owner, and its revocation or wipe cannot be undone'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER devices_fixed BEFORE UPDATE ON devices FOR EACH ROW EXECUTE FUNCTION devices_guard();

-- A device can receive alert pushes while it is registered, not wiped and has a push address.
-- +goose StatementBegin
CREATE FUNCTION device_can_push(d devices) RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT d.revoked_at IS NULL AND d.wipe_requested_at IS NULL AND d.push_endpoint IS NOT NULL;
$$;
-- +goose StatementEnd

-- One message to one device. An alert notification has one row per active
-- device of its recipient, made when the notification is made, so a replayed
-- feed or a later registration never sends it again. A device check asks a
-- device to contact the API, for example after a wipe request.
CREATE TABLE push_messages (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    device_id       uuid NOT NULL REFERENCES devices (id) ON DELETE CASCADE,
    kind            text NOT NULL CHECK (kind IN ('notification', 'device_check')),
    notification_id bigint REFERENCES notifications (id) ON DELETE CASCADE,
    status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'sending', 'delivered', 'skipped', 'failed')),
    attempts        integer NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz,
    error           text,
    CHECK ((kind = 'notification') = (notification_id IS NOT NULL)),
    UNIQUE (notification_id, device_id)
);
CREATE INDEX push_messages_outbox_idx ON push_messages (next_attempt_at, id) WHERE status IN ('pending', 'sending');
CREATE INDEX push_messages_device_idx ON push_messages (device_id);

-- Push is a third notification channel. Feed health stays with in-app and email.
ALTER TABLE notification_rules DROP CONSTRAINT notification_rules_channel_check,
    ADD CONSTRAINT notification_rules_channel_check CHECK (channel IN ('in_app', 'email', 'push')),
    ADD CONSTRAINT notification_rules_push_check CHECK (channel <> 'push' OR event <> 'feed_open');
ALTER TABLE notifications DROP CONSTRAINT notifications_channel_check,
    ADD CONSTRAINT notifications_channel_check CHECK (channel IN ('in_app', 'email', 'push'));

-- New workspaces also push new alerts to the field staff of the unit.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION seed_notification_rules(workspace uuid) RETURNS void LANGUAGE sql AS $$
    INSERT INTO notification_rules(workspace_id, event, recipients, channel, queue)
    SELECT workspace, event, recipients, channel, queue
    FROM (VALUES ('alert_new', 'field_staff', 'unit'), ('alert_new', 'division_officer', 'unit'),
        ('alert_new', 'reviewers', 'edge_review'), ('alert_new', 'reviewers', 'triage'),
        ('alert_overdue', 'division_officer', NULL), ('feed_open', 'root', NULL)) r(event, recipients, queue)
    CROSS JOIN (VALUES ('in_app'), ('email')) c(channel)
    UNION ALL SELECT workspace, 'alert_new', 'field_staff', 'push', 'unit';
$$;
-- +goose StatementEnd
INSERT INTO notification_rules(workspace_id, event, recipients, channel, queue)
SELECT id, 'alert_new', 'field_staff', 'push', 'unit' FROM workspaces ON CONFLICT DO NOTHING;

-- The rule from migration 00021, with push: one push notification per
-- incident and recipient, made only for a recipient with a device that can
-- receive it.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enqueue_alert_notifications(a alerts, kind text, source_id bigint) RETURNS void LANGUAGE sql AS $$
    INSERT INTO notifications(workspace_id, event, event_id, alert_id, recipient_id, channel, status)
    SELECT a.workspace_id, kind, source_id, a.id, g.user_id, r.channel,
           CASE r.channel WHEN 'in_app' THEN 'delivered' ELSE 'pending' END
    FROM notification_rules r JOIN alert_grants g USING (workspace_id)
    JOIN users u ON u.id = g.user_id
    WHERE r.workspace_id = a.workspace_id AND r.event = kind
      AND (r.queue IS NULL OR r.queue = a.queue)
      AND (r.forest_class IS NULL OR r.forest_class = a.forest_class)
      AND CASE r.recipients WHEN 'reviewers' THEN g.can_review WHEN 'root' THEN g.depth = 0
                            WHEN 'division_officer' THEN g.role_slug = 'division_officer' AND g.can_review
                            ELSE g.role_slug = r.recipients END
      AND alert_path_visible(a.queue, a.hierarchy_unit_paths, g.can_review, g.hierarchy_id, g.unit_path, g.depth)
      AND CASE r.channel WHEN 'in_app' THEN true
                         WHEN 'email' THEN u.notification_email IS NOT NULL AND NOT u.notification_email_muted
                         ELSE EXISTS (SELECT FROM devices d WHERE d.user_id = g.user_id AND device_can_push(d)) END
      AND (kind <> 'alert_new' OR NOT EXISTS (
          SELECT FROM incident_members mine
          JOIN incident_members other ON other.incident_id = mine.incident_id AND other.alert_id <> a.id
          JOIN notifications earlier ON earlier.alert_id = other.alert_id AND earlier.event = 'alert_new'
          WHERE mine.alert_id = a.id AND earlier.recipient_id = g.user_id AND earlier.channel = r.channel))
    ON CONFLICT DO NOTHING;
$$;
-- +goose StatementEnd

-- A new push notification gets one message per device that can receive it.
-- +goose StatementBegin
CREATE FUNCTION queue_push_messages() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO push_messages(device_id, kind, notification_id)
    SELECT d.id, 'notification', n.id
    FROM added n JOIN devices d ON d.user_id = n.recipient_id
    WHERE n.channel = 'push' AND device_can_push(d)
    ON CONFLICT DO NOTHING;
    UPDATE notifications n SET status = 'skipped'
    FROM added
    WHERE n.id = added.id AND added.channel = 'push'
      AND NOT EXISTS (SELECT FROM push_messages p WHERE p.notification_id = added.id);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER queue_push_messages AFTER INSERT ON notifications REFERENCING NEW TABLE AS added
    FOR EACH STATEMENT EXECUTE FUNCTION queue_push_messages();

-- Records a signed-in call from a device and says what the app must do:
-- 'wipe', 'revoked' or NULL to carry on. A wiped device is recognised by its
-- ID or by its sign-in session, so a call without the device ID is caught
-- too. The first wipe signal is audited.
-- +goose StatementBegin
CREATE FUNCTION device_contact(subject text, device uuid, session text) RETURNS text LANGUAGE plpgsql AS $$
DECLARE
    hit devices;
BEGIN
    IF device IS NULL AND session IS NULL THEN
        RETURN NULL;
    END IF;
    SELECT d.* INTO hit FROM devices d JOIN users u ON u.id = d.user_id
    WHERE u.oidc_subject = subject AND d.wipe_requested_at IS NOT NULL
      AND (d.id = device OR d.session_id = session)
    ORDER BY d.id = device DESC NULLS LAST, d.wipe_requested_at LIMIT 1;
    IF FOUND THEN
        IF hit.wipe_signalled_at IS NULL THEN
            UPDATE devices SET wipe_signalled_at = now() WHERE id = hit.id;
            INSERT INTO audit_log(actor_kind, actor_id, action, target_type, target_id)
            VALUES ('user', subject, 'device.wipe_signal', 'device', hit.id::text);
        END IF;
        RETURN 'wipe';
    END IF;
    IF device IS NULL THEN
        RETURN NULL;
    END IF;
    SELECT d.* INTO hit FROM devices d JOIN users u ON u.id = d.user_id
    WHERE d.id = device AND u.oidc_subject = subject;
    IF NOT FOUND THEN
        RETURN NULL;
    ELSIF hit.revoked_at IS NOT NULL THEN
        RETURN 'revoked';
    END IF;
    IF hit.last_seen_at < now() - interval '1 minute' OR hit.session_id IS DISTINCT FROM COALESCE(session, hit.session_id) THEN
        UPDATE devices SET last_seen_at = now(), session_id = COALESCE(session, session_id) WHERE id = device;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- The devices an administrator may see and wipe in a workspace: those of
-- users with a role at or below one of the administrator's units. State
-- administrators and division officers manage devices. A root state
-- administrator sees the whole workspace.
-- +goose StatementBegin
CREATE FUNCTION manageable_devices(subject text, workspace_slug text)
RETURNS TABLE(device_id uuid, workspace_id uuid) LANGUAGE sql STABLE AS $$
    SELECT DISTINCT d.id, admin.workspace_id
    FROM alert_grants admin
    JOIN role_assignments owner ON owner.workspace_id = admin.workspace_id
      AND ((admin.role_slug = 'state_admin' AND admin.depth = 0)
        OR (owner.hierarchy_id = admin.hierarchy_id AND owner.unit_path <@ admin.unit_path))
    JOIN devices d ON d.user_id = owner.user_id
    WHERE admin.oidc_subject = subject AND admin.slug = workspace_slug
      AND (admin.role_slug = 'state_admin' OR (admin.role_slug = 'division_officer' AND admin.can_review));
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION manageable_devices(text, text);
DROP FUNCTION device_contact(text, uuid, text);
DROP TRIGGER queue_push_messages ON notifications;
DROP FUNCTION queue_push_messages();
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION enqueue_alert_notifications(a alerts, kind text, source_id bigint) RETURNS void LANGUAGE sql AS $$
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
      AND (kind <> 'alert_new' OR NOT EXISTS (
          SELECT FROM incident_members mine
          JOIN incident_members other ON other.incident_id = mine.incident_id AND other.alert_id <> a.id
          JOIN notifications earlier ON earlier.alert_id = other.alert_id AND earlier.event = 'alert_new'
          WHERE mine.alert_id = a.id AND earlier.recipient_id = g.user_id AND earlier.channel = r.channel))
    ON CONFLICT DO NOTHING;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION seed_notification_rules(workspace uuid) RETURNS void LANGUAGE sql AS $$
    INSERT INTO notification_rules(workspace_id, event, recipients, channel, queue)
    SELECT workspace, event, recipients, channel, queue
    FROM (VALUES ('alert_new', 'field_staff', 'unit'), ('alert_new', 'division_officer', 'unit'),
        ('alert_new', 'reviewers', 'edge_review'), ('alert_new', 'reviewers', 'triage'),
        ('alert_overdue', 'division_officer', NULL), ('feed_open', 'root', NULL)) r(event, recipients, queue)
    CROSS JOIN (VALUES ('in_app'), ('email')) c(channel);
$$;
-- +goose StatementEnd
DELETE FROM notifications WHERE channel = 'push';
DELETE FROM notification_rules WHERE channel = 'push';
ALTER TABLE notifications DROP CONSTRAINT notifications_channel_check,
    ADD CONSTRAINT notifications_channel_check CHECK (channel IN ('in_app', 'email'));
ALTER TABLE notification_rules DROP CONSTRAINT notification_rules_push_check,
    DROP CONSTRAINT notification_rules_channel_check,
    ADD CONSTRAINT notification_rules_channel_check CHECK (channel IN ('in_app', 'email'));
DROP TABLE push_messages;
DROP FUNCTION device_can_push(devices);
DROP TABLE devices;
DROP FUNCTION devices_guard();
