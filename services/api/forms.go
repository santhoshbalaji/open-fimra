package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	formschema "github.com/santhoshbalaji/open-fimra/packages/form-schema"
)

// Forms belong to a workspace. A workspace administrator at the root of the
// hierarchy adds forms, edits drafts, publishes versions and assigns them to
// workflow steps. Anyone with a role in the workspace can read published
// versions, which the phone needs to fill them in.

// maxFormBytes is the largest form file the API accepts.
const maxFormBytes = 1 << 20

var formKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// apiProblem is an error with an HTTP status and a plain-English message,
// and optionally a list of problems, such as the validator's messages.
type apiProblem struct {
	status   int
	message  string
	problems []string
}

func (e *apiProblem) Error() string { return e.message }

func problem(status int, message string, problems ...string) error {
	return &apiProblem{status: status, message: message, problems: problems}
}

// writeProblem answers with an apiProblem, or logs any other error as a
// server fault. Missing rows answer HTTP 404 with notFound.
func (a api) writeProblem(w http.ResponseWriter, err error, notFound, failure string) {
	var p *apiProblem
	switch {
	case errors.As(err, &p):
		if p.problems != nil {
			writeJSON(w, p.status, map[string]any{"error": p.message, "problems": p.problems})
		} else {
			writeError(w, p.status, p.message)
		}
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, notFound)
	default:
		a.logger.Error(failure, "error", err)
		writeError(w, http.StatusInternalServerError, failure+".")
	}
}

// workspaceAccess is the caller's standing in a workspace.
type workspaceAccess struct {
	workspaceID string
	userID      string
	admin       bool
}

// formAccess finds the caller's roles in the workspace. A caller without one
// gets HTTP 403.
func formAccess(ctx context.Context, tx pgx.Tx, subject, slug string) (workspaceAccess, error) {
	var access workspaceAccess
	err := tx.QueryRow(ctx, `
SELECT w.id::text, min(g.user_id::text), bool_or(g.role_slug = 'state_admin' AND g.depth = 0)
FROM workspaces w JOIN alert_grants g ON g.workspace_id = w.id AND g.oidc_subject = $1
WHERE w.slug = $2 GROUP BY w.id`, subject, slug).Scan(&access.workspaceID, &access.userID, &access.admin)
	if errors.Is(err, pgx.ErrNoRows) {
		return access, problem(http.StatusForbidden, "A role in this workspace is required.")
	}
	return access, err
}

func requireFormAdmin(access workspaceAccess) error {
	if !access.admin {
		return problem(http.StatusForbidden, "A workspace administrator role at the root is required.")
	}
	return nil
}

// formRef names a version of a form in the same workspace.
type formRef struct {
	Form    string `json:"form"`
	Version int    `json:"version"`
}

// formUpload is a form file as sent, either alone or inside an envelope
// with its template settings:
//
//	{"level": "state", "extends": {"form": "first-report", "version": 1},
//	 "template": {"allowExtension": true, "locked": ["fire"]}, "definition": {...}}
type formUpload struct {
	source    []byte
	columns   []formschema.Column
	tree      *formTree
	key       string
	version   int
	envelope  bool
	level     string
	hierarchy string
	unitPath  string
	extends   *formRef
	template  json.RawMessage
}

// readFormUpload reads and validates a form file, or an envelope around one.
func readFormUpload(w http.ResponseWriter, r *http.Request) (*formUpload, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFormBytes+64<<10))
	if err != nil {
		return nil, problem(http.StatusRequestEntityTooLarge, "A form file can be at most 1 MB.")
	}
	u := &formUpload{source: body}
	var probe map[string]json.RawMessage
	if json.Unmarshal(body, &probe) == nil && probe["definition"] != nil {
		var env struct {
			Level      string          `json:"level"`
			Hierarchy  string          `json:"hierarchy"`
			UnitPath   string          `json:"unitPath"`
			Extends    *formRef        `json:"extends"`
			Template   json.RawMessage `json:"template"`
			Definition json.RawMessage `json:"definition"`
		}
		d := json.NewDecoder(bytes.NewReader(body))
		d.DisallowUnknownFields()
		if d.Decode(&env) != nil || d.Decode(new(any)) != io.EOF {
			return nil, problem(http.StatusBadRequest, "A form upload holds definition, and optionally level, hierarchy, unitPath, extends and template.")
		}
		if env.Level != "" && !slices.Contains(formLevels, env.Level) {
			return nil, problem(http.StatusUnprocessableEntity, `The level must be "national", "state" or "division".`)
		}
		if env.UnitPath != "" && !unitPrefix.MatchString(env.UnitPath) {
			return nil, problem(http.StatusUnprocessableEntity, "Invalid unit path.")
		}
		if env.Extends != nil && (!formKeyPattern.MatchString(env.Extends.Form) || env.Extends.Version < 1) {
			return nil, problem(http.StatusUnprocessableEntity, "extends needs the form and version of a published template.")
		}
		u.envelope, u.level, u.hierarchy, u.unitPath, u.extends, u.template = true, env.Level, env.Hierarchy, env.UnitPath, env.Extends, env.Template
		u.source = []byte(env.Definition)
	}
	if len(u.source) > maxFormBytes {
		return nil, problem(http.StatusRequestEntityTooLarge, "A form file can be at most 1 MB.")
	}
	columns, problems := formschema.Columns(u.source)
	if len(problems) > 0 {
		return nil, problem(http.StatusUnprocessableEntity, "The form is not valid.", problems...)
	}
	u.columns = columns
	if u.tree, err = parseFormTree(u.source); err != nil {
		return nil, err
	}
	var head struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	_ = json.Unmarshal(u.source, &head)
	u.key, u.version = head.ID, head.Version
	return u, nil
}

func formRoute(r *http.Request) (string, int, error) {
	key := chi.URLParam(r, "form")
	if !formKeyPattern.MatchString(key) || len(key) > 60 {
		return "", 0, problem(http.StatusNotFound, "Form not found.")
	}
	raw := chi.URLParam(r, "version")
	if raw == "" {
		return key, 0, nil
	}
	version, err := strconv.Atoi(raw)
	if err != nil || version < 1 || strconv.Itoa(version) != raw {
		return "", 0, problem(http.StatusNotFound, "Form version not found.")
	}
	return key, version, nil
}

// formVersionJSON describes one version. Drafts are shown only to the
// people who manage the form.
const formVersionJSON = `jsonb_build_object('form', f.form_key, 'version', v.version,
    'title', v.definition->'title', 'level', f.level,
    'unit', CASE WHEN f.owner_unit_path IS NOT NULL THEN jsonb_build_object(
        'hierarchy', (SELECT h.slug FROM hierarchies h WHERE h.id = f.owner_hierarchy_id), 'path', f.owner_unit_path::text) END,
    'status', CASE WHEN v.published_at IS NULL THEN 'draft' ELSE 'published' END,
    'createdAt', v.created_at, 'updatedAt', v.updated_at, 'publishedAt', v.published_at,
    'extendsVersionId', v.extends_version_id::text,
    'extends', (SELECT jsonb_build_object('form', pf.form_key, 'version', pv.version, 'level', pf.level)
        FROM form_versions pv JOIN forms pf ON pf.id = pv.form_id WHERE pv.id = v.extends_version_id),
    'template', v.template,
    'columns', v.columns,
    'views', (SELECT COALESCE(jsonb_object_agg(CASE WHEN fv.repeat_key = '' THEN 'submissions' ELSE fv.repeat_key END,
        'form_views.' || fv.view_name), '{}') FROM form_version_views fv WHERE fv.form_version_id = v.id))`

// formManaged adds what only the form's managers see: how many submissions
// each version has, so they can tell which versions are in use.
func formManaged(subject string) string {
	return `|| CASE WHEN form_manager(` + subject + `, f.id) THEN jsonb_build_object('canManage', true,
    'submissionCount', (SELECT count(*) FROM submissions s WHERE s.form_version_id = v.id)) ELSE '{}' END`
}

func (a api) forms(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	var result json.RawMessage
	status := http.StatusOK
	var upload *formUpload
	var err error
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		upload, err = readFormUpload(w, r)
	}
	if err == nil {
		err = pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
			access, err := formAccess(ctx, tx, subject, slug)
			if err != nil {
				return err
			}
			switch {
			case r.Method == http.MethodGet && chi.URLParam(r, "form") == "":
				return tx.QueryRow(ctx, `
SELECT COALESCE(jsonb_agg(jsonb_build_object('form', f.form_key, 'level', f.level,
    'unit', CASE WHEN f.owner_unit_path IS NOT NULL THEN jsonb_build_object(
        'hierarchy', (SELECT h.slug FROM hierarchies h WHERE h.id = f.owner_hierarchy_id), 'path', f.owner_unit_path::text) END,
    'canManage', form_manager($3, f.id),
    'versions', (SELECT jsonb_agg(`+formVersionJSON+` - 'columns' - 'views' `+formManaged("$3")+` ORDER BY v.version)
        FROM form_versions v WHERE v.form_id = f.id AND ($2 OR v.published_at IS NOT NULL OR form_manager($3, f.id))),
    'assignments', (SELECT COALESCE(jsonb_agg(jsonb_build_object('step', fa.step, 'version', av.version,
        'hierarchy', h.slug, 'unitPaths', to_jsonb(fa.unit_paths::text[])) ORDER BY fa.step), '[]')
        FROM form_assignments fa JOIN form_versions av ON av.id = fa.form_version_id
        LEFT JOIN hierarchies h ON h.id = fa.hierarchy_id WHERE fa.form_id = f.id))
    ORDER BY array_position(ARRAY['national', 'state', 'division'], f.level), f.form_key), '[]')
FROM forms f WHERE f.workspace_id = $1
  AND ($2 OR form_manager($3, f.id) OR EXISTS (SELECT FROM form_versions v WHERE v.form_id = f.id AND v.published_at IS NOT NULL))`,
					access.workspaceID, access.admin, subject).Scan(&result)
			case r.Method == http.MethodGet:
				key, version, err := formRoute(r)
				if err != nil {
					return err
				}
				return tx.QueryRow(ctx, `
SELECT `+formVersionJSON+` || jsonb_build_object('definition', v.source::jsonb) `+formManaged("$3")+`
FROM forms f JOIN form_versions v ON v.form_id = f.id
WHERE f.workspace_id = $1 AND f.form_key = $2 AND v.version = $4 AND (v.published_at IS NOT NULL OR form_manager($3, f.id))`,
					access.workspaceID, key, subject, version).Scan(&result)
			case r.Method == http.MethodPost:
				status = http.StatusCreated
				return createFormVersion(ctx, tx, subject, access, upload, &result)
			default:
				key, version, err := formRoute(r)
				if err != nil {
					return err
				}
				if key != upload.key || version != upload.version {
					return problem(http.StatusUnprocessableEntity, "The form file's id and version must match the address.")
				}
				return updateFormDraft(ctx, tx, subject, access, upload, &result)
			}
		})
	}
	if err != nil {
		a.writeProblem(w, err, "Form version not found.", "Could not load or save the form")
		return
	}
	if status == http.StatusCreated {
		w.Header().Set("Location", "/api/v1/workspaces/"+slug+"/forms/"+upload.key+"/versions/"+strconv.Itoa(upload.version))
	}
	writeJSON(w, status, result)
}

// storedForm is a form row with its owner.
type storedForm struct {
	id        int64
	level     string
	hierarchy string
	unitPath  string
}

// findOrAddForm finds the form an upload belongs to, or adds it at the
// upload's level, and checks that the caller manages it. The form row is
// locked, so two new versions cannot take the same number.
func findOrAddForm(ctx context.Context, tx pgx.Tx, subject string, access workspaceAccess, u *formUpload, add bool) (storedForm, error) {
	var f storedForm
	err := tx.QueryRow(ctx, `SELECT f.id, f.level, COALESCE(h.slug, ''), COALESCE(f.owner_unit_path::text, '')
FROM forms f LEFT JOIN hierarchies h ON h.id = f.owner_hierarchy_id
WHERE f.workspace_id = $1 AND f.form_key = $2 FOR UPDATE OF f`, access.workspaceID, u.key).Scan(&f.id, &f.level, &f.hierarchy, &f.unitPath)
	switch {
	case errors.Is(err, pgx.ErrNoRows) && !add:
		return f, err
	case errors.Is(err, pgx.ErrNoRows):
		f.level = u.level
		if f.level == "" {
			f.level = "state"
		}
		if f.level != "division" && (u.hierarchy != "" || u.unitPath != "") {
			return f, problem(http.StatusUnprocessableEntity, "Only a division form has a hierarchy and unit path.")
		}
		if f.level != "division" {
			if err := requireFormAdmin(access); err != nil {
				return f, err
			}
			err = tx.QueryRow(ctx, `INSERT INTO forms (workspace_id, form_key, level, created_by) VALUES ($1, $2, $3, $4) RETURNING id`,
				access.workspaceID, u.key, f.level, access.userID).Scan(&f.id)
			return f, err
		}
		if u.hierarchy == "" || u.unitPath == "" {
			return f, problem(http.StatusUnprocessableEntity, "A division form needs the hierarchy and unit path of the unit that owns it.")
		}
		var hierarchyID string
		err = tx.QueryRow(ctx, `SELECT h.id::text FROM hierarchies h JOIN units u ON u.hierarchy_id = h.id
WHERE h.workspace_id = $1 AND h.slug = $2 AND u.current_path = $3::ltree AND nlevel(u.current_path) > 1`,
			access.workspaceID, u.hierarchy, u.unitPath).Scan(&hierarchyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return f, problem(http.StatusUnprocessableEntity, "No current unit below the top of that hierarchy has the path "+u.unitPath+".")
		}
		if err != nil {
			return f, err
		}
		f.hierarchy, f.unitPath = u.hierarchy, u.unitPath
		err = tx.QueryRow(ctx, `INSERT INTO forms (workspace_id, form_key, level, owner_hierarchy_id, owner_unit_path, created_by)
VALUES ($1, $2, 'division', $3, $4::ltree, $5) RETURNING id`, access.workspaceID, u.key, hierarchyID, u.unitPath, access.userID).Scan(&f.id)
		if err != nil {
			return f, err
		}
	case err != nil:
		return f, err
	default:
		if u.level != "" && u.level != f.level {
			return f, problem(http.StatusUnprocessableEntity, "This form is a "+f.level+" form. A form keeps its level.")
		}
		if (u.hierarchy != "" && u.hierarchy != f.hierarchy) || (u.unitPath != "" && u.unitPath != f.unitPath) {
			return f, problem(http.StatusUnprocessableEntity, "A division form keeps the unit that owns it.")
		}
	}
	var manager bool
	if err := tx.QueryRow(ctx, `SELECT form_manager($1, $2)`, subject, f.id).Scan(&manager); err != nil {
		return f, err
	}
	if !manager {
		if f.level == "division" {
			return f, problem(http.StatusForbidden, "A supervising officer for "+f.unitPath+" or a workspace administrator at the root is required.")
		}
		return f, problem(http.StatusForbidden, "A workspace administrator role at the root is required.")
	}
	return f, nil
}

// templateSettings works out a version's template rules and the version it
// extends, and checks the form against the locked parts of that version.
func templateSettings(ctx context.Context, tx pgx.Tx, access workspaceAccess, f storedForm, u *formUpload, extends *formRef, rawRules json.RawMessage) (*int64, templateRules, error) {
	rules, problems := parseTemplateRules(rawRules, u.tree)
	if len(problems) > 0 {
		return nil, rules, problem(http.StatusUnprocessableEntity, "The template rules are not valid.", problems...)
	}
	if f.level == "division" && rules.AllowExtension {
		return nil, rules, problem(http.StatusUnprocessableEntity, "Division forms are the lowest level, so no form can extend them.")
	}
	if extends == nil {
		if f.level == "division" {
			return nil, rules, problem(http.StatusUnprocessableEntity, "A division form must extend a state form. Name it in extends.")
		}
		return nil, rules, nil
	}
	want := parentLevel(f.level)
	if want == "" {
		return nil, rules, problem(http.StatusUnprocessableEntity, "A national template comes from a national body and does not extend another form.")
	}
	if extends.Form == u.key {
		return nil, rules, problem(http.StatusUnprocessableEntity, "A form cannot extend itself. Give the new form its own id.")
	}
	var parentID int64
	var level, source string
	var published bool
	var rulesJSON []byte
	err := tx.QueryRow(ctx, `SELECT v.id, pf.level, v.source, v.published_at IS NOT NULL, v.template
FROM forms pf JOIN form_versions v ON v.form_id = pf.id
WHERE pf.workspace_id = $1 AND pf.form_key = $2 AND v.version = $3`, access.workspaceID, extends.Form, extends.Version).Scan(&parentID, &level, &source, &published, &rulesJSON)
	name := extends.Form + " version " + strconv.Itoa(extends.Version)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !published) {
		return nil, rules, problem(http.StatusUnprocessableEntity, "The form extends "+name+", which is not a published version in this workspace.")
	}
	if err != nil {
		return nil, rules, err
	}
	if level != want {
		words := map[string]string{"state": "A state form can extend only a national template.", "division": "A division form can extend only a state form."}
		return nil, rules, problem(http.StatusUnprocessableEntity, words[f.level])
	}
	var parentRules templateRules
	if err := json.Unmarshal(rulesJSON, &parentRules); err != nil {
		return nil, rules, err
	}
	if !parentRules.AllowExtension {
		return nil, rules, problem(http.StatusUnprocessableEntity, name+" does not allow other forms to extend it.")
	}
	problems, err = checkExtension([]byte(source), parentRules, u.source)
	if err != nil {
		return nil, rules, err
	}
	if len(problems) > 0 {
		return nil, rules, problem(http.StatusUnprocessableEntity, "The form changes locked parts of "+name+".", problems...)
	}
	return &parentID, mergeLocks(rules, parentRules), nil
}

func createFormVersion(ctx context.Context, tx pgx.Tx, subject string, access workspaceAccess, u *formUpload, result *json.RawMessage) error {
	f, err := findOrAddForm(ctx, tx, subject, access, u, true)
	if err != nil {
		return err
	}
	var latest int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(version), 0) FROM form_versions WHERE form_id = $1`, f.id).Scan(&latest); err != nil {
		return err
	}
	if u.version <= latest {
		return problem(http.StatusConflict, "Version "+strconv.Itoa(u.version)+" is not newer than version "+strconv.Itoa(latest)+
			" of this form. Change a draft with PUT, or give the new version the next number.")
	}
	extendsID, rules, err := templateSettings(ctx, tx, access, f, u, u.extends, u.template)
	if err != nil {
		return err
	}
	columnJSON, err := json.Marshal(u.columns)
	if err != nil {
		return err
	}
	rulesJSON, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	var id int64
	if err := tx.QueryRow(ctx, `
INSERT INTO form_versions (form_id, version, source, definition, columns, template, extends_version_id, created_by)
VALUES ($1, $2, $3::text, $3::jsonb, $4, $5, $6, $7) RETURNING id`,
		f.id, u.version, string(u.source), columnJSON, rulesJSON, extendsID, access.userID).Scan(&id); err != nil {
		return err
	}
	// A national template arrives from the national body ready to use, so
	// nobody in the workspace edits it as a draft.
	if f.level == "national" {
		if err := publishFormVersion(ctx, tx, id, string(u.source), access.userID); err != nil {
			return err
		}
	}
	return versionResult(ctx, tx, id, subject, result)
}

func versionResult(ctx context.Context, tx pgx.Tx, id int64, subject string, result *json.RawMessage) error {
	return tx.QueryRow(ctx, `SELECT `+formVersionJSON+` `+formManaged("$2")+`
FROM form_versions v JOIN forms f ON f.id = v.form_id WHERE v.id = $1`, id, subject).Scan(result)
}

func updateFormDraft(ctx context.Context, tx pgx.Tx, subject string, access workspaceAccess, u *formUpload, result *json.RawMessage) error {
	f, err := findOrAddForm(ctx, tx, subject, access, u, false)
	if err != nil {
		return err
	}
	if f.level == "national" {
		return problem(http.StatusConflict, "A national template arrives published and never changes. Add the national body's next version instead.")
	}
	var id int64
	var published bool
	var rulesJSON []byte
	var extends *formRef
	err = tx.QueryRow(ctx, `SELECT v.id, v.published_at IS NOT NULL, v.template,
    (SELECT jsonb_build_object('form', pf.form_key, 'version', pv.version) FROM form_versions pv JOIN forms pf ON pf.id = pv.form_id
     WHERE pv.id = v.extends_version_id)
FROM form_versions v WHERE v.form_id = $1 AND v.version = $2 FOR UPDATE`, f.id, u.version).Scan(&id, &published, &rulesJSON, &extends)
	if err != nil {
		return err
	}
	if published {
		return problem(http.StatusConflict, "Published versions never change. Add a new version instead.")
	}
	// With a plain form file the draft keeps its template settings. An
	// envelope replaces them.
	rawRules := json.RawMessage(rulesJSON)
	if u.envelope {
		extends, rawRules = u.extends, u.template
	} else {
		// Keep the locks on keys the new file still has. If it drops a key
		// the parent locked, the extension check says so.
		var stored templateRules
		if err := json.Unmarshal(rulesJSON, &stored); err != nil {
			return err
		}
		stored.Locked = slices.DeleteFunc(stored.Locked, func(key string) bool { return u.tree.byKey[key] == nil })
		rawRules, _ = json.Marshal(stored)
	}
	extendsID, rules, err := templateSettings(ctx, tx, access, f, u, extends, rawRules)
	if err != nil {
		return err
	}
	columnJSON, err := json.Marshal(u.columns)
	if err != nil {
		return err
	}
	newRules, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE form_versions SET source = $2::text, definition = $2::jsonb, columns = $3, template = $4,
    extends_version_id = $5, updated_at = now() WHERE id = $1`, id, string(u.source), columnJSON, newRules, extendsID); err != nil {
		return err
	}
	return versionResult(ctx, tx, id, subject, result)
}

// publishForm publishes a draft. Publishing a published version again
// returns it unchanged.
func (a api) publishForm(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	var result json.RawMessage
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, chi.URLParam(r, "slug"))
		if err != nil {
			return err
		}
		key, version, err := formRoute(r)
		if err != nil {
			return err
		}
		var id int64
		var published, manager bool
		var source string
		var rulesJSON []byte
		var extends *formRef
		err = tx.QueryRow(ctx, `SELECT v.id, v.published_at IS NOT NULL, v.source, v.template, form_manager($4, f.id),
    (SELECT jsonb_build_object('form', pf.form_key, 'version', pv.version) FROM form_versions pv JOIN forms pf ON pf.id = pv.form_id
     WHERE pv.id = v.extends_version_id)
FROM forms f JOIN form_versions v ON v.form_id = f.id
WHERE f.workspace_id = $1 AND f.form_key = $2 AND v.version = $3 FOR UPDATE OF v`, access.workspaceID, key, version, subject).Scan(&id, &published, &source, &rulesJSON, &manager, &extends)
		if err != nil {
			return err
		}
		if !manager {
			return problem(http.StatusForbidden, "Only the people who manage this form can publish it.")
		}
		if !published {
			if extends != nil {
				// The template it extends is frozen, but the checks may be newer.
				var parentSource string
				var parentRules []byte
				if err := tx.QueryRow(ctx, `SELECT pv.source, pv.template FROM form_versions v JOIN form_versions pv ON pv.id = v.extends_version_id
WHERE v.id = $1`, id).Scan(&parentSource, &parentRules); err != nil {
					return err
				}
				var rules templateRules
				if err := json.Unmarshal(parentRules, &rules); err != nil {
					return err
				}
				problems, err := checkExtension([]byte(parentSource), rules, []byte(source))
				if err != nil {
					return err
				}
				if len(problems) > 0 {
					return problem(http.StatusUnprocessableEntity, "The form changes locked parts of "+extends.Form+" version "+strconv.Itoa(extends.Version)+".", problems...)
				}
			}
			if err := publishFormVersion(ctx, tx, id, source, access.userID); err != nil {
				return err
			}
		}
		return versionResult(ctx, tx, id, subject, &result)
	})
	if err != nil {
		a.writeProblem(w, err, "Form version not found.", "Could not publish the form")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// publishFormVersion freezes a draft and creates its flat views. The caller
// holds a lock on the version and knows it is a draft.
func publishFormVersion(ctx context.Context, tx pgx.Tx, id int64, source, userID string) error {
	// Check again: the validator may be newer than the draft.
	columns, problems := formschema.Columns([]byte(source))
	if len(problems) > 0 {
		return problem(http.StatusUnprocessableEntity, "The form is not valid.", problems...)
	}
	tree, err := parseFormTree([]byte(source))
	if err != nil {
		return err
	}
	views, err := formViews(id, tree, columns)
	if err != nil {
		return err
	}
	for _, view := range views {
		if _, err := tx.Exec(ctx, view.SQL); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO form_version_views (form_version_id, repeat_key, view_name) VALUES ($1, $2, $3)`,
			id, view.RepeatKey, view.Name); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE form_versions SET published_at = now(), published_by = $2 WHERE id = $1`, id, userID)
	return err
}

// workspaceTemplate is the workflow template new alerts in a workspace use:
// the workspace's own, or else the shared default.
const workspaceTemplate = `SELECT workspace_workflow_template($1)`
const workflowFormsLock = `SELECT pg_advisory_xact_lock(hashtextextended('open-fimra:starter-pack:' || $1, 0))`

// formStep is one form version placed on a workflow step.
type formStep struct {
	formID, versionID, templateID int64
	key, step                     string
	version                       int
	hierarchyID                   *string
	unitPaths                     []string
}

// saveAssignment puts a published version on a step, replacing the form's
// earlier version there, and records it in the audit log.
func saveAssignment(ctx context.Context, tx pgx.Tx, access workspaceAccess, subject string, a formStep) error {
	if a.unitPaths == nil {
		a.unitPaths = []string{}
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO form_assignments (form_id, form_version_id, template_id, step, hierarchy_id, unit_paths, assigned_by)
VALUES ($1, $2, $3, $4, $5, $6::text[]::ltree[], $7)
ON CONFLICT (form_id, template_id, step) DO UPDATE SET form_version_id = EXCLUDED.form_version_id,
    hierarchy_id = EXCLUDED.hierarchy_id, unit_paths = EXCLUDED.unit_paths,
    assigned_by = EXCLUDED.assigned_by, assigned_at = now()`,
		a.formID, a.versionID, a.templateID, a.step, a.hierarchyID, a.unitPaths, access.userID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
VALUES ($1, 'user', $2, 'form.assign', 'form', $3, jsonb_build_object('step', $4::text, 'version', $5::int, 'unitPaths', $6::text[]))`,
		access.workspaceID, subject, a.key, a.step, a.version, a.unitPaths)
	return err
}

type assignmentRequest struct {
	Version   int      `json:"version"`
	Hierarchy string   `json:"hierarchy"`
	UnitPaths []string `json:"unitPaths"`
}

// formAssignment puts a published version on a workflow step, or takes a
// form off a step. The step belongs to the template new alerts in the
// workspace use.
func (a api) formAssignment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	step := chi.URLParam(r, "step")
	var input assignmentRequest
	if r.Method == http.MethodPut {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || input.Version < 1 || len(input.UnitPaths) > 200 ||
			(input.Hierarchy == "") != (len(input.UnitPaths) == 0) {
			writeError(w, http.StatusBadRequest, "Provide a version, and a hierarchy with unit paths or neither.")
			return
		}
		for _, p := range input.UnitPaths {
			if !unitPrefix.MatchString(p) {
				writeError(w, http.StatusBadRequest, "Invalid unit path.")
				return
			}
		}
		slices.Sort(input.UnitPaths)
		input.UnitPaths = slices.Compact(input.UnitPaths)
	}
	var result json.RawMessage
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, chi.URLParam(r, "slug"))
		if err == nil {
			err = requireFormAdmin(access)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, workflowFormsLock, access.workspaceID); err != nil {
			return err
		}
		key, _, err := formRoute(r)
		if err != nil {
			return err
		}
		if !stepSlug.MatchString(step) {
			return problem(http.StatusNotFound, "Workflow step not found.")
		}
		var formID, templateID int64
		var stepFound bool
		var level, ownerHierarchy, ownerPath string
		err = tx.QueryRow(ctx, `
SELECT f.id, t.id, EXISTS (SELECT FROM workflow_steps s WHERE s.template_id = t.id AND s.slug = $3),
    f.level, COALESCE(f.owner_hierarchy_id::text, ''), COALESCE(f.owner_unit_path::text, '')
FROM forms f CROSS JOIN LATERAL (SELECT workspace_workflow_template(f.workspace_id) AS id) t
WHERE f.workspace_id = $1 AND f.form_key = $2 FOR UPDATE OF f`, access.workspaceID, key, step).Scan(&formID, &templateID, &stepFound,
			&level, &ownerHierarchy, &ownerPath)
		if err != nil {
			return err
		}
		if !stepFound {
			return problem(http.StatusNotFound, "Workflow step not found.")
		}
		if r.Method == http.MethodDelete {
			tag, err := tx.Exec(ctx, `DELETE FROM form_assignments WHERE form_id = $1 AND template_id = $2 AND step = $3`, formID, templateID, step)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return problem(http.StatusNotFound, "This form is not assigned to that step.")
			}
			_, err = tx.Exec(ctx, `INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
VALUES ($1, 'user', $2, 'form.unassign', 'form', $3, jsonb_build_object('step', $4::text))`, access.workspaceID, subject, key, step)
			return err
		}
		var versionID int64
		var published bool
		var source string
		err = tx.QueryRow(ctx, `SELECT id, published_at IS NOT NULL, source FROM form_versions WHERE form_id = $1 AND version = $2`,
			formID, input.Version).Scan(&versionID, &published, &source)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !published) {
			return problem(http.StatusUnprocessableEntity, "Only a published version can be assigned.")
		}
		if err != nil {
			return err
		}
		tree, err := parseFormTree([]byte(source))
		if err != nil {
			return err
		}
		if _, err := tree.stageAt(step); err != nil {
			return problem(http.StatusUnprocessableEntity, "This form has stages. Assign it at the workflow step of one of its stages.")
		}
		var hierarchyID *string
		if input.Hierarchy != "" {
			var missing []string
			err := tx.QueryRow(ctx, `SELECT h.id::text, COALESCE(array_agg(p.path) FILTER (WHERE u.path IS NULL), '{}')
FROM hierarchies h CROSS JOIN unnest($3::text[]) p(path)
LEFT JOIN units u ON u.hierarchy_id = h.id AND u.current_path = p.path::ltree
WHERE h.workspace_id = $1 AND h.slug = $2 GROUP BY h.id`, access.workspaceID, input.Hierarchy, input.UnitPaths).Scan(&hierarchyID, &missing)
			if errors.Is(err, pgx.ErrNoRows) {
				return problem(http.StatusUnprocessableEntity, "Unknown hierarchy.")
			}
			if err != nil {
				return err
			}
			if len(missing) > 0 {
				return problem(http.StatusUnprocessableEntity, "No current unit has the path "+strings.Join(missing, ", ")+".")
			}
		}
		// A division form is used only under the unit that owns it.
		if level == "division" {
			inside := hierarchyID != nil && *hierarchyID == ownerHierarchy
			for _, p := range input.UnitPaths {
				inside = inside && (p == ownerPath || strings.HasPrefix(p, ownerPath+"."))
			}
			if !inside {
				return problem(http.StatusUnprocessableEntity, "A division form can be assigned only under its own unit, "+ownerPath+".")
			}
		}
		if input.UnitPaths == nil {
			input.UnitPaths = []string{}
		}
		if err := saveAssignment(ctx, tx, access, subject, formStep{formID: formID, versionID: versionID, templateID: templateID,
			key: key, step: step, version: input.Version, hierarchyID: hierarchyID, unitPaths: input.UnitPaths}); err != nil {
			return err
		}
		result, err = json.Marshal(map[string]any{"form": key, "step": step, "version": input.Version,
			"hierarchy": input.Hierarchy, "unitPaths": input.UnitPaths})
		return err
	})
	if err != nil {
		a.writeProblem(w, err, "Form not found.", "Could not save the form assignment")
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
