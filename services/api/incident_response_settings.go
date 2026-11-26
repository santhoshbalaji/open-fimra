package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type responseTarget struct {
	From              string `json:"from"`
	Through           string `json:"through"`
	Step              string `json:"step"`
	TargetMinutes     int    `json:"targetMinutes"`
	EscalationMinutes *int   `json:"escalationMinutes"`
}

type responseSettings struct {
	Extended bool             `json:"extended"`
	Targets  []responseTarget `json:"targets"`
}

func validResponseTargets(targets []responseTarget) bool {
	for i, target := range targets {
		from, errFrom := time.Parse(time.DateOnly, target.From)
		through, errThrough := time.Parse(time.DateOnly, target.Through)
		if errFrom != nil || errThrough != nil || through.Before(from) ||
			!slices.Contains([]string{"new", "reopened", "acknowledged", "assigned", "team_sent"}, target.Step) ||
			target.TargetMinutes < 1 || target.TargetMinutes > 10080 ||
			(target.EscalationMinutes != nil && (*target.EscalationMinutes <= target.TargetMinutes || *target.EscalationMinutes > 10080)) {
			return false
		}
		for _, earlier := range targets[:i] {
			if earlier.Step == target.Step && target.From <= earlier.Through && earlier.From <= target.Through {
				return false
			}
		}
	}
	return true
}

func (a api) incidentResponseSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	var in responseSettings
	if r.Method == http.MethodPut && (!decodeNotificationInput(w, r, &in) ||
		in.Targets == nil || len(in.Targets) > 100 || !validResponseTargets(in.Targets)) {
		writeError(w, 400, "Provide up to 100 response targets with valid dates and minutes. Seasons for the same milestone cannot overlap.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	subject := r.Context().Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	var result json.RawMessage
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, slug)
		if err != nil {
			return err
		}
		if err := requireFormAdmin(access); err != nil {
			return err
		}
		if r.Method == http.MethodPut {
			if err := saveResponseSettings(ctx, tx, access, subject, in); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `
SELECT jsonb_build_object('extended',w.extended_incident_response,'targets',COALESCE((
    SELECT jsonb_agg(jsonb_build_object('step',step,'from',lower(season),'through',upper(season)-1,
        'targetMinutes',target_minutes,'escalationMinutes',escalation_minutes) ORDER BY lower(season),step)
    FROM incident_season_targets WHERE workspace_id=w.id
),'[]')) FROM workspaces w WHERE w.id=$1`, access.workspaceID).Scan(&result)
	})
	if err != nil {
		a.writeProblem(w, err, "Workspace not found.", "Could not load or save response settings")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func saveResponseSettings(ctx context.Context, tx pgx.Tx, access workspaceAccess, subject string, in responseSettings) error {
	extended, err := selectResponseWorkflow(ctx, tx, access, subject, in.Extended)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM incident_season_targets WHERE workspace_id=$1`, access.workspaceID); err != nil {
		return err
	}
	data, _ := json.Marshal(in.Targets)
	if _, err := tx.Exec(ctx, `
INSERT INTO incident_season_targets(workspace_id,season,step,target_minutes,escalation_minutes)
SELECT $1,daterange("from",through,'[]'),step,"targetMinutes","escalationMinutes"
FROM jsonb_to_recordset($2) AS t("from" date,through date,step text,"targetMinutes" integer,"escalationMinutes" integer)`,
		access.workspaceID, data); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,details)
VALUES ($1,'user',$2,'incident.response_settings','workspace',$1::uuid::text,
    jsonb_build_object('extended',$3::boolean,'targets',$4::jsonb))`,
		access.workspaceID, subject, extended, data)
	return err
}

func selectResponseWorkflow(ctx context.Context, tx pgx.Tx, access workspaceAccess, subject string, enable bool) (bool, error) {
	// Serialize with the starter pack so form assignments are copied before
	// the new version can be used by new alerts or incidents.
	if _, err := tx.Exec(ctx, workflowFormsLock, access.workspaceID); err != nil {
		return false, err
	}
	var extended bool
	if err := tx.QueryRow(ctx, `SELECT extended_incident_response FROM workspaces WHERE id=$1 FOR UPDATE`,
		access.workspaceID).Scan(&extended); err != nil {
		return false, err
	}
	if enable && !extended {
		packEnabled, err := starterFormsEnabled(ctx, tx, access.workspaceID, false)
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO form_assignments(form_id,form_version_id,template_id,step,hierarchy_id,unit_paths,assigned_by)
SELECT fa.form_id,fa.form_version_id,v2.id,fa.step,fa.hierarchy_id,fa.unit_paths,$2
FROM form_assignments fa JOIN forms f ON f.id=fa.form_id
JOIN workflow_templates v2 ON v2.workspace_id IS NULL AND v2.version=2
JOIN workflow_steps s ON s.template_id=v2.id AND s.slug=fa.step
WHERE f.workspace_id=$1 AND fa.template_id=workspace_workflow_template($1) ON CONFLICT DO NOTHING`, access.workspaceID, access.userID); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET extended_incident_response=true WHERE id=$1`, access.workspaceID); err != nil {
			return false, err
		}
		if packEnabled {
			if _, err := enableStarterPack(ctx, tx, access, subject); err != nil {
				return false, err
			}
		}
	}
	return extended || enable, nil
}
