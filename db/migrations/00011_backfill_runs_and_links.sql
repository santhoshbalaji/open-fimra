-- Backfill runs, processing times and links between records of the same
-- observation (PLAN.md sections 5.1 and 10).
--
-- Each ingest run now says why it ran: a 15-minute poll, a backfill chunk or a
-- file import. Backfill skips days that a successful backfill run already
-- covers, so it can be stopped and started again.
--
-- FIRMS publishes each satellite observation twice: first in a near-real-time
-- product, then weeks later in a standard-processing product. When both are
-- stored, the later record is linked to the first and gets no match of its
-- own, so it is not counted or alerted twice.
--
-- To measure the release gate that 95% of new FIRMS records are processed
-- within 20 minutes, runs record when their response arrived, and detections
-- record the run that first stored them.

-- +goose Up
ALTER TABLE ingest_runs
    ADD COLUMN kind text NOT NULL DEFAULT 'poll' CHECK (kind IN ('poll', 'backfill', 'import')),
    -- When the source's response had been read, by the database clock.
    ADD COLUMN fetched_at timestamptz,
    -- The newest observation time in the response, including duplicates.
    ADD COLUMN latest_observed_at timestamptz,
    -- New records linked to an earlier record of the same observation.
    ADD COLUMN linked integer NOT NULL DEFAULT 0 CHECK (linked >= 0),
    ADD CONSTRAINT ingest_runs_linked_inserted_check CHECK (linked <= inserted);

-- Earlier runs did not record these. Imports had no workspace.
UPDATE ingest_runs SET fetched_at = finished_at WHERE status = 'succeeded';
UPDATE ingest_runs SET kind = 'import' WHERE workspace_id IS NULL;

ALTER TABLE ingest_runs
    ADD CONSTRAINT ingest_runs_fetched_at_check CHECK (status <> 'succeeded' OR fetched_at IS NOT NULL);

-- Feed health reads each workspace's live feeds; backfill reads its own chunks.
CREATE INDEX ingest_runs_workspace_feed_idx
    ON ingest_runs (workspace_id, kind, source, product, started_at DESC);

ALTER TABLE detections
    ADD COLUMN ingest_run_id bigint REFERENCES ingest_runs (id) ON DELETE SET NULL;

CREATE INDEX detections_ingest_run_idx ON detections (ingest_run_id);

-- A record that reports the same observation as an earlier record.
CREATE TABLE detection_links (
    detection_id        bigint NOT NULL,
    observed_at         timestamptz NOT NULL,
    -- The earlier record. It keeps the workspace matches.
    primary_id          bigint NOT NULL,
    primary_observed_at timestamptz NOT NULL,
    reason              text NOT NULL CHECK (reason IN ('same_observation')),
    distance_m          real NOT NULL CHECK (distance_m >= 0),
    linked_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (detection_id, observed_at),
    UNIQUE (primary_id, primary_observed_at),
    CHECK ((detection_id, observed_at) <> (primary_id, primary_observed_at)),
    FOREIGN KEY (detection_id, observed_at) REFERENCES detections (id, observed_at) ON DELETE CASCADE,
    FOREIGN KEY (primary_id, primary_observed_at) REFERENCES detections (id, observed_at) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE detection_links;
DROP INDEX detections_ingest_run_idx;
ALTER TABLE detections DROP COLUMN ingest_run_id;
DROP INDEX ingest_runs_workspace_feed_idx;
ALTER TABLE ingest_runs
    DROP CONSTRAINT ingest_runs_fetched_at_check,
    DROP CONSTRAINT ingest_runs_linked_inserted_check,
    DROP COLUMN linked,
    DROP COLUMN latest_observed_at,
    DROP COLUMN fetched_at,
    DROP COLUMN kind;
