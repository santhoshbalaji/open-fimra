-- Detections and matches (PLAN.md sections 5.1, 5.3 and 8).
--
-- Detections are shared across workspaces: one copy of each satellite record.
-- Matches are per workspace: the forest class and hierarchy a workspace gives
-- to a detection. This lets one national installation serve many states.

-- +goose Up
CREATE TABLE detections (
    id              bigint GENERATED ALWAYS AS IDENTITY,
    -- Where the record came from: firms, fsi, bhuvan, upload or field.
    source          text NOT NULL CHECK (source ~ '^[a-z][a-z0-9_]*$'),
    -- The source's product, for example VIIRS_SNPP_NRT or MODIS_NRT.
    product         text NOT NULL,
    satellite       text,
    instrument      text,
    observed_at     timestamptz NOT NULL,
    geom            geometry(Point, 4326) NOT NULL,
    -- Pixel size in metres along scan and track, when the source gives it.
    scan_m          real,
    track_m         real,
    brightness_k    real,
    frp_mw          real,
    -- Kept as text because sources disagree: VIIRS uses l/n/h, MODIS uses 0 to 100.
    confidence      text,
    day_night       char(1) CHECK (day_night IN ('D', 'N')),
    -- The source's own identity for the record, built by the connector. Replaying
    -- a feed finds the same key, so it cannot create a duplicate.
    source_key      text NOT NULL,
    raw             jsonb NOT NULL DEFAULT '{}'::jsonb,
    ingested_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (id, observed_at),
    UNIQUE (source, source_key, observed_at)
) PARTITION BY RANGE (observed_at);

CREATE INDEX detections_geom_idx ON detections USING gist (geom);
CREATE INDEX detections_observed_idx ON detections (observed_at DESC);

-- Creates the monthly partition that holds `month`, if it does not exist yet.
-- Ingestion calls this for every month in a batch before inserting. There is no
-- default partition on purpose: a missing partition fails loudly instead of
-- hiding rows where later partitions cannot be created.
-- +goose StatementBegin
CREATE FUNCTION ensure_detection_partition(month date) RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
    -- Partitions follow UTC months.
    first_day date := date_trunc('month', month)::date;
    part_name text := format('detections_%s', to_char(first_day, 'YYYY_MM'));
BEGIN
    IF to_regclass(part_name) IS NULL THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF detections FOR VALUES FROM (%L) TO (%L)',
            part_name,
            first_day::timestamp AT TIME ZONE 'UTC',
            (first_day + interval '1 month') AT TIME ZONE 'UTC'
        );
    END IF;
    RETURN part_name;
END;
$$;
-- +goose StatementEnd

CREATE TABLE detection_matches (
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    detection_id      bigint NOT NULL,
    observed_at       timestamptz NOT NULL,
    forest_class      text NOT NULL CHECK (forest_class IN ('inside', 'near_edge', 'outside', 'fixed_heat_source')),
    -- Distance to the nearest forest feature, 0 when inside.
    forest_distance_m real CHECK (forest_distance_m >= 0),
    forest_feature_id bigint REFERENCES layer_features (id) ON DELETE SET NULL,
    -- Every current unit the detection falls in, across all hierarchies. Used for
    -- routing and for permission checks: a user sees a match when one of these
    -- paths is under a path they hold a role at.
    unit_paths        ltree[] NOT NULL DEFAULT '{}',
    -- Snapshot of every level of every hierarchy at matching time, for example
    -- {"territorial": {"circle": {"code": "C1", "name": "..."}, ...}}. It does not
    -- change when boundaries change later.
    hierarchy         jsonb NOT NULL DEFAULT '{}'::jsonb,
    attributes        jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- Shared boundaries, overlapping units and forest with no mapped unit go to triage.
    triage_reason     text CHECK (triage_reason IN ('shared_boundary', 'overlapping_units', 'no_unit')),
    -- The layer versions used, so a match can be reproduced.
    layer_version_ids uuid[] NOT NULL DEFAULT '{}',
    matched_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, detection_id, observed_at),
    FOREIGN KEY (detection_id, observed_at) REFERENCES detections (id, observed_at) ON DELETE CASCADE
);

CREATE INDEX detection_matches_recent_idx ON detection_matches (workspace_id, observed_at DESC);
CREATE INDEX detection_matches_class_idx ON detection_matches (workspace_id, forest_class, observed_at DESC);
CREATE INDEX detection_matches_units_idx ON detection_matches USING gist (unit_paths);
CREATE INDEX detection_matches_triage_idx ON detection_matches (workspace_id) WHERE triage_reason IS NOT NULL;

-- +goose Down
DROP TABLE detection_matches;
DROP FUNCTION ensure_detection_partition(date);
DROP TABLE detections;
