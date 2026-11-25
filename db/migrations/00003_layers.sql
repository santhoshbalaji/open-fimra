-- Layers and their versions (PLAN.md section 5.2).
--
-- A layer has a role: forest (decides which detections count), hierarchy
-- (decides who is responsible), exclusion (known heat sources) or analysis.
-- Every upload becomes a draft version. Publishing a version makes it the one
-- used for matching; older versions are kept so past results can be reproduced.

-- +goose Up
CREATE TABLE layers (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    slug          text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]*$'),
    name          text NOT NULL,
    role          text NOT NULL CHECK (role IN ('forest', 'hierarchy', 'exclusion', 'analysis')),
    -- Hierarchy layers say which hierarchy they fill.
    hierarchy_id  uuid REFERENCES hierarchies (id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, slug),
    CHECK ((role = 'hierarchy') = (hierarchy_id IS NOT NULL))
);

CREATE TABLE layer_versions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    layer_id        uuid NOT NULL REFERENCES layers (id) ON DELETE CASCADE,
    version         integer NOT NULL CHECK (version > 0),
    status          text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'retired')),
    source_file     text,
    -- Saved column mapping and import settings, so the next upload can reuse them.
    import_settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    notes           text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    published_at    timestamptz,
    retired_at      timestamptz,
    UNIQUE (layer_id, version),
    CHECK ((status = 'draft') = (published_at IS NULL)),
    CHECK ((status = 'retired') = (retired_at IS NOT NULL))
);

-- At most one published version per layer.
CREATE UNIQUE INDEX layer_versions_one_published_key ON layer_versions (layer_id) WHERE status = 'published';

-- Features of forest, exclusion and analysis layers. Hierarchy layers become
-- units instead. Geometry is stored in WGS 84; areas are worked out in EPSG:7755.
CREATE TABLE layer_features (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    layer_version_id  uuid NOT NULL REFERENCES layer_versions (id) ON DELETE CASCADE,
    code              text,
    name              text,
    attributes        jsonb NOT NULL DEFAULT '{}'::jsonb,
    geom              geometry(Geometry, 4326) NOT NULL CHECK (ST_IsValid(geom))
);

CREATE INDEX layer_features_version_idx ON layer_features (layer_version_id);
CREATE INDEX layer_features_geom_idx ON layer_features USING gist (geom);

ALTER TABLE units
    ADD CONSTRAINT units_layer_version_fkey
    FOREIGN KEY (layer_version_id) REFERENCES layer_versions (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE units DROP CONSTRAINT units_layer_version_fkey;
DROP TABLE layer_features;
DROP TABLE layer_versions;
DROP TABLE layers;
