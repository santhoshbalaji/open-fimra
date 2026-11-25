-- Ingest runs and per-hierarchy match paths (PLAN.md section 5.1).
--
-- Every fetch from a source is recorded, with how many records it fetched,
-- inserted and skipped as duplicates, so feed health can be shown. Silent
-- failure in fire season is the largest operational risk.

-- +goose Up
CREATE TABLE ingest_runs (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id    uuid REFERENCES workspaces (id) ON DELETE SET NULL,
    source          text NOT NULL,
    product         text NOT NULL,
    window_start    timestamptz NOT NULL,
    window_end      timestamptz NOT NULL CHECK (window_end >= window_start),
    started_at      timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at     timestamptz,
    status          text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'succeeded', 'failed')),
    fetched         integer NOT NULL DEFAULT 0 CHECK (fetched >= 0),
    inserted        integer NOT NULL DEFAULT 0 CHECK (inserted >= 0),
    duplicates      integer NOT NULL DEFAULT 0 CHECK (duplicates >= 0),
    error_message   text,
    CHECK ((status = 'running') = (finished_at IS NULL)),
    CHECK ((status = 'failed') = (error_message IS NOT NULL)),
    CHECK (status <> 'succeeded' OR fetched = inserted + duplicates)
);

CREATE INDEX ingest_runs_feed_idx ON ingest_runs (source, product, started_at DESC);

-- Paths can have the same spelling in different hierarchies. Keep their
-- hierarchy IDs for permission checks, even after the units change.
ALTER TABLE detection_matches
    ADD COLUMN hierarchy_unit_paths jsonb NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE detection_matches DROP COLUMN hierarchy_unit_paths;
DROP TABLE ingest_runs;
