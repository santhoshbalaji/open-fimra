package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type intakeRequest struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observedAt"`
	Longitude  *float64  `json:"longitude"`
	Latitude   *float64  `json:"latitude"`
	Note       string    `json:"note"`
	IncidentID string    `json:"incidentId"`
}

const incidentGroupingLock = `SELECT pg_advisory_xact_lock(hashtextextended('open-fimra:incidents:' || $1, 0))`

func (a api) fireIntake(w http.ResponseWriter, r *http.Request) {
	var in intakeRequest
	if !decodeNotificationInput(w, r, &in) {
		writeError(w, 400, "Provide valid report details.")
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if !uuidPattern.MatchString(in.ID) || !slices.Contains([]string{"phone", "radio", "staff", "public"}, in.Source) ||
		in.ObservedAt.IsZero() || in.ObservedAt.After(time.Now().Add(clockSkew)) || len([]rune(in.Note)) > 2000 ||
		in.Longitude == nil || in.Latitude == nil || *in.Longitude < -180 || *in.Longitude > 180 ||
		*in.Latitude < -90 || *in.Latitude > 90 {
		writeError(w, 400, "Provide a report ID, source, observation time and valid coordinates.")
		return
	}
	in.ObservedAt = in.ObservedAt.UTC()
	var target *int64
	if in.IncidentID != "" {
		id, err := strconv.ParseInt(in.IncidentID, 10, 64)
		if err != nil || id < 1 {
			writeError(w, 400, "Invalid incident ID.")
			return
		}
		target = &id
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	status := http.StatusOK
	var result json.RawMessage
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, slug)
		if err != nil {
			return err
		}
		// Use the same lock order as satellite ingestion.
		if _, err := tx.Exec(ctx, detectionWriteLock); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, incidentGroupingLock, access.workspaceID); err != nil {
			return err
		}
		body, _ := json.Marshal(in)
		sum := sha256.Sum256(body)
		hash := hex.EncodeToString(sum[:])
		var alertID int64
		var oldUser, oldHash string
		err = tx.QueryRow(ctx, `SELECT alert_id,user_id::text,request_hash FROM fire_intake
WHERE workspace_id=$1 AND request_id=$2`, access.workspaceID, in.ID).Scan(&alertID, &oldUser, &oldHash)
		switch {
		case err == nil:
			if oldUser != access.userID || oldHash != hash {
				return problem(409, "This report ID already belongs to another request.")
			}
		case errors.Is(err, pgx.ErrNoRows):
			alertID, err = saveIntake(ctx, tx, access, subject, slug, in, target, hash)
			if err != nil {
				return err
			}
			status = http.StatusCreated
		default:
			return err
		}
		return tx.QueryRow(ctx, `SELECT jsonb_build_object('alertId',f.alert_id::text,'incidentId',m.incident_id::text,
 'source',f.source,'observedAt',f.observed_at,'receivedAt',f.received_at)
FROM fire_intake f JOIN incident_members m USING (alert_id)
WHERE f.alert_id=$3 AND EXISTS (SELECT FROM alert_roles($1,$2,$3))`, subject, slug, alertID).Scan(&result)
	})
	if err != nil {
		a.writeProblem(w, err, "Report or incident not found.", "Could not save the fire report")
		return
	}
	writeJSON(w, status, result)
}

func saveIntake(ctx context.Context, tx pgx.Tx, access workspaceAccess, subject, slug string, in intakeRequest, target *int64, hash string) (int64, error) {
	if target != nil {
		var active bool
		err := tx.QueryRow(ctx, `SELECT i.merged_into IS NULL AND NOT s.terminal
FROM incidents i JOIN workflow_instances wi ON wi.incident_id=i.id
JOIN workflow_steps s ON (s.template_id,s.slug)=(wi.template_id,wi.current_step)
WHERE i.id=$3 AND i.workspace_id=$4 AND EXISTS (
    SELECT FROM incident_alert_roles($1,$2,$3) WHERE officer)
FOR UPDATE OF wi`, subject, slug, target, access.workspaceID).Scan(&active)
		if err != nil {
			return 0, err
		}
		if !active {
			return 0, problem(409, "Choose an open incident.")
		}
	}
	if _, err := tx.Exec(ctx, "SELECT ensure_detection_partition($1::date)", in.ObservedAt); err != nil {
		return 0, err
	}
	location, _ := json.Marshal(map[string]any{"type": "Point", "coordinates": []float64{*in.Longitude, *in.Latitude}})
	raw, _ := json.Marshal(map[string]string{"reportId": in.ID, "reportSource": in.Source})
	var inWorkspace bool
	var class *string
	var alert *int64
	err := tx.QueryRow(ctx, manualReportSQL, access.workspaceID, manualSource, manualProduct,
		"intake:"+access.workspaceID+":"+in.ID, in.ObservedAt, string(location), raw, true, target).Scan(&inWorkspace, &class, &alert)
	if err != nil {
		return 0, err
	}
	if !inWorkspace || alert == nil {
		return 0, problem(422, "This place is outside the workspace's area. Check the location.")
	}
	var visible bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM alert_roles($1,$2,$3)
WHERE officer OR (role_slug='field_staff' AND $4='staff')
              OR (role_slug='community_caller' AND $4='public'))`, subject, slug, *alert, in.Source).Scan(&visible); err != nil {
		return 0, err
	}
	if !visible {
		return 0, problem(403, "Your role cannot record this report for this location.")
	}
	_, err = tx.Exec(ctx, `
INSERT INTO fire_intake(workspace_id,request_id,alert_id,user_id,source,observed_at,note,request_hash)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, access.workspaceID, in.ID, *alert, access.userID, in.Source, in.ObservedAt, in.Note, hash)
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,details)
VALUES ($1,'user',$2,'alert.intake','alert',$3,jsonb_build_object('source',$4::text,'reportId',$5::text))`,
		access.workspaceID, subject, strconv.FormatInt(*alert, 10), in.Source, in.ID)
	return *alert, err
}
