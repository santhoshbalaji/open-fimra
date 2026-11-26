-- +goose Up
ALTER TABLE alerts ADD COLUMN assignment jsonb;

-- +goose StatementBegin
CREATE FUNCTION alert_due_at(template bigint, step text, entered timestamptz)
RETURNS timestamptz LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT min(entered + time_limit)
    FROM workflow_transitions WHERE (template_id, from_step) = (template, step);
$$;
-- +goose StatementEnd

-- +goose Down
UPDATE alerts a SET
    queue = log.details #>> '{previous,queue}',
    hierarchy_id = (log.details #>> '{previous,hierarchyId}')::uuid,
    unit_path = (log.details #>> '{previous,unitPath}')::ltree,
    unit_paths = ARRAY(SELECT path::ltree FROM jsonb_array_elements_text(log.details #> '{previous,unitPaths}') AS p(path)),
    hierarchy_unit_paths = log.details #> '{previous,hierarchyUnitPaths}'
FROM audit_log log
WHERE a.assignment IS NOT NULL AND log.id = (a.assignment->>'auditId')::bigint
  AND log.action = 'alert.assign' AND log.target_type = 'alert' AND log.target_id = a.id::text;

DROP FUNCTION alert_due_at(bigint, text, timestamptz);
ALTER TABLE alerts DROP COLUMN assignment;
