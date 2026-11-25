-- +goose Up
-- The worker claims a row and saves the result in the same transaction.
CREATE TABLE geo_jobs (
    layer_version_id uuid PRIMARY KEY REFERENCES layer_versions (id) ON DELETE CASCADE,
    file_key         text NOT NULL,
    status           text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'failed')),
    created_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz,
    source_crs       text,
    CHECK ((status = 'pending') = (finished_at IS NULL))
);

CREATE INDEX geo_jobs_pending_idx ON geo_jobs (created_at, layer_version_id) WHERE status = 'pending';

CREATE TABLE layer_import_problems (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    layer_version_id uuid NOT NULL REFERENCES layer_versions (id) ON DELETE CASCADE,
    kind             text NOT NULL,
    feature_ref      text,
    message          text NOT NULL,
    blocks_publish   boolean NOT NULL
);

CREATE INDEX layer_import_problems_version_idx ON layer_import_problems (layer_version_id, id);

-- Unresolved paths and parents can be saved for review. Drafts never enter units.
CREATE TABLE draft_units (
    layer_version_id uuid NOT NULL REFERENCES layer_versions (id) ON DELETE CASCADE,
    feature_number   integer NOT NULL CHECK (feature_number > 0),
    depth            smallint NOT NULL CHECK (depth >= 0),
    code             text NOT NULL,
    name             text NOT NULL,
    path             ltree,
    parent_code      text,
    parent_path      ltree,
    geom             geometry(MultiPolygon, 4326) NOT NULL CHECK (ST_IsValid(geom)),
    area_m2          double precision NOT NULL CHECK (area_m2 >= 0),
    PRIMARY KEY (layer_version_id, feature_number),
    CHECK (path IS NULL OR nlevel(path) = depth + 1),
    CHECK (parent_path IS NULL OR nlevel(parent_path) = depth)
);

-- Published rows and older seed data can leave these import fields empty.
ALTER TABLE layer_features
    ADD COLUMN source_feature_number integer CHECK (source_feature_number > 0),
    ADD COLUMN area_m2 double precision CHECK (area_m2 >= 0);

-- +goose Down
ALTER TABLE layer_features DROP COLUMN area_m2, DROP COLUMN source_feature_number;
DROP TABLE draft_units;
DROP TABLE layer_import_problems;
DROP TABLE geo_jobs;
