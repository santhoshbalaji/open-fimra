-- Append-only audit log (PLAN.md section 6): sign-ins, changes, workflow steps
-- and exports. Rows can be added but never changed or deleted.

-- +goose Up
CREATE TABLE audit_log (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    workspace_id  uuid REFERENCES workspaces (id) ON DELETE SET NULL,
    -- Who acted: a user (their OpenID Connect subject), an API key or the system.
    actor_kind    text NOT NULL CHECK (actor_kind IN ('user', 'api_key', 'system')),
    actor_id      text,
    -- What happened, as a dotted name such as layer.publish or export.create.
    action        text NOT NULL CHECK (action ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$'),
    target_type   text,
    target_id     text,
    details       jsonb NOT NULL DEFAULT '{}'::jsonb,
    ip_address    inet,
    CHECK ((actor_kind = 'system') = (actor_id IS NULL))
);

CREATE INDEX audit_log_workspace_idx ON audit_log (workspace_id, occurred_at DESC);
CREATE INDEX audit_log_target_idx ON audit_log (target_type, target_id);

-- +goose StatementBegin
CREATE FUNCTION audit_log_reject_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER audit_log_append_only
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_reject_change();

CREATE TRIGGER audit_log_no_truncate
    BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_reject_change();

-- +goose Down
DROP TABLE audit_log;
DROP FUNCTION audit_log_reject_change();
