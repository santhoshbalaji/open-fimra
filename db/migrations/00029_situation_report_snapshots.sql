-- +goose Up
ALTER TABLE situation_reports
    ADD COLUMN snapshot_grants jsonb,
    DROP CONSTRAINT situation_reports_status_check,
    DROP CONSTRAINT situation_reports_check2,
    ADD CONSTRAINT situation_reports_status_check CHECK (status IN ('draft', 'pending', 'ready', 'failed', 'expired')),
    ADD CONSTRAINT situation_reports_check2 CHECK ((status IN ('draft', 'pending')) = (finished_at IS NULL));

DROP INDEX situation_reports_expiry_idx;
CREATE INDEX situation_reports_expiry_idx ON situation_reports (expires_at) WHERE status <> 'expired';

-- +goose Down
DELETE FROM situation_reports WHERE status = 'draft';
ALTER TABLE situation_reports
    DROP COLUMN snapshot_grants,
    DROP CONSTRAINT situation_reports_status_check,
    DROP CONSTRAINT situation_reports_check2,
    ADD CONSTRAINT situation_reports_status_check CHECK (status IN ('pending', 'ready', 'failed', 'expired')),
    ADD CONSTRAINT situation_reports_check2 CHECK ((status = 'pending') = (finished_at IS NULL));

DROP INDEX situation_reports_expiry_idx;
CREATE INDEX situation_reports_expiry_idx ON situation_reports (expires_at) WHERE status IN ('pending', 'ready', 'failed');
