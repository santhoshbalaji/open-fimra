-- XLSForm conversions (PLAN.md section 5.4). The geo worker turns an uploaded
-- XLSForm into a draft form definition and a review report, or writes a form
-- definition as an XLSForm. Each row is one job and holds its own result.
--
-- This is a job table, not a form table: a draft stays here until the forms
-- part of the API copies it into a form of its own.

-- +goose Up
CREATE TABLE form_conversions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The requester's OpenID Connect subject. Only this user may read the result.
    requested_by  text NOT NULL,
    direction     text NOT NULL CHECK (direction IN ('import', 'export')),
    -- An import starts from an uploaded .xlsx workbook, kept with its file name.
    source_name   text CHECK (char_length(source_name) BETWEEN 1 AND 255),
    source_file   bytea CHECK (octet_length(source_file) BETWEEN 1 AND 10485760),
    -- An export starts from a form definition in the form format.
    source_form   jsonb CHECK (jsonb_typeof(source_form) = 'object'),
    status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ready', 'failed')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz,
    -- The draft form an import made. It always passed the form validator.
    draft_form    jsonb CHECK (jsonb_typeof(draft_form) = 'object'),
    -- The workbook an export wrote.
    result_file   bytea CHECK (octet_length(result_file) BETWEEN 1 AND 10485760),
    -- The review report: a list of {sheet, row, column, item, message}, in plain English.
    report        jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(report) = 'array'),
    error_message text,
    CHECK ((direction = 'import') = (source_file IS NOT NULL AND source_form IS NULL)),
    CHECK ((direction = 'export') = (source_form IS NOT NULL AND source_file IS NULL AND source_name IS NULL)),
    CHECK ((status = 'pending') = (finished_at IS NULL)),
    CHECK ((status = 'failed') = (error_message IS NOT NULL)),
    CHECK ((status = 'ready' AND direction = 'import') = (draft_form IS NOT NULL)),
    CHECK ((status = 'ready' AND direction = 'export') = (result_file IS NOT NULL))
);

CREATE INDEX form_conversions_pending_idx ON form_conversions (created_at, id) WHERE status = 'pending';
CREATE INDEX form_conversions_requester_idx ON form_conversions (workspace_id, requested_by, created_at);

-- +goose Down
DROP TABLE form_conversions;
