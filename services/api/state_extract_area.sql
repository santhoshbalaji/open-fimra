-- A state allocation is numeric and explicitly approved for this authority.
-- A whole-incident estimate or free text cannot stand in for an allocation.
WITH latest AS (
    SELECT DISTINCT ON (i.id) i.id,s.answers
    FROM incidents i JOIN incident_members m ON m.incident_id=i.id
    JOIN submissions s ON s.alert_id=m.alert_id AND s.workspace_id=i.workspace_id
    JOIN form_versions v ON v.id=s.form_version_id JOIN forms f ON f.id=v.form_id AND f.form_key='damage-assessment'
    JOIN approvals ap ON ap.submission_id=s.id AND ap.decision='approved'
    LEFT JOIN submissions provisional ON provisional.id=s.provisional_submission_id
    -- A revision stays in the window of the first assessment, even if approved
    -- later. Use the submitted time for older forms without an assessment time.
    CROSS JOIN LATERAL (
        SELECT coalesce(
            CASE WHEN pg_input_is_valid(coalesce(provisional.answers,s.answers)->>'assessed_at','timestamp with time zone')
                 THEN (coalesce(provisional.answers,s.answers)->>'assessed_at')::timestamptz END,
            provisional.received_at,s.received_at) AS assessed_at
    ) time
    WHERE i.workspace_id=$1 AND i.id=ANY($2::bigint[])
      AND time.assessed_at >= $4 AND time.assessed_at < $5
    ORDER BY i.id, ap.decided_at DESC, s.id DESC
)
SELECT coalesce(jsonb_agg(jsonb_build_object(
    'hectares', answers->'state_area_ha', 'landClass', answers->>'state_land_class',
    'method', answers->>'state_area_method', 'status', answers->>'state_area_status',
    'impacts', jsonb_build_object(
        'vegetation', coalesce(answers->'state_impact_categories' ? 'vegetation',false),
        'wildlife', coalesce(answers->'state_impact_categories' ? 'wildlife',false),
        'property', coalesce(answers->'state_impact_categories' ? 'property',false))) ORDER BY id), '[]')
FROM latest WHERE answers->>'state_code'=$3
    AND answers->>'state_land_class' IN ('forest','non_forest','mixed','unknown')
    AND answers->>'state_area_method' IN ('field','satellite','mixed')
    AND answers->>'state_area_status' IN ('provisional','final')
    AND CASE WHEN jsonb_typeof(answers->'state_area_ha')='number' AND jsonb_typeof(answers->'area_ha')='number'
        THEN (answers->>'state_area_ha')::numeric BETWEEN 0 AND (answers->>'area_ha')::numeric ELSE false END
