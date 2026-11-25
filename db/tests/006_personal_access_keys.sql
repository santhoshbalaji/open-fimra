-- Personal access keys keep their hash, owner and expiry. Only last use and
-- revocation change, and a revoked key stays revoked.

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO users (id, oidc_subject, username, display_name)
VALUES ('00000000-0000-0000-0000-00000000ac01', 'key-test-subject', 'key.test', 'Key Test');
INSERT INTO personal_access_keys (id, user_id, name, key_hash, hint, expires_at)
VALUES ('00000000-0000-0000-0000-00000000ac11', '00000000-0000-0000-0000-00000000ac01', 'Test laptop',
        sha256('fimra_test-key'), 'test-k', now() + interval '1 day');

-- Recording use and revoking are allowed.
UPDATE personal_access_keys SET last_used_at = now() WHERE id = '00000000-0000-0000-0000-00000000ac11';
UPDATE personal_access_keys SET revoked_at = now() WHERE id = '00000000-0000-0000-0000-00000000ac11';

DO $$
BEGIN
    UPDATE personal_access_keys SET revoked_at = NULL WHERE id = '00000000-0000-0000-0000-00000000ac11';
    RAISE EXCEPTION 'a revoked key should stay revoked';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

DO $$
BEGIN
    UPDATE personal_access_keys SET expires_at = now() + interval '1 year' WHERE id = '00000000-0000-0000-0000-00000000ac11';
    RAISE EXCEPTION 'the expiry should not change';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

DO $$
BEGIN
    UPDATE personal_access_keys SET key_hash = sha256('fimra_other') WHERE id = '00000000-0000-0000-0000-00000000ac11';
    RAISE EXCEPTION 'the hash should not change';
EXCEPTION WHEN insufficient_privilege THEN NULL;
END $$;

DO $$
BEGIN
    INSERT INTO personal_access_keys (user_id, name, key_hash, hint)
    VALUES ('00000000-0000-0000-0000-00000000ac01', 'Short hash', '\x01', 'test-k');
    RAISE EXCEPTION 'a hash that is not 32 bytes should be rejected';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

DO $$
BEGIN
    INSERT INTO personal_access_keys (user_id, name, key_hash, hint, created_at, expires_at)
    VALUES ('00000000-0000-0000-0000-00000000ac01', ' Padded ', sha256('fimra_third'), 'test-k', now(), now());
    RAISE EXCEPTION 'a padded name and an expiry at creation should be rejected';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

ROLLBACK;
