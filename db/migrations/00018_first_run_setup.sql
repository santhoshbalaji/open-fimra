-- First-run setup (PLAN.md section 7): the one-time claim of a fresh install,
-- each workspace's wizard progress, encrypted workspace secrets such as the
-- FIRMS map key, and pending invitations that become role grants on sign-in.

-- +goose Up
-- One row at most, ever: the first administrator's claim of a fresh install.
CREATE TABLE install_setup (
    singleton     boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    workspace_id  uuid REFERENCES workspaces (id) ON DELETE SET NULL,
    -- The OpenID Connect subject of the first administrator.
    claimed_by    text NOT NULL,
    claimed_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE workspace_setup (
    workspace_id  uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    mode          text NOT NULL CHECK (mode IN ('state', 'national')),
    -- The hierarchy the wizard builds. Its top level is the workspace boundary.
    hierarchy_id  uuid NOT NULL REFERENCES hierarchies (id) ON DELETE CASCADE,
    preset        text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz
);

CREATE TABLE workspace_setup_steps (
    workspace_id  uuid NOT NULL REFERENCES workspace_setup (workspace_id) ON DELETE CASCADE,
    step          text NOT NULL CHECK (step IN ('mode', 'workspace', 'hierarchy', 'layers', 'firms', 'forms', 'invitations')),
    status        text NOT NULL CHECK (status IN ('done', 'skipped')),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    updated_by    text NOT NULL,
    PRIMARY KEY (workspace_id, step)
);

-- Secrets are AES-256-GCM ciphertext. The key comes from the environment and
-- never reaches the database; key_id says which key sealed each row.
CREATE TABLE workspace_settings_secrets (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name          text NOT NULL CHECK (name IN ('firms_map_key')),
    key_id        text NOT NULL CHECK (key_id ~ '^[0-9a-f]{16}$'),
    nonce         bytea NOT NULL CHECK (octet_length(nonce) = 12),
    ciphertext    bytea NOT NULL,
    -- At most the last four characters, so administrators can tell keys apart.
    hint          text NOT NULL CHECK (char_length(hint) <= 4),
    set_at        timestamptz NOT NULL DEFAULT now(),
    set_by        text NOT NULL,
    PRIMARY KEY (workspace_id, name)
);

-- An invited address is personal data. The row is deleted when the grant is
-- claimed or the invitation expires, and the address is never logged.
CREATE TABLE invitations (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id  uuid NOT NULL,
    hierarchy_id  uuid NOT NULL,
    unit_path     ltree NOT NULL,
    role_slug     text NOT NULL REFERENCES roles (slug),
    email         text NOT NULL CHECK (email = lower(email) AND char_length(email) <= 254),
    invited_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    email_status  text NOT NULL DEFAULT 'off' CHECK (email_status IN ('off', 'pending', 'sent', 'failed')),
    FOREIGN KEY (workspace_id, hierarchy_id) REFERENCES hierarchies (workspace_id, id) ON DELETE CASCADE,
    CHECK (expires_at > created_at),
    UNIQUE (workspace_id, hierarchy_id, unit_path, role_slug, email)
);

CREATE INDEX invitations_email_idx ON invitations (email);
CREATE INDEX invitations_expiry_idx ON invitations (expires_at);

-- +goose Down
DROP TABLE invitations;
DROP TABLE workspace_settings_secrets;
DROP TABLE workspace_setup_steps;
DROP TABLE workspace_setup;
DROP TABLE install_setup;
