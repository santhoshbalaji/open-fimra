-- Fires recorded by hand with the manual fire report (PLAN.md section 5.4).
-- A manual report becomes a detection with the source 'manual', so its alert
-- follows the same matching, routing, incidents and workflow as a satellite
-- detection. Satellite records are shared by every workspace, but a manual
-- report belongs to the workspace it was recorded in, and is matched there only.

-- +goose Up
ALTER TABLE detections ADD COLUMN workspace_id uuid REFERENCES workspaces;
ALTER TABLE detections ADD CONSTRAINT detections_manual_workspace
    CHECK (source <> 'manual' OR workspace_id IS NOT NULL);

-- Re-matching and previews read this view, and keep a detection with a
-- workspace to that workspace.
CREATE OR REPLACE VIEW matchable_detections AS
SELECT d.id, d.observed_at, d.geom, d.workspace_id
FROM detections d
WHERE NOT EXISTS (
    SELECT 1 FROM detection_links l WHERE (l.detection_id, l.observed_at) = (d.id, d.observed_at)
);

-- +goose Down
DROP VIEW matchable_detections;
CREATE VIEW matchable_detections AS
SELECT d.id, d.observed_at, d.geom
FROM detections d
WHERE NOT EXISTS (
    SELECT 1 FROM detection_links l WHERE (l.detection_id, l.observed_at) = (d.id, d.observed_at)
);
ALTER TABLE detections DROP CONSTRAINT detections_manual_workspace;
ALTER TABLE detections DROP COLUMN workspace_id;
