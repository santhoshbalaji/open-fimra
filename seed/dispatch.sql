-- Optional dispatch example for the synthetic workspace.
BEGIN;
SELECT pg_advisory_xact_lock(hashtext('open-fimra:demo-seed'));
DO $$
DECLARE
    workspace uuid;
    wildlife uuid;
    territorial uuid;
    buffer_rule bigint;
    shift bigint;
BEGIN
    SELECT id INTO STRICT workspace FROM workspaces WHERE slug = 'demo';
    SELECT id INTO STRICT territorial FROM hierarchies WHERE workspace_id = workspace AND slug = 'territorial';
    IF EXISTS (SELECT FROM dispatch_rules WHERE workspace_id = workspace AND name IN ('Demo reserve core', 'Revised reserve core')) THEN
        RETURN;
    END IF;
    SELECT id INTO STRICT wildlife FROM hierarchies WHERE workspace_id = workspace AND slug = 'reserve-response';

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
