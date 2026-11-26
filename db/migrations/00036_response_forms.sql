-- +goose Up
ALTER TABLE submissions ADD COLUMN provisional_submission_id uuid REFERENCES submissions;
CREATE INDEX submissions_provisional_idx ON submissions (provisional_submission_id)
    WHERE provisional_submission_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION check_assessment_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT FROM submissions p
        JOIN form_versions v ON v.id = NEW.form_version_id
        JOIN forms f ON f.id = v.form_id AND f.form_key = 'damage-assessment'
        JOIN form_versions pv ON pv.id = p.form_version_id AND pv.form_id = f.id
        WHERE p.id = NEW.provisional_submission_id AND p.provisional_submission_id IS NULL
          AND p.alert_id = NEW.alert_id AND p.workspace_id = NEW.workspace_id
    ) THEN
        RAISE EXCEPTION 'a revision must link to a provisional damage assessment for the same alert'
            USING ERRCODE = 'check_violation', CONSTRAINT = 'assessment_revision_parent';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER assessment_revision_parent BEFORE INSERT ON submissions FOR EACH ROW
    WHEN (NEW.provisional_submission_id IS NOT NULL) EXECUTE FUNCTION check_assessment_revision();

CREATE TABLE source_feedback (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    alert_id bigint NOT NULL REFERENCES alerts,
    verification_id uuid NOT NULL REFERENCES submissions,
    assessment_id uuid REFERENCES submissions,
    external_alert_ref text NOT NULL CHECK (length(btrim(external_alert_ref)) BETWEEN 1 AND 500),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    status text NOT NULL DEFAULT 'prepared' CHECK (status IN ('prepared', 'submitted', 'accepted', 'rejected')),
    prepared_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    submitted_at timestamptz,
    decided_at timestamptz,
    provider_receipt text CHECK (length(provider_receipt) <= 500),
    UNIQUE NULLS NOT DISTINCT (alert_id, verification_id, assessment_id),
    CHECK ((status = 'prepared') = (submitted_at IS NULL)),
    CHECK ((status IN ('accepted', 'rejected')) = (decided_at IS NOT NULL)),
    CHECK (decided_at >= submitted_at)
);
CREATE INDEX source_feedback_alert_recent_idx ON source_feedback (alert_id, prepared_at DESC, id DESC);

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM source_feedback) OR EXISTS (SELECT FROM submissions WHERE provisional_submission_id IS NOT NULL) THEN
        RAISE EXCEPTION 'Downgrading would discard assessment or feedback history.';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP TABLE source_feedback;
DROP TRIGGER assessment_revision_parent ON submissions;
DROP FUNCTION check_assessment_revision();
ALTER TABLE submissions DROP COLUMN provisional_submission_id;
