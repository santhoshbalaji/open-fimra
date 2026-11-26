-- Each state owns its figures. The shared event has two reports and one ID.
INSERT INTO national_windows (state_id,days)
SELECT s.id,'[2026-03-08,2026-03-09)' FROM national_states s JOIN workspaces w ON w.id=s.workspace_id
WHERE w.slug='national-demo' AND s.state_code='XS-EAST';
UPDATE national_windows n SET ministry_shared=true,survey_shared=true,preparedness_shared=true,feedback_shared=true
FROM national_states s JOIN workspaces w ON w.id=s.workspace_id
WHERE n.state_id=s.id AND w.slug='national-demo';

INSERT INTO national_publications (window_id,extract_id)
SELECT n.id,x.id FROM national_current_extracts x JOIN national_windows n ON n.id=x.window_id
JOIN national_states s ON s.id=n.state_id JOIN workspaces w ON w.id=s.workspace_id
WHERE w.slug='national-demo' AND x.kind='incident';

INSERT INTO national_satellite_measures (window_id,source,product,starts_at,ends_at,expected,reported,detections)
SELECT n.id,'Fictional satellite collection','Fictional hotspots','2026-03-07T12:30:00Z','2026-03-08T12:30:00Z',6,
       CASE WHEN s.state_code='XS-EAST' THEN 5 ELSE 6 END,
       CASE WHEN s.state_code='XS-EAST' THEN 12 ELSE 83 END
FROM national_windows n JOIN national_states s ON s.id=n.state_id JOIN workspaces w ON w.id=s.workspace_id
WHERE w.slug='national-demo';

INSERT INTO national_feedback (window_id,prepared,submitted,accepted,rejected)
SELECT n.id,1,2,4,1 FROM national_windows n JOIN national_states s ON s.id=n.state_id
JOIN workspaces w ON w.id=s.workspace_id WHERE w.slug='national-demo' AND s.state_code='XS-DEMO';

INSERT INTO national_incident_updates (state_id,incident_ref,shared_event_id,fsi_id,status,severity,occurred_at,next_update_at,
    precision_decimals,longitude,latitude,threats,aid_requested,aid_accepted,provisional_impacts,shared)
SELECT s.id,CASE WHEN s.state_code='XS-DEMO' THEN 'DEMO-06' ELSE 'NORTH-09' END,
    'cdc00000-0000-4000-8000-000000000001','FSI-DEMO-04','active','major','2026-03-08T10:30:00Z','2026-03-08T14:30:00Z',
    s.location_decimals,82,20,'Fire is spreading along a fictional ridge.','One water crew.','One support crew confirmed.',
    'An early estimate of 40 hectares across the two states.',true
FROM national_states s JOIN workspaces w ON w.id=s.workspace_id WHERE w.slug='national-demo' AND s.state_code IN ('XS-DEMO','XS-NORTH');
