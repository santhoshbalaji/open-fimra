package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func validOperationsUnit(raw string) bool {
	id, err := strconv.ParseInt(raw, 10, 64)
	return err == nil && id > 0
}

// operationsOptions supplies the form versions that can be selected in a
// unit and the owners an officer can assign there.
func (a api) operationsOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	q := r.URL.Query()
	unit, search, record := q.Get("unit"), strings.TrimSpace(q.Get("search")), q.Get("record")
	if (unit != "" && !validOperationsUnit(unit)) || (record != "" && !uuidPattern.MatchString(record)) || len(search) > 120 {
		writeError(w, http.StatusBadRequest, "Invalid unit, record or search.")
		return
	}
	var result json.RawMessage
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, slug)
		if err != nil {
			return err
		}
		// With no selection, search only current units the caller can use.
		if unit == "" && record == "" {
			return tx.QueryRow(ctx, `SELECT jsonb_build_object('units', COALESCE(jsonb_agg(to_jsonb(u)), '[]'))
FROM (SELECT u.id::text, u.name, u.path::text, h.slug AS hierarchy
      FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
      WHERE h.workspace_id = $2 AND u.valid_to IS NULL
        AND EXISTS (SELECT FROM operations_scope($1, $2, h.id, u.path))
        AND (strpos(lower(u.name), lower($3)) > 0 OR strpos(lower(u.path::text), lower($3)) > 0 OR $3 = '')
      ORDER BY u.depth, u.path, h.slug LIMIT 50) u`, subject, access.workspaceID, search).Scan(&result)
		}
		return tx.QueryRow(ctx, `WITH selected AS (
    SELECT u.hierarchy_id, u.path, NULL::text AS kind, g.officer
    FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
    CROSS JOIN LATERAL operations_scope($1, $3, h.id, u.path) g
    WHERE h.workspace_id = $3 AND u.id = NULLIF($4, '')::bigint AND u.valid_to IS NULL AND $5 = ''
    UNION ALL
    SELECT r.hierarchy_id, r.unit_path, r.kind, g.officer
    FROM operations_records r CROSS JOIN LATERAL operations_roles($1, $2, r.id) g
    WHERE r.id = NULLIF($5, '')::uuid AND g.full_access
)
SELECT jsonb_build_object('forms', (SELECT COALESCE(jsonb_agg(to_jsonb(v) ORDER BY v.form), '[]') FROM (
    SELECT o.kind, o.form_key AS form, o.completion_only AS "completionOnly", p.version, p.definition->'title' AS title
    FROM operations_forms o JOIN forms f ON f.form_key = o.form_key
    CROSS JOIN LATERAL (SELECT v.version, v.definition FROM form_versions v
        WHERE v.form_id = f.id AND v.published_at IS NOT NULL ORDER BY v.version DESC LIMIT 1) p
    WHERE f.workspace_id = $3 AND (s.kind IS NULL OR o.kind = s.kind)
      AND (f.level <> 'division' OR (f.owner_hierarchy_id = s.hierarchy_id AND s.path <@ f.owner_unit_path))
      AND (o.kind NOT IN ('offence', 'relief') OR EXISTS (
          SELECT FROM referral_grants g WHERE g.workspace_id = $3 AND g.user_id = $6 AND g.kind = o.kind
            AND g.hierarchy_id = s.hierarchy_id AND g.unit_path @> s.path))
) v), 'owners', (SELECT COALESCE(jsonb_agg(to_jsonb(u) ORDER BY u.name, u.id), '[]') FROM (
    SELECT DISTINCT g.user_id AS id, owner.display_name AS name
    FROM permanent_alert_grants g JOIN users owner ON owner.id = g.user_id
    WHERE g.workspace_id = $3 AND (g.user_id = $6 OR s.officer) AND s.kind IS NULL
      AND ((g.depth = 0 AND g.role_slug = 'state_admin') OR (g.hierarchy_id = s.hierarchy_id AND g.unit_path @> s.path))
) u))
FROM selected s`, subject, slug, access.workspaceID, unit, record, access.userID).Scan(&result)
	})
	if err != nil {
		a.writeProblem(w, err, "Unit or record not found.", "Could not load record options")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a api) referralGrant(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	var in struct {
		Kind string `json:"kind"`
		Unit string `json:"unit"`
		User string `json:"user"`
	}
	if !decodeOperations(w, r, &in, 16<<10) || !isReferral(in.Kind) ||
		!validOperationsUnit(in.Unit) || !uuidPattern.MatchString(in.User) {
		writeError(w, http.StatusBadRequest, "Provide a referral kind, a unit ID and a user ID.")
		return
	}
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, slug)
		if err != nil {
			return err
		}
		if err := requireFormAdmin(access); err != nil {
			return err
		}
		var hierarchy, path string
		err = tx.QueryRow(ctx, `SELECT u.hierarchy_id::text, u.path::text
FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
WHERE h.workspace_id = $1 AND u.id = $2::text::bigint AND u.valid_to IS NULL`, access.workspaceID, in.Unit).Scan(&hierarchy, &path)
		if err != nil {
			return err
		}
		action := "referral.grant"
		statement := `INSERT INTO referral_grants (workspace_id, hierarchy_id, unit_path, user_id, kind)
VALUES ($1, $2, $3::ltree, $4, $5) ON CONFLICT DO NOTHING`
		if r.Method == http.MethodDelete {
			action = "referral.revoke"
			statement = `DELETE FROM referral_grants WHERE (workspace_id, hierarchy_id, unit_path, user_id, kind) = ($1, $2, $3::ltree, $4, $5)`
		} else {
			var allowed bool
			err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM users u
CROSS JOIN LATERAL operations_scope(u.oidc_subject, $1, $2, $3::ltree) g WHERE u.id = $4)`,
				access.workspaceID, hierarchy, path, in.User).Scan(&allowed)
			if err != nil {
				return err
			}
			if !allowed {
				return problem(http.StatusUnprocessableEntity, "The user needs a role covering this unit.")
			}
		}
		tag, err := tx.Exec(ctx, statement, access.workspaceID, hierarchy, path, in.User, in.Kind)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_log(workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
VALUES ($1, 'user', $2, $3, 'user', $4, jsonb_build_object('kind', $5::text, 'hierarchy', $6::uuid, 'unit', $7::text))`,
			access.workspaceID, subject, action, in.User, in.Kind, hierarchy, path)
		return err
	})
	if err != nil {
		a.writeProblem(w, err, "Unit not found.", "Could not update the referral grant")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a api) operationsBurnAlert(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	record := chi.URLParam(r, "record")
	alert, err := strconv.ParseInt(chi.URLParam(r, "alert"), 10, 64)
	if err != nil || alert < 1 {
		writeError(w, http.StatusBadRequest, "Invalid alert ID.")
		return
	}
	err = pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := fullOperationsAccess(ctx, tx, subject, slug, record)
		if err != nil {
			return err
		}
		if access.kind != "burn" || !access.officer {
			return problem(http.StatusForbidden, "A supervising officer can link a prescribed burn to an alert.")
		}
		if err := lockOperationsRecord(ctx, tx, record); err != nil {
			return err
		}
		return linkBurnAlert(ctx, tx, subject, slug, access.workspace, record, alert)
	})
	if err != nil {
		a.writeProblem(w, err, "Burn or alert not found.", "Could not link the alert")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func linkBurnAlert(ctx context.Context, tx pgx.Tx, subject, slug, workspace, record string, alert int64) error {
	var visible, approved bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
    SELECT FROM alert_roles($1, $2, $3) g JOIN alerts a ON a.id = $3
    WHERE g.officer AND a.workspace_id = r.workspace_id
      AND (match_under_grant(a.hierarchy_unit_paths, r.hierarchy_id, r.unit_path, (nlevel(r.unit_path)-1)::smallint)
           OR (a.hierarchy_id = r.hierarchy_id AND a.unit_path <@ r.unit_path))
), EXISTS (SELECT FROM submissions s JOIN approvals ap ON ap.submission_id = s.id
    WHERE s.operations_record_id = r.id AND NOT s.completes_record AND ap.decision = 'approved')
FROM operations_records r WHERE r.id = $4 AND r.kind = 'burn'`, subject, slug, alert, record).Scan(&visible, &approved)
	if err != nil {
		return err
	}
	if !visible {
		return pgx.ErrNoRows
	}
	if !approved {
		return problem(http.StatusConflict, "Approve the burn authorisation before linking an alert.")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO prescribed_burn_alerts (workspace_id, alert_id, record_id) VALUES ($1, $2, $3)
ON CONFLICT (alert_id) DO NOTHING`, workspace, alert, record)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var same bool
		if err := tx.QueryRow(ctx, `SELECT record_id = $2 FROM prescribed_burn_alerts WHERE alert_id = $1`, alert, record).Scan(&same); err != nil {
			return err
		}
		if !same {
			return problem(http.StatusConflict, "This alert already belongs to another prescribed burn.")
		}
		return nil
	}
	return auditOperations(ctx, tx, subject, record, "operations.burn.link", &alert, nil)
}
