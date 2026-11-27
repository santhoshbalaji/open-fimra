-- +goose Up
-- A comparable fetch bounds when the source could first have published a row.
-- Existing runs have no recorded comparison, so their samples are censored.
ALTER TABLE ingest_runs
    ADD COLUMN previous_fetched_at timestamptz,
    ADD CONSTRAINT ingest_runs_previous_fetch_check CHECK (
        previous_fetched_at IS NULL OR
        (kind = 'poll' AND status = 'succeeded' AND previous_fetched_at < fetched_at));

CREATE INDEX ingest_runs_latency_idx ON ingest_runs (fetched_at)
    WHERE previous_fetched_at IS NOT NULL;

-- +goose Down
DROP INDEX ingest_runs_latency_idx;
ALTER TABLE ingest_runs
    DROP CONSTRAINT ingest_runs_previous_fetch_check,
    DROP COLUMN previous_fetched_at;
