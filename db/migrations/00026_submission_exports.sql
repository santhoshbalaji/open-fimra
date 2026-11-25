-- Exports of field reports (PLAN.md section 15, slice 25). A submission export
-- freezes the submissions a user may see, through the same alert rules as an
-- alert export. Each row keeps its submission, its alert and the alert's
-- detection, so the geo worker can write the place and the answers.

-- +goose Up
ALTER TABLE exports DROP CONSTRAINT exports_kind_check,
    ADD CONSTRAINT exports_kind_check CHECK (kind IN ('detections', 'alerts', 'submissions'));

-- Submissions are never deleted, so a submission row stays as long as its export.
ALTER TABLE export_rows ADD COLUMN submission_id uuid REFERENCES submissions;
CREATE INDEX export_rows_submission_idx ON export_rows (submission_id) WHERE submission_id IS NOT NULL;

-- The rule from 00016 for every kind: rows only from the creating transaction,
-- while pending, in the export's workspace and up to its row count. Alert and
-- submission rows name their alert, and a submission row names a submission
-- of that alert in the same workspace.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION export_rows_check_added() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM (SELECT DISTINCT export_id FROM added) a
        LEFT JOIN exports e ON e.id = a.export_id
        WHERE e.id IS NULL OR e.status <> 'pending' OR e.created_xact <> pg_current_xact_id()
           OR e.row_count <> (SELECT count(*) FROM export_rows r WHERE r.export_id = e.id)
    ) OR EXISTS (
        SELECT 1 FROM added a JOIN exports e ON e.id = a.export_id
        WHERE a.workspace_id <> e.workspace_id
           OR (e.kind IN ('alerts', 'submissions')) <> (a.alert_id IS NOT NULL)
           OR (e.kind = 'submissions') <> (a.submission_id IS NOT NULL)
    ) OR EXISTS (
        SELECT 1 FROM added a
        WHERE a.submission_id IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM submissions s
            WHERE s.id = a.submission_id AND s.alert_id = a.alert_id AND s.workspace_id = a.workspace_id)
    ) THEN
        RAISE EXCEPTION 'export rows are frozen when the export is requested'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
DELETE FROM exports WHERE kind = 'submissions';
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION export_rows_check_added() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM (SELECT DISTINCT export_id FROM added) a
        LEFT JOIN exports e ON e.id = a.export_id
        WHERE e.id IS NULL OR e.status <> 'pending' OR e.created_xact <> pg_current_xact_id()
           OR e.row_count <> (SELECT count(*) FROM export_rows r WHERE r.export_id = e.id)
    ) OR EXISTS (
        SELECT 1 FROM added a JOIN exports e ON e.id = a.export_id
        WHERE a.workspace_id <> e.workspace_id OR (e.kind = 'alerts') <> (a.alert_id IS NOT NULL)
    ) THEN
        RAISE EXCEPTION 'export rows are frozen when the export is requested'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
DROP INDEX export_rows_submission_idx;
ALTER TABLE export_rows DROP COLUMN submission_id;
ALTER TABLE exports DROP CONSTRAINT exports_kind_check,
    ADD CONSTRAINT exports_kind_check CHECK (kind IN ('detections', 'alerts'));
