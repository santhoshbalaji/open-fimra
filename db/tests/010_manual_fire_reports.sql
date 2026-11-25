-- A manual fire report's detection belongs to one workspace, and the view
-- that re-matching reads keeps that workspace.

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO workspaces (id, slug, name)
VALUES ('00000000-0000-0000-0000-00000000a101', 'manual-report-test', 'Manual Report Test');
SELECT ensure_detection_partition('2026-03-15');

INSERT INTO detections (source, product, source_key, observed_at, geom, workspace_id)
VALUES ('manual', 'manual-fire-report', '5ca1ab1e-0000-4000-8000-00000000a111', '2026-03-15T09:00:00Z',
        ST_SetSRID(ST_Point(86.3, 16.4), 4326), '00000000-0000-0000-0000-00000000a101');

DO $$
BEGIN
    INSERT INTO detections (source, product, source_key, observed_at, geom)
    VALUES ('manual', 'manual-fire-report', '5ca1ab1e-0000-4000-8000-00000000a112', '2026-03-15T09:00:00Z',
            ST_SetSRID(ST_Point(86.3, 16.4), 4326));
    RAISE EXCEPTION 'a manual detection should need its workspace';
EXCEPTION WHEN check_violation THEN NULL;
END $$;

DO $$
BEGIN
    IF (SELECT m.workspace_id FROM matchable_detections m JOIN detections d ON (d.id, d.observed_at) = (m.id, m.observed_at)
        WHERE d.source_key = '5ca1ab1e-0000-4000-8000-00000000a111')
       IS DISTINCT FROM '00000000-0000-0000-0000-00000000a101' THEN
        RAISE EXCEPTION 'matchable_detections should keep the workspace of a manual detection';
    END IF;
END $$;

ROLLBACK;
