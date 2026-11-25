-- Feed-health alerts (PLAN.md sections 5.1 and 13).
--
-- A periodic check opens an alert when a live feed goes quiet or starts
-- failing in fire season, and resolves it when the feed recovers. There is at
-- most one open alert per workspace feed, so a problem that lasts for days is
-- still one alert. Notifications are sent from these rows by a later slice.

-- +goose Up
CREATE TABLE feed_health_alerts (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    source           text NOT NULL,
    product          text NOT NULL,
    -- The status that opened the alert, and the latest status while it is open.
    opened_status    text NOT NULL CHECK (opened_status IN ('quiet', 'failing')),
    status           text NOT NULL CHECK (status IN ('healthy', 'delayed', 'quiet', 'failing')),
    opened_at        timestamptz NOT NULL,
    last_checked_at  timestamptz NOT NULL,
    resolved_at      timestamptz,
    -- The feed's facts when the alert opened: last success, latest observation
    -- and recent failures.
    details          jsonb NOT NULL DEFAULT '{}'::jsonb,
    CHECK (last_checked_at >= opened_at),
    CHECK (resolved_at IS NULL OR resolved_at >= opened_at),
    CHECK (resolved_at IS NOT NULL OR status IN ('quiet', 'failing'))
);

CREATE UNIQUE INDEX feed_health_alerts_open_key
    ON feed_health_alerts (workspace_id, source, product) WHERE resolved_at IS NULL;
CREATE INDEX feed_health_alerts_workspace_idx ON feed_health_alerts (workspace_id, opened_at DESC);

-- +goose Down
DROP TABLE feed_health_alerts;
