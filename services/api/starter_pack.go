package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	formschema "github.com/santhoshbalaji/open-fimra/packages/form-schema"
	starterpack "github.com/santhoshbalaji/open-fimra/packages/starter-pack"
)

// The starter pack (PLAN.md sections 5.4 and 7) is a ready set of forms for
// the default workflow. Turning it on adds each starter form the workspace
// does not have yet, publishes it and puts it on its workflow steps, all in
// one transaction. A form the workspace already has, with the same key, is
// left exactly as it is, whoever made it. So turning the pack on again is
// safe, and never undoes a department's changes or assignments.

type packSkip struct {
	Step   string `json:"step"`
	Reason string `json:"reason"`
}

type packForm struct {
	Form    string   `json:"form"`
	Title   string   `json:"title"`
	Version int      `json:"version"`
	Steps   []string `json:"steps"`
	// Status is added (by this request), present (in the workspace already)
	// or missing.
	Status string `json:"status"`
	// Assigned lists the steps the workspace's form is on now.
	Assigned []string `json:"assigned"`
	// Skipped lists the pack's steps that this request left alone.
	Skipped []packSkip `json:"skipped,omitempty"`
}

type packState struct {
	// Enabled is true when every starter form is in the workspace.
	Enabled bool       `json:"enabled"`
	Forms   []packForm `json:"forms"`
}

// starterPack shows the pack and, on POST, turns it on.
func (a api) starterPack(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	var state packState
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, chi.URLParam(r, "slug"))
		if err == nil {
			err = requireFormAdmin(access)
		}
		if err != nil {
			return err
		}
		added := map[string][]packSkip{}
		if r.Method == http.MethodPost {
			if added, err = enableStarterPack(ctx, tx, access, subject); err != nil {
				return err
			}
		}
		state, err = readPackState(ctx, tx, access.workspaceID, added)
		return err
	})
	if err != nil {
		a.writeProblem(w, err, "Workspace not found.", "Could not turn on the starter forms")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// enableStarterPack adds the starter forms the workspace lacks. It returns
// the forms it added, each with the steps it left alone.
func enableStarterPack(ctx context.Context, tx pgx.Tx, access workspaceAccess, subject string) (map[string][]packSkip, error) {
	// Two requests at once must not both add the same form.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('open-fimra:starter-pack:' || $1, 0))`, access.workspaceID); err != nil {
		return nil, err
	}
	var templateID int64
	if err := tx.QueryRow(ctx, workspaceTemplate, access.workspaceID).Scan(&templateID); err != nil {
		return nil, err
	}
	added := map[string][]packSkip{}
	for _, form := range starterpack.Forms() {
		var present bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM forms WHERE workspace_id = $1 AND form_key = $2)`,
			access.workspaceID, form.Key).Scan(&present); err != nil {
			return nil, err
		}
		if present {
			continue
		}
		columns, problems := formschema.Columns(form.Source)
		if len(problems) > 0 {
			return nil, problem(http.StatusInternalServerError, "The starter form "+form.Key+" is not valid.", problems...)
		}
		tree, err := parseFormTree(form.Source)
		if err != nil {
			return nil, err
		}
		upload := &formUpload{source: form.Source, columns: columns, tree: tree, key: form.Key, version: form.Version, level: "state"}
		var created json.RawMessage
		if err := createFormVersion(ctx, tx, subject, access, upload, &created); err != nil {
			return nil, err
		}
		var formID, versionID int64
		if err := tx.QueryRow(ctx, `SELECT f.id, v.id FROM forms f JOIN form_versions v ON v.form_id = f.id
WHERE f.workspace_id = $1 AND f.form_key = $2 AND v.version = $3`, access.workspaceID, form.Key, form.Version).Scan(&formID, &versionID); err != nil {
			return nil, err
		}
		if err := publishFormVersion(ctx, tx, versionID, string(form.Source), access.userID); err != nil {
			return nil, err
		}
		skipped := []packSkip{}
		for _, step := range form.Steps {
			var known, taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM workflow_steps WHERE template_id = $2 AND slug = $3),
    EXISTS (SELECT FROM form_assignments fa JOIN forms f ON f.id = fa.form_id
            WHERE f.workspace_id = $1 AND fa.template_id = $2 AND fa.step = $3)`,
				access.workspaceID, templateID, step).Scan(&known, &taken); err != nil {
				return nil, err
			}
			switch {
			case !known:
				skipped = append(skipped, packSkip{step, "The workspace's workflow has no step " + step + "."})
			case taken:
				// Adding a second form would change what the step asks for.
				skipped = append(skipped, packSkip{step, "Another form is already filled in at this step."})
			default:
				if err := saveAssignment(ctx, tx, access, subject, formStep{formID: formID, versionID: versionID,
					templateID: templateID, key: form.Key, step: step, version: form.Version}); err != nil {
					return nil, err
				}
			}
		}
		added[form.Key] = skipped
	}
	keys := make([]string, 0, len(added))
	for _, form := range starterpack.Forms() {
		if _, ok := added[form.Key]; ok {
			keys = append(keys, form.Key)
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
VALUES ($1, 'user', $2, 'starter_pack.enable', 'workspace', $3, jsonb_build_object('added', $4::text[]))`,
		access.workspaceID, subject, access.workspaceID, keys)
	return added, err
}

// readPackState describes each starter form in the workspace. added holds
// the forms this request added, with the steps it skipped.
func readPackState(ctx context.Context, tx pgx.Tx, workspaceID string, added map[string][]packSkip) (packState, error) {
	state := packState{Enabled: true, Forms: []packForm{}}
	for _, form := range starterpack.Forms() {
		item := packForm{Form: form.Key, Title: form.Title, Version: form.Version, Steps: form.Steps, Status: "missing"}
		var present bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM forms WHERE workspace_id = $1 AND form_key = $2),
    COALESCE((SELECT array_agg(fa.step ORDER BY fa.step) FROM forms f JOIN form_assignments fa ON fa.form_id = f.id
              JOIN workflow_templates t ON t.id = fa.template_id AND (t.workspace_id IS NULL OR t.workspace_id = f.workspace_id)
              WHERE f.workspace_id = $1 AND f.form_key = $2), '{}')`, workspaceID, form.Key).Scan(&present, &item.Assigned); err != nil {
			return state, err
		}
		if skipped, ok := added[form.Key]; ok {
			item.Status, item.Skipped = "added", skipped
		} else if present {
			item.Status = "present"
		}
		state.Enabled = state.Enabled && present
		state.Forms = append(state.Forms, item)
	}
	return state, nil
}

// starterPackEnabled says whether every starter form is in the workspace.
func starterPackEnabled(ctx context.Context, db rowQuerier, workspaceID string) (bool, error) {
	keys := make([]string, 0)
	for _, form := range starterpack.Forms() {
		keys = append(keys, form.Key)
	}
	var missing int
	err := db.QueryRow(ctx, `SELECT count(*) FROM unnest($2::text[]) k(key)
WHERE NOT EXISTS (SELECT FROM forms f WHERE f.workspace_id = $1 AND f.form_key = k.key)`, workspaceID, keys).Scan(&missing)
	return missing == 0, err
}
