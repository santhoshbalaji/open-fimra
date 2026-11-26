BEGIN;
-- The roster cannot point at a different workspace, and grants open and
-- close without an update to the person's permanent role.
DO $$
DECLARE
    workspace uuid;
    hierarchy uuid;
    other_workspace uuid;
    other_hierarchy uuid;
    staff uuid;
    shift bigint;
BEGIN
    INSERT INTO workspaces(slug, name) VALUES ('test-duty', 'Test Duty') RETURNING id INTO workspace;
    INSERT INTO hierarchies(workspace_id, slug, name, kind)
    VALUES (workspace, 'test', 'Test', 'territorial') RETURNING id INTO hierarchy;
    INSERT INTO hierarchy_levels(hierarchy_id, depth, slug, labels) VALUES
        (hierarchy, 0, 'state', '{"en":"State"}'),
        (hierarchy, 1, 'division', '{"en":"Division"}');
    INSERT INTO units(hierarchy_id, depth, code, name, path) VALUES
        (hierarchy, 0, 'TEST', 'Test Duty', 'test'),
        (hierarchy, 1, 'A', 'Home Test Division', 'test.a'),
        (hierarchy, 1, 'B', 'Duty Test Division', 'test.b');
    INSERT INTO users(oidc_subject, username, display_name)
    VALUES ('test-duty', 'test.duty', 'Test Duty User') RETURNING id INTO staff;
    INSERT INTO role_assignments(user_id, workspace_id, hierarchy_id, unit_path, role_slug)
    VALUES (staff, workspace, hierarchy, 'test.a', 'field_staff');

    INSERT INTO duty_shifts(workspace_id, name, kind, starts_at, ends_at)
    VALUES (workspace, 'Test Watch', 'watcher', now() + interval '1 hour', now() + interval '2 hours')
    RETURNING id INTO shift;
    INSERT INTO duty_members VALUES (shift, staff);
    INSERT INTO duty_service_units VALUES (workspace, shift, hierarchy, 'test.b');
    INSERT INTO workspaces(slug, name) VALUES ('test-other-duty', 'Other Test') RETURNING id INTO other_workspace;
    INSERT INTO hierarchies(workspace_id, slug, name, kind)
    VALUES (other_workspace, 'other', 'Other Test', 'territorial') RETURNING id INTO other_hierarchy;
    BEGIN
        INSERT INTO duty_service_units VALUES (workspace, shift, other_hierarchy, 'other.b');
        RAISE EXCEPTION 'A service unit from another workspace was accepted';
    EXCEPTION WHEN foreign_key_violation THEN
        NULL;
    END;
    IF EXISTS (SELECT FROM alert_grants WHERE user_id = staff AND unit_path = 'test.b') THEN
        RAISE EXCEPTION 'A future shift granted access';
    END IF;
    UPDATE duty_shifts SET starts_at = statement_timestamp() WHERE id = shift;
    IF NOT EXISTS (SELECT FROM alert_grants WHERE user_id = staff AND unit_path = 'test.b'
                   AND role_slug = 'field_staff' AND NOT can_review) THEN
        RAISE EXCEPTION 'An active watcher shift did not grant field access only';
    END IF;
    UPDATE duty_shifts SET starts_at = now() - interval '1 hour', ends_at = statement_timestamp() WHERE id = shift;
    IF EXISTS (SELECT FROM alert_grants WHERE user_id = staff AND unit_path = 'test.b') OR
       NOT EXISTS (SELECT FROM alert_grants WHERE user_id = staff AND unit_path = 'test.a') THEN
        RAISE EXCEPTION 'An ended shift changed home access or still grants access';
    END IF;
END $$;
ROLLBACK;
