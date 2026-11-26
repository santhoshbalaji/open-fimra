-- Optional dispatch example for the synthetic workspace.
BEGIN;
SELECT pg_advisory_xact_lock(hashtext('open-fimra:demo-seed'));
DO $$
DECLARE
    workspace uuid;
    wildlife uuid;
    territorial uuid;
    reserve geometry;
    core geometry;
    version uuid;
    buffer_rule bigint;
    shift bigint;
BEGIN
    SELECT id INTO STRICT workspace FROM workspaces WHERE slug = 'demo';
    SELECT id INTO STRICT territorial FROM hierarchies WHERE workspace_id = workspace AND slug = 'territorial';
    IF EXISTS (SELECT FROM hierarchies WHERE workspace_id = workspace AND slug = 'reserve-response') THEN
        RETURN;
    END IF;
    SELECT v.id INTO STRICT version FROM layer_versions v JOIN layers l ON l.id = v.layer_id
    WHERE l.workspace_id = workspace AND l.slug = 'protected-areas' AND v.status = 'published';
    SELECT ST_Multi(ST_Union(geom)) INTO reserve FROM layer_features WHERE layer_version_id = version;
    SELECT geom INTO STRICT core FROM layer_features WHERE layer_version_id = version AND code = 'PA01-C';

    INSERT INTO hierarchies (workspace_id, slug, name, kind)
    VALUES (workspace, 'reserve-response', 'Demo Reserve Response', 'protected_area') RETURNING id INTO wildlife;
    INSERT INTO hierarchy_levels (hierarchy_id, depth, slug, labels) VALUES
        (wildlife, 0, 'state', '{"en":"State"}'),
        (wildlife, 1, 'division', '{"en":"Division"}'),
        (wildlife, 2, 'beat', '{"en":"Beat"}');
    INSERT INTO units (hierarchy_id, depth, code, name, path, geom, layer_version_id, valid_from) VALUES
        (wildlife, 0, 'DR', 'Demo Reserve', 'dr', reserve, version, '-infinity'),
        (wildlife, 1, 'W1', 'Demo Wildlife Division', 'dr.w1', reserve, version, '-infinity'),
        (wildlife, 2, 'RC', 'Demo Reserve Core Beat', 'dr.w1.core', core, version, '-infinity');

    INSERT INTO dispatch_rules (workspace_id, name, priority, effective_from, overlay_layer, overlay_code,
                                overlay_zone, lead_hierarchy_id, lead_path)
    VALUES (workspace, 'Demo reserve core', 20, '-infinity', 'protected-areas', 'PA01-C', 'core', wildlife, 'dr.w1'),
           (workspace, 'Demo reserve buffer', 10, '-infinity', 'protected-areas', 'PA01-B', 'buffer', territorial, 'ds.c2.d4');
    SELECT id INTO STRICT buffer_rule FROM dispatch_rules WHERE workspace_id = workspace AND name = 'Demo reserve buffer';
    INSERT INTO dispatch_rule_support (workspace_id, rule_id, hierarchy_id, unit_path)
    VALUES (workspace, buffer_rule, wildlife, 'dr.w1');

    INSERT INTO duty_shifts (workspace_id, name, kind, starts_at, ends_at)
    VALUES (workspace, 'Demo Reserve Fire Squad', 'squad', now() - interval '1 hour', now() + interval '7 hours')
    RETURNING id INTO shift;
    INSERT INTO duty_members (shift_id, user_id)
    SELECT shift, id FROM users WHERE username = 'beat.guard'
      AND EXISTS (SELECT FROM role_assignments a WHERE a.workspace_id = workspace AND a.user_id = users.id);
    INSERT INTO duty_service_units VALUES (workspace, shift, wildlife, 'dr.w1.core');

    INSERT INTO duty_shifts (workspace_id, name, kind, starts_at, ends_at)
    VALUES (workspace, 'Demo Lake Watch', 'watcher', now() + interval '7 hours', now() + interval '15 hours')
    RETURNING id INTO shift;
    INSERT INTO duty_members (shift_id, user_id)
    SELECT shift, id FROM users WHERE username = 'beat.guard'
      AND EXISTS (SELECT FROM role_assignments a WHERE a.workspace_id = workspace AND a.user_id = users.id);
    INSERT INTO duty_service_units VALUES (workspace, shift, territorial, 'ds.c2.d4.r7.b19');
END $$;
COMMIT;
