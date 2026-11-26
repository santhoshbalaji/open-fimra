package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type membershipRequest struct {
	Operation  string   `json:"operation"`
	TargetID   string   `json:"targetId"`
	AlertIDs   []string `json:"alertIds"`
	Reason     string   `json:"reason"`
	PreviewKey string   `json:"previewKey"`
}

type membershipIncident struct {
	ID       string   `json:"id"`
	Step     string   `json:"step"`
	AlertIDs []string `json:"alertIds"`
}

type membershipImpact struct {
	Source     membershipIncident  `json:"source"`
	Target     *membershipIncident `json:"target,omitempty"`
	Moving     []string            `json:"moving"`
	PreviewKey string              `json:"previewKey"`
}

func (a api) incidentMembership(w http.ResponseWriter, r *http.Request) {
	var in membershipRequest
	if !decodeNotificationInput(w, r, &in) {
		writeError(w, 400, "Provide valid merge or split details.")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "Invalid incident ID.")
		return
	}
	preview := r.URL.Query().Get("preview") == "true"
	in.Reason = strings.TrimSpace(in.Reason)
	if !preview && (in.Reason == "" || len([]rune(in.Reason)) > 2000) {
		writeError(w, 400, "Give a reason for the merge or split.")
		return
	}
	var target int64
	switch in.Operation {
	case "merge":
		target, err = strconv.ParseInt(in.TargetID, 10, 64)
		if err != nil || target < 1 || target == id || len(in.AlertIDs) > 0 {
			writeError(w, 400, "Choose another incident to receive these alerts.")
			return
		}
	case "split":
		if in.TargetID != "" || len(in.AlertIDs) < 1 || len(in.AlertIDs) > 500 {
			writeError(w, 400, "Choose up to 500 alerts to move to a new incident.")
			return
		}
		for _, raw := range in.AlertIDs {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n < 1 || strconv.FormatInt(n, 10) != raw {
				writeError(w, 400, "Invalid alert ID.")
				return
			}
		}
		slices.Sort(in.AlertIDs)
		in.AlertIDs = slices.Compact(in.AlertIDs)
	default:
		writeError(w, 400, "Choose merge or split.")
		return
	}

	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	var result any
	err = pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, slug)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, incidentGroupingLock, access.workspaceID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT id FROM workflow_instances
WHERE incident_id IN ($1,$2) AND incident_id IN (SELECT id FROM incidents WHERE workspace_id=$3)
ORDER BY incident_id FOR UPDATE`, id, target, access.workspaceID); err != nil {
			return err
		}
		impact, err := readMembershipImpact(ctx, tx, access.workspaceID, subject, slug, id, target, in.AlertIDs)
		if err != nil {
			return err
		}
		if preview {
			result = impact
			return nil
		}
		if in.PreviewKey != impact.PreviewKey {
			return problem(409, "The incidents have changed. Preview the impact again.")
		}
		result, err = saveMembershipChange(ctx, tx, access, subject, id, target, in, impact)
		return err
	})
	if err != nil {
		a.writeProblem(w, err, "Incident not found.", "Could not change incident membership")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func readMembershipImpact(ctx context.Context, tx pgx.Tx, workspace, subject, slug string, id, target int64, selected []string) (membershipImpact, error) {
	read := func(caseID int64) (membershipIncident, error) {
		incident := membershipIncident{}
		var active, canManage bool
		err := tx.QueryRow(ctx, `WITH members AS (
    SELECT m.alert_id, EXISTS (SELECT FROM alert_roles($1,$2,m.alert_id) WHERE officer) AS managed
    FROM incident_members m WHERE m.incident_id=$3
)
SELECT i.id::text,s.label,i.merged_into IS NULL AND (NOT s.terminal OR $3<>$5),
    COALESCE((SELECT array_agg(alert_id::text ORDER BY alert_id) FROM members),'{}'),
    COALESCE((SELECT bool_and(managed) FROM members),false)
FROM incidents i JOIN workflow_instances wi ON wi.incident_id=i.id
JOIN workflow_steps s ON (s.template_id,s.slug)=(wi.template_id,wi.current_step)
WHERE i.id=$3 AND i.workspace_id=$4 AND EXISTS (SELECT FROM incident_alert_roles($1,$2,$3))`,
			subject, slug, caseID, workspace, target).Scan(&incident.ID, &incident.Step, &active, &incident.AlertIDs, &canManage)
		if err != nil {
			return incident, err
		}
		if !active {
			return incident, problem(409, "Choose an open destination and a source that has not been merged.")
		}
		if !canManage {
			return incident, problem(403, "A supervising officer role on every member alert is required.")
		}
		return incident, nil
	}

	source, err := read(id)
	if err != nil {
		return membershipImpact{}, err
	}
	impact := membershipImpact{Source: source, Moving: source.AlertIDs}
	if target != 0 {
		destination, err := read(target)
		if err != nil {
			return impact, err
		}
		impact.Target = &destination
	} else {
		if len(selected) >= len(source.AlertIDs) {
			return impact, problem(422, "Keep at least one alert on the original incident.")
		}
		for _, alert := range selected {
			if !slices.Contains(source.AlertIDs, alert) {
				return impact, problem(422, "Choose alerts belonging to the source incident.")
			}
		}
		impact.Moving = selected
	}
	body, _ := json.Marshal(impact)
	sum := sha256.Sum256(body)
	impact.PreviewKey = hex.EncodeToString(sum[:])
	return impact, nil
}

func saveMembershipChange(ctx context.Context, tx pgx.Tx, access workspaceAccess, subject string, id, target int64, in membershipRequest, impact membershipImpact) (json.RawMessage, error) {
	if target == 0 {
		// A split starts a new workflow on the source's version so the new
		// case and its member dispositions can still use the same forms.
		if err := tx.QueryRow(ctx, `WITH opened AS (
    INSERT INTO incidents(workspace_id,first_observed_at,last_observed_at)
    SELECT $1,min(observed_at),max(observed_at) FROM alerts WHERE id=ANY($2::text[]::bigint[])
    RETURNING id,created_at
), workflow AS (
    INSERT INTO workflow_instances(incident_id,template_id,current_step,entered_at)
    SELECT o.id,wi.template_id,'new',o.created_at FROM opened o
    JOIN workflow_instances wi ON wi.incident_id=$3
    RETURNING *
)
INSERT INTO step_log(incident_id,template_id,to_step,occurred_at)
SELECT incident_id,template_id,current_step,entered_at FROM workflow RETURNING incident_id`,
			access.workspaceID, impact.Moving, id).Scan(&target); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE incident_members SET incident_id=$1 WHERE alert_id=ANY($2::text[]::bigint[])`, target, impact.Moving); err != nil {
		return nil, err
	}
	if in.Operation == "merge" {
		if _, err := tx.Exec(ctx, `UPDATE incidents SET merged_into=$1,merged_at=COALESCE(merged_at,clock_timestamp())
WHERE id=$2 OR merged_into=$2`, target, id); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE incidents i SET first_observed_at=b.first_seen,last_observed_at=b.last_seen
FROM (SELECT m.incident_id,min(a.observed_at) AS first_seen,max(a.observed_at) AS last_seen
      FROM incident_members m JOIN alerts a ON a.id=m.alert_id WHERE m.incident_id IN ($1,$2) GROUP BY m.incident_id) b
WHERE i.id=b.incident_id`, id, target); err != nil {
		return nil, err
	}
	var receipt json.RawMessage
	err := tx.QueryRow(ctx, `WITH saved AS (
    INSERT INTO incident_membership_changes(workspace_id,operation,from_incident_id,to_incident_id,alert_ids,user_id,reason)
    VALUES ($1,$2,$3,$4,$5::text[]::bigint[],$6,$7)
    RETURNING *
), audited AS (
    INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,details)
    SELECT workspace_id,'user',$8,'incident.'||operation,'incident',from_incident_id::text,
        jsonb_build_object('membershipChangeId',id,'targetId',to_incident_id,'alertIds',alert_ids,'reason',reason)
    FROM saved
)
SELECT jsonb_build_object('id',id::text,'operation',operation,'sourceId',from_incident_id::text,
    'targetId',to_incident_id::text,'moving',$5::text[],'reason',reason,'at',changed_at) FROM saved`,
		access.workspaceID, in.Operation, id, target, impact.Moving, access.userID, in.Reason, subject).Scan(&receipt)
	return receipt, err
}
