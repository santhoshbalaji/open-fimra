-- Workspaces and hierarchies (PLAN.md sections 5.2 and 8).
--
-- A workspace is one agency's space: a state, or a national body. Each workspace
-- defines one or more hierarchies (territorial, revenue, protected area), each
-- with its own levels. Units are the places in a hierarchy, such as one beat.

-- +goose Up
CREATE TABLE workspaces (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]*$'),
    name        text NOT NULL,
    -- Area of interest. Detections outside it, plus a margin, are not matched.
    boundary    geometry(MultiPolygon, 4326),
    settings    jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE hierarchies (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    slug          text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]*$'),
    name          text NOT NULL,
    kind          text NOT NULL CHECK (kind IN ('territorial', 'revenue', 'protected_area', 'other')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, slug)
);

-- Levels are numbered from 0 (the top, usually the state).
-- Labels hold translations, for example {"en": "Beat", "hi": "बीट"}.
CREATE TABLE hierarchy_levels (
    hierarchy_id  uuid NOT NULL REFERENCES hierarchies (id) ON DELETE CASCADE,
    depth         smallint NOT NULL CHECK (depth >= 0),
    slug          text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9_]*$'),
    labels        jsonb NOT NULL CHECK (jsonb_typeof(labels) = 'object' AND labels ? 'en'),
    PRIMARY KEY (hierarchy_id, depth),
    UNIQUE (hierarchy_id, slug)
);

-- A unit's path holds one ltree label per level, from the top down, so
-- "everything under this division" is a single `path <@ 'x.y'` query.
-- Units are versioned by validity dates: a boundary change closes the old row
-- and opens a new one, so past alerts keep the unit they had at the time.
CREATE TABLE units (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    hierarchy_id      uuid NOT NULL,
    depth             smallint NOT NULL,
    code              text NOT NULL,
    name              text NOT NULL,
    path              ltree NOT NULL,
    geom              geometry(MultiPolygon, 4326),
    -- The layer version this unit was published from. The foreign key is added
    -- with the layers table in the next migration.
    layer_version_id  uuid,
    valid_from        timestamptz NOT NULL DEFAULT now(),
    valid_to          timestamptz,
    FOREIGN KEY (hierarchy_id, depth) REFERENCES hierarchy_levels (hierarchy_id, depth) ON DELETE CASCADE,
    CHECK (nlevel(path) = depth + 1),
    CHECK (valid_to IS NULL OR valid_to > valid_from),
    CHECK (geom IS NULL OR ST_IsValid(geom))
);

-- Only one current row per place in a hierarchy.
CREATE UNIQUE INDEX units_current_path_key ON units (hierarchy_id, path) WHERE valid_to IS NULL;
CREATE INDEX units_path_idx ON units USING gist (path);
CREATE INDEX units_geom_idx ON units USING gist (geom);
CREATE INDEX units_hierarchy_code_idx ON units (hierarchy_id, code);

-- +goose Down
DROP TABLE units;
DROP TABLE hierarchy_levels;
DROP TABLE hierarchies;
DROP TABLE workspaces;
