-- Role grants use a current path in the right workspace and hierarchy.

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO workspaces (id, slug, name) VALUES
    ('00000000-0000-0000-0000-000000000001', 'role-test', 'Role Test'),
    ('00000000-0000-0000-0000-000000000002', 'other-test', 'Other Test');
INSERT INTO hierarchies (id, workspace_id, slug, name, kind)
VALUES ('00000000-0000-0000-0000-000000000010', '00000000-0000-0000-0000-000000000001',
        'territorial', 'Territorial', 'territorial');
INSERT INTO hierarchy_levels (hierarchy_id, depth, slug, labels)
VALUES ('00000000-0000-0000-0000-000000000010', 0, 'state', '{"en":"State"}');
INSERT INTO units (hierarchy_id, depth, code, name, path, valid_from, valid_to) VALUES
    ('00000000-0000-0000-0000-000000000010', 0, 'DS', 'Demo State', 'ds', now(), NULL),
    ('00000000-0000-0000-0000-000000000010', 0, 'OLD', 'Old State', 'old', now() - interval '1 day', now());
INSERT INTO users (id, oidc_subject, username, display_name)
VALUES ('00000000-0000-0000-0000-000000000020', 'test-subject', 'test.officer', 'Test Officer');

SET CONSTRAINTS role_assignments_current_unit_fkey IMMEDIATE;
INSERT INTO role_assignments (user_id, workspace_id, hierarchy_id, unit_path, role_slug)
VALUES ('00000000-0000-0000-0000-000000000020', '00000000-0000-0000-0000-000000000001',
        '00000000-0000-0000-0000-000000000010', 'ds', 'state_admin');

DO $$
BEGIN
    INSERT INTO role_assignments (user_id, workspace_id, hierarchy_id, unit_path, role_slug)
    SELECT user_id, '00000000-0000-0000-0000-000000000002', hierarchy_id, unit_path, role_slug
    FROM role_assignments WHERE unit_path = 'ds';
    RAISE EXCEPTION 'a grant in the wrong workspace should be rejected';
EXCEPTION WHEN foreign_key_violation THEN NULL;
END $$;

DO $$
DECLARE
    bad_path ltree;
BEGIN
    FOREACH bad_path IN ARRAY ARRAY['missing', 'old']::ltree[] LOOP
        BEGIN
            INSERT INTO role_assignments (user_id, workspace_id, hierarchy_id, unit_path, role_slug)
            SELECT user_id, workspace_id, hierarchy_id, bad_path, role_slug
            FROM role_assignments WHERE unit_path = 'ds';
            RAISE EXCEPTION 'a grant at path % should be rejected', bad_path;
        EXCEPTION WHEN foreign_key_violation THEN NULL;
        END;
    END LOOP;
END $$;

SET CONSTRAINTS role_assignments_current_unit_fkey DEFERRED;
UPDATE units SET valid_to = now() + interval '1 second' WHERE path = 'ds';
INSERT INTO units (hierarchy_id, depth, code, name, path, valid_from)
VALUES ('00000000-0000-0000-0000-000000000010', 0, 'DS', 'Demo State (redrawn)', 'ds',
        now() + interval '1 second');
SET CONSTRAINTS role_assignments_current_unit_fkey IMMEDIATE;

DO $$
BEGIN
    DELETE FROM units WHERE current_path = 'ds';
    RAISE EXCEPTION 'deleting the only current unit with a grant should be rejected';
EXCEPTION WHEN foreign_key_violation THEN NULL;
END $$;

ROLLBACK;
