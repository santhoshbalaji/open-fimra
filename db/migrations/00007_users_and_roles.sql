-- Users and roles granted at a place in a hierarchy (PLAN.md section 6).
--
-- A user is known by their OpenID Connect subject. A role assignment gives a
-- user a role at one current unit, such as a division; the role covers every
-- unit under that path.

-- +goose Up
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    oidc_subject  text NOT NULL UNIQUE,
    username      text NOT NULL,
    display_name  text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE roles (
    slug         text PRIMARY KEY,
    description  text NOT NULL
);

INSERT INTO roles (slug, description) VALUES
    ('state_admin', 'Manages access and settings for a state.'),
    ('division_officer', 'Coordinates fire response in a division.'),
    ('field_staff', 'Reports and follows up on fires in the assigned area.');

ALTER TABLE hierarchies
    ADD CONSTRAINT hierarchies_workspace_id_key UNIQUE (workspace_id, id);

-- Give grants a key that points to the current unit and ignores past versions.
ALTER TABLE units
    ADD COLUMN current_path ltree GENERATED ALWAYS AS (
        CASE WHEN valid_to IS NULL THEN path END
    ) STORED,
    ADD CONSTRAINT units_current_grant_path_key UNIQUE (hierarchy_id, current_path);

CREATE TABLE role_assignments (
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id  uuid NOT NULL,
    hierarchy_id  uuid NOT NULL,
    unit_path     ltree NOT NULL,
    role_slug     text NOT NULL REFERENCES roles (slug),
    granted_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    granted_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, workspace_id, hierarchy_id, unit_path, role_slug),
    FOREIGN KEY (workspace_id, hierarchy_id)
        REFERENCES hierarchies (workspace_id, id) ON DELETE CASCADE,
    -- Check at commit so a new version can replace the unit at the same path.
    CONSTRAINT role_assignments_current_unit_fkey
        FOREIGN KEY (hierarchy_id, unit_path)
        REFERENCES units (hierarchy_id, current_path) DEFERRABLE INITIALLY DEFERRED
);

CREATE INDEX role_assignments_hierarchy_path_idx ON role_assignments (hierarchy_id, unit_path);

-- +goose Down
DROP TABLE role_assignments;
ALTER TABLE units DROP COLUMN current_path;
ALTER TABLE hierarchies DROP CONSTRAINT hierarchies_workspace_id_key;
DROP TABLE roles;
DROP TABLE users;
