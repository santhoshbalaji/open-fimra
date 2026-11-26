-- +goose Up
-- Keep the member decisions made at save time with the append-only incident
-- entry so a retry can return them even after the alerts have moved again.
ALTER TABLE step_log ADD COLUMN incident_outcomes jsonb
    CHECK (incident_outcomes IS NULL OR (incident_id IS NOT NULL AND jsonb_typeof(incident_outcomes) = 'array'));

-- +goose Down
ALTER TABLE step_log DROP COLUMN incident_outcomes;
