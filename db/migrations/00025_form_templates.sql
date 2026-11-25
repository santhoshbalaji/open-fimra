-- Form templates (PLAN.md section 5.4): national, state and division forms
-- that extend each other where allowed. A national body publishes a
-- template, a state extends it, and a division adds to the state's form if
-- the state allows it. Locked parts of a template cannot change in the forms
-- that extend it. The API checks the locked parts; the database checks who
-- may extend what.

-- +goose Up
-- Who owns a form. A national template comes from a national body as a file
-- and is published on arrival. A division form belongs to one unit below the
-- top of a hierarchy, such as a division or a circle.
ALTER TABLE forms
    ADD COLUMN level text NOT NULL DEFAULT 'state' CHECK (level IN ('national', 'state', 'division')),
    ADD COLUMN owner_hierarchy_id uuid,
    ADD COLUMN owner_unit_path ltree,
    ADD CONSTRAINT forms_owner_hierarchy_fkey FOREIGN KEY (workspace_id, owner_hierarchy_id)
        REFERENCES hierarchies (workspace_id, id),
    ADD CONSTRAINT forms_division_owner CHECK ((level = 'division') = (owner_unit_path IS NOT NULL)),
    ADD CONSTRAINT forms_owner_complete CHECK ((owner_hierarchy_id IS NULL) = (owner_unit_path IS NULL)),
    ADD CONSTRAINT forms_owner_below_top CHECK (owner_unit_path IS NULL OR nlevel(owner_unit_path) > 1);

-- A form keeps its level and owner.
-- +goose StatementBegin
CREATE FUNCTION form_owner_fixed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.level IS DISTINCT FROM OLD.level OR NEW.owner_hierarchy_id IS DISTINCT FROM OLD.owner_hierarchy_id
       OR NEW.owner_unit_path IS DISTINCT FROM OLD.owner_unit_path OR NEW.workspace_id <> OLD.workspace_id THEN
        RAISE EXCEPTION 'a form keeps its workspace, level and owner' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER form_owner_fixed BEFORE UPDATE ON forms
    FOR EACH ROW EXECUTE FUNCTION form_owner_fixed();

-- Who may draft and publish a form: a workspace administrator at the root,
-- and for a division form also a supervising officer whose unit holds the
-- form's unit.
-- +goose StatementBegin
CREATE FUNCTION form_manager(subject text, form bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT FROM forms f JOIN alert_grants g ON g.workspace_id = f.workspace_id AND g.oidc_subject = subject
        WHERE f.id = form
          AND ((g.role_slug = 'state_admin' AND g.depth = 0)
               OR (f.level = 'division' AND g.can_review AND g.role_slug IN ('state_admin', 'division_officer')
                   AND g.hierarchy_id = f.owner_hierarchy_id AND f.owner_unit_path <@ g.unit_path)))
$$;
-- +goose StatementEnd

-- What a version allows the forms that extend it, frozen with the version:
-- {"allowExtension": true, "locked": ["fire", "reached_at"]}. Locked keys
-- name sections or items; a locked section or group locks everything in it.
ALTER TABLE form_versions
    ADD COLUMN template jsonb NOT NULL DEFAULT '{"allowExtension": false, "locked": []}'
        CHECK (COALESCE(jsonb_typeof(template->'allowExtension') = 'boolean'
                        AND jsonb_typeof(template->'locked') = 'array', false));
CREATE INDEX form_versions_extends_idx ON form_versions (extends_version_id) WHERE extends_version_id IS NOT NULL;

-- A version may extend only a published version of another form in the same
-- workspace, one level up, whose template allows extension.
-- +goose StatementBegin
CREATE FUNCTION form_version_extends() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    child forms;
    parent forms;
    parent_version form_versions;
BEGIN
    SELECT * INTO child FROM forms WHERE id = NEW.form_id;
    IF NEW.extends_version_id IS NULL THEN
        IF child.level = 'division' THEN
            RAISE EXCEPTION 'a division form extends a state form' USING ERRCODE = 'check_violation';
        END IF;
        RETURN NEW;
    END IF;
    SELECT * INTO parent_version FROM form_versions WHERE id = NEW.extends_version_id;
    SELECT * INTO parent FROM forms WHERE id = parent_version.form_id;
    IF parent_version.published_at IS NULL THEN
        RAISE EXCEPTION 'only a published version can be extended' USING ERRCODE = 'check_violation';
    END IF;
    IF parent.workspace_id <> child.workspace_id OR parent.id = child.id THEN
        RAISE EXCEPTION 'a form extends another form in its own workspace' USING ERRCODE = 'check_violation';
    END IF;
    IF (parent.level, child.level) NOT IN (('national', 'state'), ('state', 'division')) THEN
        RAISE EXCEPTION 'a % form cannot extend a % form', child.level, parent.level USING ERRCODE = 'check_violation';
    END IF;
    IF NOT (parent_version.template->>'allowExtension')::boolean THEN
        RAISE EXCEPTION 'this template does not allow extension' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER form_version_extends BEFORE INSERT OR UPDATE OF extends_version_id, form_id ON form_versions
    FOR EACH ROW EXECUTE FUNCTION form_version_extends();

-- A national template is never a draft in a workspace: it is published in
-- the same transaction that adds it.
-- +goose StatementBegin
CREATE FUNCTION form_national_published() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT FROM forms f JOIN form_versions v ON v.form_id = f.id
               WHERE v.id = NEW.id AND f.level = 'national' AND v.published_at IS NULL) THEN
        RAISE EXCEPTION 'a national template is published when it is added' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER form_national_published AFTER INSERT OR UPDATE ON form_versions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION form_national_published();

-- +goose Down
DROP TRIGGER form_national_published ON form_versions;
DROP FUNCTION form_national_published();
DROP TRIGGER form_version_extends ON form_versions;
DROP FUNCTION form_version_extends();
DROP INDEX form_versions_extends_idx;
ALTER TABLE form_versions DROP COLUMN template;
DROP FUNCTION form_manager(text, bigint);
DROP TRIGGER form_owner_fixed ON forms;
DROP FUNCTION form_owner_fixed();
ALTER TABLE forms DROP CONSTRAINT forms_owner_below_top, DROP CONSTRAINT forms_owner_complete,
    DROP CONSTRAINT forms_division_owner, DROP CONSTRAINT forms_owner_hierarchy_fkey,
    DROP COLUMN owner_unit_path, DROP COLUMN owner_hierarchy_id, DROP COLUMN level;
