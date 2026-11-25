-- Field devices keep their owner, a revocation or wipe stays, push addresses
-- belong to one active device, and a wiped device is recognised by its ID or
-- its sign-in session.

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO users (id, oidc_subject, username, display_name) VALUES
    ('00000000-0000-0000-0000-00000000de01', 'device-test-subject', 'device.test', 'Device Test'),
    ('00000000-0000-0000-0000-00000000de02', 'device-other-subject', 'device.other', 'Device Other');
INSERT INTO devices (id, user_id, platform, app_version, push_provider, push_endpoint, push_public_key, push_auth, session_id)
VALUES ('00000000-0000-4000-8000-00000000de11', '00000000-0000-0000-0000-00000000de01', 'android', '0.2.0+2',
        'unifiedpush', 'https://push.example.invalid/upOne', decode(repeat('04', 65), 'hex'), decode(repeat('aa', 16), 'hex'), 'sid-one'),
       ('00000000-0000-4000-8000-00000000de12', '00000000-0000-0000-0000-00000000de01', 'android', '0.2.0+2',
        NULL, NULL, NULL, NULL, 'sid-two');

DO $$
BEGIN
    IF NOT (SELECT device_can_push(d) FROM devices d WHERE id = '00000000-0000-4000-8000-00000000de11') THEN
        RAISE EXCEPTION 'a registered device with a push address should receive pushes';
    END IF;
    IF (SELECT device_can_push(d) FROM devices d WHERE id = '00000000-0000-4000-8000-00000000de12') THEN
        RAISE EXCEPTION 'a device without a push address should not receive pushes';
    END IF;
    IF device_contact('device-test-subject', '00000000-0000-4000-8000-00000000de11', 'sid-one') IS NOT NULL
       OR device_contact('device-other-subject', '00000000-0000-4000-8000-00000000de11', NULL) IS NOT NULL THEN
        RAISE EXCEPTION 'an active device, or another user naming it, should carry on';
    END IF;
END $$;

DO $$
BEGIN
    INSERT INTO devices (id, user_id, platform, app_version, push_provider, push_endpoint, push_public_key, push_auth)
    VALUES (gen_random_uuid(), '00000000-0000-0000-0000-00000000de02', 'android', '1', 'unifiedpush',
            'https://push.example.invalid/upOne', decode(repeat('04', 65), 'hex'), decode(repeat('aa', 16), 'hex'));
    RAISE EXCEPTION 'two active devices should not share a push address';
EXCEPTION WHEN unique_violation THEN NULL;
END $$;

DO $$
BEGIN
    INSERT INTO devices (id, user_id, platform, app_version, push_provider, push_endpoint)
    VALUES (gen_random_uuid(), '00000000-0000-0000-0000-00000000de02', 'android', '1', 'unifiedpush', 'https://push.example.invalid/upTwo');
    RAISE EXCEPTION 'a UnifiedPush address without encryption keys should be rejected';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

DO $$
BEGIN
    UPDATE devices SET user_id = '00000000-0000-0000-0000-00000000de02' WHERE id = '00000000-0000-4000-8000-00000000de11';
    RAISE EXCEPTION 'a device should keep its owner';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

-- Revoking: the device is told so by its ID, and a revocation stays.
UPDATE devices SET revoked_at = now(), revoked_reason = 'signed_out' WHERE id = '00000000-0000-4000-8000-00000000de12';
DO $$
BEGIN
    IF device_contact('device-test-subject', '00000000-0000-4000-8000-00000000de12', 'sid-two') IS DISTINCT FROM 'revoked' THEN
        RAISE EXCEPTION 'a revoked device should be told it was removed';
    END IF;
    UPDATE devices SET revoked_at = NULL, revoked_reason = NULL WHERE id = '00000000-0000-4000-8000-00000000de12';
    RAISE EXCEPTION 'a revocation should stay';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

-- Wiping: the device is told by its ID or its session, the first signal is
-- audited once, and the request cannot be withdrawn.
UPDATE devices SET wipe_requested_at = now() WHERE id = '00000000-0000-4000-8000-00000000de11';
DO $$
BEGIN
    IF device_contact('device-test-subject', NULL, 'sid-one') IS DISTINCT FROM 'wipe'
       OR device_contact('device-test-subject', '00000000-0000-4000-8000-00000000de11', NULL) IS DISTINCT FROM 'wipe' THEN
        RAISE EXCEPTION 'a wiped device should be told to wipe by its session or its ID';
    END IF;
    IF device_contact('device-other-subject', NULL, 'sid-one') IS NOT NULL THEN
        RAISE EXCEPTION 'another user with the same session text should carry on';
    END IF;
    IF (SELECT count(*) FROM audit_log WHERE action = 'device.wipe_signal'
        AND target_id = '00000000-0000-4000-8000-00000000de11') <> 1 THEN
        RAISE EXCEPTION 'the first wipe signal should be audited once';
    END IF;
    IF (SELECT device_can_push(d) FROM devices d WHERE id = '00000000-0000-4000-8000-00000000de11') THEN
        RAISE EXCEPTION 'a wiped device should not receive alert pushes';
    END IF;
    UPDATE devices SET wipe_requested_at = NULL WHERE id = '00000000-0000-4000-8000-00000000de11';
    RAISE EXCEPTION 'a wipe request should not be withdrawn';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

DO $$
BEGIN
    INSERT INTO notification_rules (workspace_id, event, recipients, channel)
    SELECT id, 'feed_open', 'root', 'push' FROM workspaces LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'feed health should not go out as a push';
    END IF;
EXCEPTION WHEN check_violation THEN NULL;
END $$;

ROLLBACK;
