WITH ready AS MATERIALIZED (
    SELECT c.id AS case_id, m.*
    FROM lsasaf_cases c
    JOIN LATERAL (
        SELECT p.* FROM lsasaf_pixels p WHERE p.case_id = c.id ORDER BY p.slot DESC LIMIT 1
    ) hit ON true
    JOIN detection_matches m ON (m.workspace_id,m.detection_id,m.observed_at) = (hit.workspace_id,hit.detection_id,hit.slot)
    WHERE c.workspace_id = $1 AND c.alert_id IS NULL AND c.confirmed_at IS NULL
      AND c.last_slot >= $2::timestamptz - interval '2 hours'
      -- Check windows ending at or after the latest hit. A valid empty scan
      -- can complete one, and a late scan can fill a gap after a retry.
      AND EXISTS (
          SELECT FROM generate_series(c.last_slot, c.last_slot + ($4::int - 1) * interval '15 minutes',
                                      interval '15 minutes') seq(window_end)
          CROSS JOIN LATERAL (
              SELECT count(s.slot) AS valid_scans, count(p.slot) AS hit_scans
              FROM lsasaf_scans s LEFT JOIN lsasaf_pixels p ON p.case_id = c.id AND p.slot = s.slot
              WHERE s.workspace_id = c.workspace_id AND s.validated_at IS NOT NULL AND s.slot BETWEEN
                  seq.window_end - ($4 - 1) * interval '15 minutes' AND seq.window_end
          ) evidence
          WHERE valid_scans = $4 AND hit_scans >= $3::int
      )
      AND EXISTS (SELECT FROM workspaces w WHERE w.id = c.workspace_id AND w.kind <> 'national')
),
routed AS (
    INSERT INTO alerts (workspace_id,detection_id,observed_at,hierarchy,queue,
        hierarchy_unit_paths,unit_paths,forest_class,triage_reason,layer_version_ids,boundary_snapshot)
    SELECT workspace_id,detection_id,observed_at,hierarchy,'triage',
        hierarchy_unit_paths,unit_paths,forest_class,triage_reason,layer_version_ids,boundary_snapshot
    FROM ready RETURNING id, workspace_id, detection_id, observed_at
)
UPDATE lsasaf_cases c SET alert_id = a.id FROM routed a JOIN ready r
ON (r.workspace_id,r.detection_id,r.observed_at) = (a.workspace_id,a.detection_id,a.observed_at)
WHERE c.id = r.case_id;
