-- Fictional receiving workspace. Load separately with make demo-national-portal.
INSERT INTO workspaces (slug,name,kind) VALUES ('national-demo','National Demo','national') ON CONFLICT (slug) DO NOTHING;
INSERT INTO hierarchies (workspace_id,slug,name,kind)
SELECT id,'exchange','State exchange','other' FROM workspaces WHERE slug='national-demo' AND kind='national'
ON CONFLICT DO NOTHING;
INSERT INTO hierarchy_levels (hierarchy_id,depth,slug,labels)
SELECT h.id,l.depth,l.slug,l.labels::jsonb
FROM hierarchies h JOIN workspaces w ON w.id=h.workspace_id
CROSS JOIN (VALUES (0,'country','{"en":"National","hi":"राष्ट्रीय"}'),(1,'state','{"en":"State","hi":"राज्य"}')) l(depth,slug,labels)
WHERE w.slug='national-demo' AND h.slug='exchange'
ON CONFLICT DO NOTHING;
INSERT INTO units (hierarchy_id,depth,path,code,name)
SELECT h.id,v.depth,v.path::ltree,v.code,v.name
FROM hierarchies h JOIN workspaces w ON w.id=h.workspace_id
CROSS JOIN (VALUES (0,'hub','XS','National Demo'),(1,'hub.demo','XS-DEMO','Demo State'),
    (1,'hub.north','XS-NORTH','North Demo State'),(1,'hub.east','XS-EAST','East Demo State')) v(depth,path,code,name)
WHERE w.slug='national-demo' AND h.slug='exchange'
ON CONFLICT (hierarchy_id,path) WHERE valid_to IS NULL DO NOTHING;
INSERT INTO national_states (workspace_id,hierarchy_id,unit_path,state_code,unit_code_system,location_decimals)
SELECT w.id,h.id,u.path,u.code,'demo-forest-units',CASE WHEN u.code='XS-NORTH' THEN 1 ELSE 0 END
FROM workspaces w JOIN hierarchies h ON h.workspace_id=w.id
JOIN units u ON u.hierarchy_id=h.id AND u.valid_to IS NULL AND u.depth=1
WHERE w.slug='national-demo' AND h.slug='exchange'
ON CONFLICT DO NOTHING;

INSERT INTO users (oidc_subject,username,display_name)
VALUES ('d1000000-0000-4000-8000-0000000000a1','national.ministry','Demo Ministry Officer'),
       ('d1000000-0000-4000-8000-0000000000a2','national.survey','Demo Survey Officer'),
       ('d1000000-0000-4000-8000-0000000000a3','national.disaster','Demo Disaster Officer'),
       ('d1000000-0000-4000-8000-0000000000a4','state.nodal','Demo Nodal Officer'),
       ('d1000000-0000-4000-8000-0000000000a5','public.viewer','Demo Public Viewer'),
       ('d1000000-0000-4000-8000-0000000000a6','north.nodal','North Demo Nodal Officer')
ON CONFLICT (oidc_subject) DO NOTHING;
INSERT INTO role_assignments (user_id,workspace_id,hierarchy_id,unit_path,role_slug)
SELECT u.id,s.workspace_id,s.hierarchy_id,s.unit_path,r.role
FROM national_states s JOIN workspaces w ON w.id=s.workspace_id
CROSS JOIN (VALUES
    ('national.ministry','national_ministry',ARRAY['XS-DEMO','XS-NORTH','XS-EAST']),
    ('national.survey','national_survey',ARRAY['XS-DEMO','XS-EAST']),
    ('national.disaster','national_disaster',ARRAY['XS-DEMO','XS-NORTH']),
    ('state.nodal','national_nodal',ARRAY['XS-DEMO']),
    ('public.viewer','national_public',ARRAY['XS-DEMO','XS-NORTH','XS-EAST']),
    ('north.nodal','national_nodal',ARRAY['XS-NORTH'])
) r(username,role,states)
JOIN users u ON u.username=r.username AND u.oidc_subject LIKE 'd1000000-0000-4000-8000-0000000000a%'
WHERE w.slug='national-demo' AND s.state_code=ANY(r.states)
ON CONFLICT DO NOTHING;
