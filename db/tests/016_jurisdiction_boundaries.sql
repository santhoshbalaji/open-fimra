BEGIN;
SET LOCAL TIME ZONE 'UTC';

INSERT INTO workspaces (id, slug, name, boundary)
VALUES ('00000000-0000-0000-0000-000000001601', 'boundary-test', 'Boundary test',
        ST_Multi(ST_MakeEnvelope(80, 15, 81, 16, 4326)));
INSERT INTO hierarchies (id, workspace_id, slug, name, kind)
VALUES ('00000000-0000-0000-0000-000000001602', '00000000-0000-0000-0000-000000001601',
        'territorial', 'Territorial', 'territorial');
INSERT INTO hierarchy_levels (hierarchy_id, depth, slug, labels, optional)
SELECT '00000000-0000-0000-0000-000000001602', depth, slug, jsonb_build_object('en', label), optional
FROM (VALUES (0, 'state', 'State', false), (1, 'division', 'Division', false),
             (2, 'sub_division', 'Sub-division', true), (3, 'range', 'Range', false),
             (4, 'section', 'Round', true), (5, 'beat', 'Beat', false)) level(depth, slug, label, optional);

INSERT INTO layers (id, workspace_id, slug, name, role, hierarchy_id)
SELECT id::uuid, '00000000-0000-0000-0000-000000001601', slug, name, role,
       CASE WHEN role = 'hierarchy' THEN '00000000-0000-0000-0000-000000001602'::uuid END
FROM (VALUES ('00000000-0000-0000-0000-000000001603', 'territory', 'Territory', 'hierarchy'),
             ('00000000-0000-0000-0000-000000001604', 'forest', 'Forest', 'forest'),
             ('00000000-0000-0000-0000-000000001605', 'reserve', 'Reserve', 'analysis'),
             ('00000000-0000-0000-0000-000000001606', 'district', 'District', 'analysis')) layer(id, slug, name, role);
INSERT INTO layer_versions (id, layer_id, version, status, published_at, retired_at, overlay_kind, overlay_from)
SELECT ('00000000-0000-0000-0000-0000000016' || suffix)::uuid,
       ('00000000-0000-0000-0000-0000000016' || layer)::uuid, version,
       CASE WHEN ends IS NULL THEN 'published' ELSE 'retired' END,
       starts::timestamptz, ends::timestamptz, kind, CASE WHEN kind IS NOT NULL THEN starts::timestamptz END
FROM (VALUES ('11', '03', 1, '2026-01-01', '2026-02-01', NULL),
             ('12', '03', 2, '2026-02-01', NULL, NULL),
             ('13', '04', 1, '2026-01-01', NULL, NULL),
             ('14', '05', 1, '2026-01-01', '2026-02-01', 'protected_area'),
             ('15', '05', 2, '2026-02-01', NULL, 'protected_area'),
             ('16', '06', 1, '2026-01-01', NULL, 'civil_district')) v(suffix, layer, version, starts, ends, kind);

INSERT INTO layer_features (layer_version_id, name, attributes, geom)
SELECT ('00000000-0000-0000-0000-0000000016' || suffix)::uuid, name, attributes::jsonb,
       ST_MakeEnvelope(80, 15, 81, 16, 4326)
FROM (VALUES ('13', 'Test Forest', '{}'), ('14', 'Demo Reserve', '{"zone":"core"}'),
             ('15', 'Demo Reserve', '{"zone":"buffer"}'), ('16', 'Demo District', '{}')) f(suffix, name, attributes);

INSERT INTO units (hierarchy_id, depth, code, name, path, geom, layer_version_id, valid_from, valid_to)
SELECT '00000000-0000-0000-0000-000000001602', depth, code, code, path::ltree,
       ST_Multi(ST_MakeEnvelope(80, 15, 81, 16, 4326)),
       ('00000000-0000-0000-0000-0000000016' || suffix)::uuid,
       starts::timestamptz, ends::timestamptz
FROM (VALUES (0, 'S', 's', '11', '2026-01-01', NULL),
             (1, 'D', 's.d', '11', '2026-01-01', NULL),
             (3, 'R', 's.d.r', '11', '2026-01-01', NULL),
             (5, 'B1', 's.d.r.b1', '11', '2026-01-01', '2026-02-01'),
             (4, 'Section', 's.d.r.section', '12', '2026-02-01', NULL),
             (5, 'B2', 's.d.r.section.b2', '12', '2026-02-01', NULL)) u(depth, code, path, suffix, starts, ends);

DO $$
DECLARE
    workspace uuid := '00000000-0000-0000-0000-000000001601';
    territory uuid := '00000000-0000-0000-0000-000000001602';
    point geometry := ST_SetSRID(ST_Point(80.5, 15.5), 4326);
    before_transfer detection_matches;
    at_transfer detection_matches;
BEGIN
    ASSERT hierarchy_parent_allowed(territory, 3::smallint, 1::smallint)
        AND hierarchy_parent_allowed(territory, 5::smallint, 3::smallint),
        'branches may skip the sub-division and section';
    ASSERT NOT hierarchy_parent_allowed(territory, 5::smallint, 2::smallint),
        'branches must use the range';

    SELECT * INTO STRICT before_transfer FROM classify_detection(workspace, 1, '2026-01-31 23:59:59+00', point);
    SELECT * INTO STRICT at_transfer FROM classify_detection(workspace, 2, '2026-02-01 00:00:00+00', point);
    ASSERT (SELECT unit_path FROM alert_route(before_transfer)) = 's.d.r.b1';
    ASSERT (SELECT unit_path FROM alert_route(at_transfer)) = 's.d.r.section.b2';
    ASSERT match_under_grant(before_transfer.hierarchy_unit_paths, territory, 's.d', 1::smallint)
       AND match_under_grant(at_transfer.hierarchy_unit_paths, territory, 's.d', 1::smallint),
       'the same division grant covers both paths';
    ASSERT before_transfer.hierarchy #> '{territorial,section}' IS NULL
        AND at_transfer.hierarchy #>> '{territorial,section,code}' = 'Section';
    ASSERT before_transfer.boundary_snapshot->'overlays' @> '[{"name":"Demo District"},{"zone":"core"}]'
        AND jsonb_array_length(before_transfer.boundary_snapshot->'overlays') = 2;
    ASSERT at_transfer.boundary_snapshot->'overlays' @> '[{"name":"Demo District"},{"zone":"buffer"}]'
        AND jsonb_array_length(at_transfer.boundary_snapshot->'overlays') = 2;
    ASSERT before_transfer.layer_version_ids @> ARRAY['00000000-0000-0000-0000-000000001614'::uuid]
        AND at_transfer.layer_version_ids @> ARRAY['00000000-0000-0000-0000-000000001615'::uuid]
        AND NOT (at_transfer.layer_version_ids @> ARRAY['00000000-0000-0000-0000-000000001614'::uuid]);
    ASSERT NOT EXISTS (SELECT FROM classify_detection(workspace, 3, '2025-12-31 23:59:59+00', point)
                       WHERE forest_class = 'inside'), 'a version is not used before its effective date';
END;
$$;

ROLLBACK;
