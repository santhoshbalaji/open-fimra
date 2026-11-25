-- Personal access keys for the read-only GIS link (PLAN.md sections 5.6 and 6).
--
-- Desktop GIS such as QGIS cannot easily sign in through the browser, so a
-- signed-in user can make a key for it. A key acts with exactly its owner's
-- current grants, and only on the OGC API routes. Only a SHA-256 hash of the
-- key is stored; the key itself is shown once when it is made. A key is never
-- deleted, only revoked, so the audit log can always point at it.

-- +goose Up
CREATE TABLE personal_access_keys (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name          text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 100),
    key_hash      bytea NOT NULL UNIQUE CHECK (length(key_hash) = 32),
    -- The first characters of the key, so the owner can tell keys apart.
    hint          text NOT NULL CHECK (hint ~ '^[A-Za-z0-9_-]{4,16}$'),
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz CHECK (expires_at > created_at),
    last_used_at  timestamptz,
    revoked_at    timestamptz,
    CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE INDEX personal_access_keys_user_idx ON personal_access_keys (user_id, created_at DESC);

-- +goose StatementBegin
-- The hash, owner, name, hint and times are fixed. Only last use and
-- revocation change, and a revoked key stays revoked.
CREATE FUNCTION personal_access_keys_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.user_id, NEW.name, NEW.key_hash, NEW.hint, NEW.created_at, NEW.expires_at)
       IS DISTINCT FROM (OLD.id, OLD.user_id, OLD.name, OLD.key_hash, OLD.hint, OLD.created_at, OLD.expires_at)
       OR (OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at) THEN
        RAISE EXCEPTION 'personal access keys can only record use or be revoked'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER personal_access_keys_fixed
    BEFORE UPDATE ON personal_access_keys
    FOR EACH ROW EXECUTE FUNCTION personal_access_keys_guard();

-- +goose Down
DROP TABLE personal_access_keys;
DROP FUNCTION personal_access_keys_guard();
