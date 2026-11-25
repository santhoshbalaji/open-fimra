package main

import (
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

// readFormFile reads and validates a form file. It returns the exact text,
// the columns and the form's id and version.
func readFormFile(w http.ResponseWriter, r *http.Request) ([]byte, []formschema.Column, string, int, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFormBytes))
	if err != nil {
		return nil, nil, "", 0, problem(http.StatusRequestEntityTooLarge, "A form file can be at most 1 MB.")
	}
	columns, problems := formschema.Columns(body)
	if len(problems) > 0 {
		return nil, nil, "", 0, problem(http.StatusUnprocessableEntity, "The form is not valid.", problems...)
	}
	var head struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	_ = json.Unmarshal(body, &head)
	return body, columns, head.ID, head.Version, nil
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

// formVersionJSON describes one version. Drafts are shown only to administrators.
const formVersionJSON = `jsonb_build_object('form', f.form_key, 'version', v.version,
    'title', v.definition->'title',
    'status', CASE WHEN v.published_at IS NULL THEN 'draft' ELSE 'published' END,
    'createdAt', v.created_at, 'updatedAt', v.updated_at, 'publishedAt', v.published_at,
    'extendsVersionId', v.extends_version_id::text,
    'columns', v.columns,
    'views', (SELECT COALESCE(jsonb_object_agg(CASE WHEN fv.repeat_key = '' THEN 'submissions' ELSE fv.repeat_key END,
        'form_views.' || fv.view_name), '{}') FROM form_version_views fv WHERE fv.form_version_id = v.id))`

func (a api) forms(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	var result json.RawMessage
	status := http.StatusOK
	var body []byte
	var columns []formschema.Column
	var fileKey string
	var fileVersion int
	var err error
	if r.Method == http.MethodPost || r.Method == http.MethodPut {
		body, columns, fileKey, fileVersion, err = readFormFile(w, r)
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
SELECT COALESCE(jsonb_agg(jsonb_build_object('form', f.form_key,
    'versions', (SELECT jsonb_agg(`+formVersionJSON+` - 'columns' - 'views' ORDER BY v.version)
        FROM form_versions v WHERE v.form_id = f.id AND ($2 OR v.published_at IS NOT NULL)),
    'assignments', (SELECT COALESCE(jsonb_agg(jsonb_build_object('step', fa.step, 'version', av.version,
        'hierarchy', h.slug, 'unitPaths', to_jsonb(fa.unit_paths::text[])) ORDER BY fa.step), '[]')
        FROM form_assignments fa JOIN form_versions av ON av.id = fa.form_version_id
        LEFT JOIN hierarchies h ON h.id = fa.hierarchy_id WHERE fa.form_id = f.id)) ORDER BY f.form_key), '[]')
FROM forms f WHERE f.workspace_id = $1
  AND ($2 OR EXISTS (SELECT FROM form_versions v WHERE v.form_id = f.id AND v.published_at IS NOT NULL))`,
					access.workspaceID, access.admin).Scan(&result)
			case r.Method == http.MethodGet:
				key, version, err := formRoute(r)
				if err != nil {
					return err
				}
				return tx.QueryRow(ctx, `
SELECT `+formVersionJSON+` || jsonb_build_object('definition', v.source::jsonb)
FROM forms f JOIN form_versions v ON v.form_id = f.id
WHERE f.workspace_id = $1 AND f.form_key = $2 AND v.version = $3 AND ($4 OR v.published_at IS NOT NULL)`,
					access.workspaceID, key, version, access.admin).Scan(&result)
			case r.Method == http.MethodPost:
				if err := requireFormAdmin(access); err != nil {
					return err
				}
				status = http.StatusCreated
				return createFormVersion(ctx, tx, access, body, columns, fileKey, fileVersion, &result)
			default:
				if err := requireFormAdmin(access); err != nil {
					return err
				}
				key, version, err := formRoute(r)
				if err != nil {
					return err
				}
				if key != fileKey || version != fileVersion {
					return problem(http.StatusUnprocessableEntity, "The form file's id and version must match the address.")
				}
				return updateFormDraft(ctx, tx, access, body, columns, key, version, &result)
			}
		})
	}
	if err != nil {
		a.writeProblem(w, err, "Form version not found.", "Could not load or save the form")
		return
	}
	if status == http.StatusCreated {
		w.Header().Set("Location", "/api/v1/workspaces/"+slug+"/forms/"+fileKey+"/versions/"+strconv.Itoa(fileVersion))
	}
	writeJSON(w, status, result)
}

func createFormVersion(ctx context.Context, tx pgx.Tx, access workspaceAccess, body []byte, columns []formschema.Column, key string, version int, result *json.RawMessage) error {
	columnJSON, err := json.Marshal(columns)
	if err != nil {
		return err
	}
	var formID int64
	err = tx.QueryRow(ctx, `
WITH added AS (
    INSERT INTO forms (workspace_id, form_key, created_by) VALUES ($1, $2, $3)
    ON CONFLICT (workspace_id, form_key) DO NOTHING RETURNING id
)
SELECT id FROM added UNION ALL SELECT id FROM forms WHERE workspace_id = $1 AND form_key = $2`,
		access.workspaceID, key, access.userID).Scan(&formID)
	if err != nil {
		return err
	}
	// Lock the form so two new versions cannot take the same number.
	var latest int
	if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT max(version) FROM form_versions WHERE form_id = $1), 0)
FROM forms WHERE id = $1 FOR UPDATE`, formID).Scan(&latest); err != nil {
		return err
	}
	if version <= latest {
		return problem(http.StatusConflict, "Version "+strconv.Itoa(version)+" is not newer than version "+strconv.Itoa(latest)+
			" of this form. Change a draft with PUT, or give the new version the next number.")
	}
	return tx.QueryRow(ctx, `
WITH v AS (
    INSERT INTO form_versions (form_id, version, source, definition, columns, created_by)
    VALUES ($1, $2, $3::text, $3::jsonb, $4, $5) RETURNING *
)
SELECT `+formVersionJSON+` FROM v JOIN forms f ON f.id = v.form_id`,
		formID, version, string(body), columnJSON, access.userID).Scan(result)
}

func updateFormDraft(ctx context.Context, tx pgx.Tx, access workspaceAccess, body []byte, columns []formschema.Column, key string, version int, result *json.RawMessage) error {
	var id int64
	var published bool
	err := tx.QueryRow(ctx, `SELECT v.id, v.published_at IS NOT NULL FROM forms f JOIN form_versions v ON v.form_id = f.id
WHERE f.workspace_id = $1 AND f.form_key = $2 AND v.version = $3 FOR UPDATE OF v`, access.workspaceID, key, version).Scan(&id, &published)
	if err != nil {
		return err
	}
	if published {
		return problem(http.StatusConflict, "Published versions never change. Add a new version instead.")
	}
	columnJSON, err := json.Marshal(columns)
	if err != nil {
		return err
	}
	return tx.QueryRow(ctx, `
WITH v AS (
    UPDATE form_versions SET source = $2::text, definition = $2::jsonb, columns = $3, updated_at = now()
    WHERE id = $1 RETURNING *
)
SELECT `+formVersionJSON+` FROM v JOIN forms f ON f.id = v.form_id`, id, string(body), columnJSON).Scan(result)
}

// publishForm freezes a draft and creates its flat views in the same
// transaction. Publishing a published version again returns it unchanged.
func (a api) publishForm(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	var result json.RawMessage
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, chi.URLParam(r, "slug"))
		if err == nil {
			err = requireFormAdmin(access)
		}
		if err != nil {
			return err
		}
		key, version, err := formRoute(r)
		if err != nil {
			return err
		}
		var id int64
		var published bool
		var source string
		var columnJSON []byte
		err = tx.QueryRow(ctx, `SELECT v.id, v.published_at IS NOT NULL, v.source, v.columns FROM forms f JOIN form_versions v ON v.form_id = f.id
WHERE f.workspace_id = $1 AND f.form_key = $2 AND v.version = $3 FOR UPDATE OF v`, access.workspaceID, key, version).Scan(&id, &published, &source, &columnJSON)
		if err != nil {
			return err
		}
		if !published {
			if err := publishFormVersion(ctx, tx, id, source, access.userID); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `SELECT `+formVersionJSON+` FROM form_versions v JOIN forms f ON f.id = v.form_id WHERE v.id = $1`, id).Scan(&result)
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
const workspaceTemplate = `SELECT t.id FROM workflow_templates t WHERE t.workspace_id IS NULL OR t.workspace_id = $1
ORDER BY t.workspace_id NULLS LAST LIMIT 1`

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
		key, _, err := formRoute(r)
		if err != nil {
			return err
		}
		if !stepSlug.MatchString(step) {
			return problem(http.StatusNotFound, "Workflow step not found.")
		}
		var formID, templateID int64
		var stepFound bool
		err = tx.QueryRow(ctx, `
SELECT f.id, t.id, EXISTS (SELECT FROM workflow_steps s WHERE s.template_id = t.id AND s.slug = $3)
FROM forms f CROSS JOIN LATERAL (
    SELECT t.id FROM workflow_templates t WHERE t.workspace_id IS NULL OR t.workspace_id = f.workspace_id
    ORDER BY t.workspace_id NULLS LAST LIMIT 1) t
WHERE f.workspace_id = $1 AND f.form_key = $2 FOR UPDATE OF f`, access.workspaceID, key, step).Scan(&formID, &templateID, &stepFound)
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
