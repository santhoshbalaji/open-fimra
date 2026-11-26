package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

//go:embed incidents.sql
var incidentsSQL string

//go:embed incidents_step_outcomes.sql
var incidentMemberOutcomesSQL string

type incidentPage struct {
	Items      []json.RawMessage `json:"items"`
	Total      int               `json:"total"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

// incidents lists the incidents the caller may see, or reads one of them.
// It takes the alert queue's filters. Queue, class and unit path keep an
// incident when any visible member matches. Status is the incident's step.
func (a api) incidents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	f, err := readAlertFilters(r.URL.Query())
	if err != nil {
		writeError(w, 400, strings.Replace(err.Error(), "alert cursor", "incident cursor", 1))
		return
	}
	sortOrder := r.URL.Query().Get("sort")
	if sortOrder != "" && sortOrder != "activity-desc" && sortOrder != "activity-asc" {
		writeError(w, 400, "Unknown incident sort order.")
		return
	}
	var id *int64
	if raw := chi.URLParam(r, "id"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			writeError(w, 400, "Invalid incident ID.")
			return
		}
		id = &n
	}
	caller := r.Context().Value(identityKey{}).(identity)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var allowed, knownStatus bool
	var page incidentPage
	err = a.reader.QueryRow(ctx, incidentsSQL, caller.Subject, chi.URLParam(r, "slug"), id, f.hours,
		f.queue, f.status, f.class, f.path, f.before, f.beforeID, f.limit+1, sortOrder).Scan(&allowed, &knownStatus, &page.Total, &page.Items)
	if err != nil {
		a.logger.Error("Could not read incidents", "error", err)
		writeError(w, 500, "Could not load incidents.")
		return
	}
	if !allowed {
		writeError(w, 403, "A role in this workspace is required.")
		return
	}
	if !knownStatus {
		writeError(w, 400, "Unknown workflow step.")
		return
	}
	if id != nil {
		if len(page.Items) == 0 {
			writeError(w, 404, "Incident not found.")
			return
		}
		writeJSON(w, 200, page.Items[0])
		return
	}
	if len(page.Items) > f.limit {
		page.Items = page.Items[:f.limit]
		var last struct {
			LastObserved string `json:"last_observed_time"`
			Activity     string `json:"activity_at"`
			ID           string `json:"id"`
		}
		if err := json.Unmarshal(page.Items[f.limit-1], &last); err != nil {
			a.logger.Error("Could not read incident cursor", "error", err)
			writeError(w, 500, "Could not load incidents.")
			return
		}
		cursorTime := last.LastObserved
		if sortOrder != "" {
			cursorTime = last.Activity
		}
		page.NextCursor = cursorTime + "," + last.ID
	}
	writeJSON(w, 200, page)
}

// incidentWorkflow reads an incident's timeline, previews a step on request,
// or records a step and returns its effect on the visible members.
func (a api) incidentWorkflow(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "Invalid incident ID.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	var result json.RawMessage
	if r.Method == http.MethodGet {
		if r.URL.Query().Has("preview") {
			err = a.previewIncidentStep(ctx, subject, slug, id, r.URL.Query().Get("preview"), &result)
		} else {
			// The moved count includes only member alerts that the caller can see.
			err = a.reader.QueryRow(ctx, `WITH visible AS (SELECT DISTINCT alert_id FROM incident_alert_roles($1,$2,$3))
SELECT jsonb_build_object('moves',COALESCE((SELECT jsonb_agg(to_jsonb(m) ORDER BY terminal,label)
 FROM incident_moves($1,$2,$3) m),'[]'),
 'mergedInto',(SELECT merged_into::text FROM incidents WHERE id=$3),
 'entries',(
 SELECT jsonb_agg(jsonb_build_object('from',l.from_step,'step',l.to_step,'label',s.label,
  'at',l.occurred_at,'actor',u.display_name,'note',l.note,'reason',l.override_reason,
  'longitude',ST_X(l.location),'latitude',ST_Y(l.location),'reportPending',l.report_pending,
  'alertsMoved',(SELECT count(*) FROM step_log x WHERE x.incident_step_id=l.id
      AND x.alert_id IN (SELECT alert_id FROM visible))) ORDER BY l.id)
 FROM step_log l JOIN workflow_steps s ON (s.template_id,s.slug)=(l.template_id,l.to_step)
 LEFT JOIN users u ON u.id=l.user_id WHERE l.incident_id=$3))
WHERE EXISTS (SELECT FROM visible)`, subject, slug, id).Scan(&result)
		}
	} else {
		var input stepRequest
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || !stepSlug.MatchString(input.Step) || len(input.ClientID) > 128 || (input.ClientTime != nil && input.ClientID == "") {
			writeError(w, 400, "Provide a step and valid step details.")
			return
		}
		input.Reason = strings.TrimSpace(input.Reason)
		if p := input.Location; p != nil && (p.Latitude == nil || p.Longitude == nil || *p.Latitude < -90 || *p.Latitude > 90 || *p.Longitude < -180 || *p.Longitude > 180) {
			writeError(w, 400, "Provide a valid latitude and longitude.")
			return
		}
		err = pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
			return recordIncidentStep(ctx, tx, subject, slug, id, input, &result)
		})
	}
	var problem *stepError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, 404, "Incident not found.")
	case errors.As(err, &problem):
		writeError(w, problem.status, problem.message)
	case err != nil:
		a.logger.Error("Could not process incident workflow", "error", err)
		writeError(w, 500, "Could not load or save this step.")
	default:
		writeJSON(w, 200, result)
	}
}

func (a api) previewIncidentStep(ctx context.Context, subject, slug string, id int64, step string, result *json.RawMessage) error {
	if !stepSlug.MatchString(step) {
		return &stepError{400, "Provide a step to preview."}
	}
	var merged, available, allowed bool
	err := a.reader.QueryRow(ctx, `WITH `+incidentMemberOutcomesSQL+`
SELECT i.merged_into IS NOT NULL,
 EXISTS (SELECT FROM workflow_transitions WHERE template_id=wi.template_id AND from_step=wi.current_step AND to_step=$4),
 EXISTS (SELECT FROM incident_moves($1,$2,$3) WHERE step=$4),
 jsonb_build_object('alerts',o.alerts)
FROM incidents i JOIN workflow_instances wi ON wi.incident_id=i.id CROSS JOIN member_outcomes o
WHERE i.id=$3 AND EXISTS (SELECT FROM incident_alert_roles($1,$2,$3))`,
		subject, slug, id, step).Scan(&merged, &available, &allowed, result)
	if err != nil {
		return err
	}
	return checkIncidentMove(merged, available, allowed)
}

func checkIncidentMove(merged, available, allowed bool) error {
	switch {
	case merged:
		return &stepError{409, "This incident was merged into another incident. Record steps there."}
	case !available:
		return &stepError{409, "This move is unavailable from the current step."}
	case !allowed:
		return &stepError{403, "Your role cannot record this step."}
	}
	return nil
}

// The stored outcomes are the original decisions. Recheck visibility on
// replay, and use the alert logs for saves made before outcomes were stored.
func readIncidentReceipt(ctx context.Context, tx pgx.Tx, subject, slug string, id, logID int64, result *json.RawMessage) error {
	return tx.QueryRow(ctx, `WITH visible AS (
 SELECT DISTINCT alert_id FROM incident_alert_roles($1,$2,$3)
), outcomes AS (
 SELECT o.item FROM step_log l
 CROSS JOIN LATERAL jsonb_array_elements(l.incident_outcomes) o(item)
 JOIN visible v ON v.alert_id=(o.item->>'alertId')::bigint
 WHERE l.id=$4
)
SELECT jsonb_build_object('id',l.id::text,'step',l.to_step,'at',l.occurred_at,
 'alertsMoved',(SELECT count(*) FROM step_log x JOIN visible v USING (alert_id) WHERE x.incident_step_id=l.id),
 'alerts',(SELECT COALESCE(jsonb_agg(item ORDER BY (item->>'alertId')::bigint),'[]') FROM outcomes))
FROM step_log l WHERE l.id=$4`, subject, slug, id, logID).Scan(result)
}

func recordIncidentStep(ctx context.Context, tx pgx.Tx, subject, slug string, id int64, in stepRequest, result *json.RawMessage) error {
	// Serialize with alert grouping so a merge or a new member cannot change
	// the incident while its step is being recorded.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('open-fimra:incidents:' || w.id::text, 0))
 FROM workspaces w WHERE w.slug=$2 AND EXISTS (SELECT FROM incident_alert_roles($1,$2,$3))`, subject, slug, id); err != nil {
		return err
	}
	var current, user string
	var entered time.Time
	var template int64
	var merged bool
	// Lock the incident first, then its member alerts in ID order. An alert
	// step never locks its incident, so the two cannot wait on each other.
	err := tx.QueryRow(ctx, `SELECT wi.current_step,wi.entered_at,wi.template_id,i.merged_into IS NOT NULL,r.user_id::text
 FROM incidents i JOIN workflow_instances wi ON wi.incident_id=i.id CROSS JOIN incident_alert_roles($1,$2,$3) r
 WHERE i.id=$3 LIMIT 1 FOR UPDATE OF wi`, subject, slug, id).Scan(&current, &entered, &template, &merged, &user)
	if err != nil {
		return err
	}
	var lon, lat *float64
	if in.Location != nil {
		lon = in.Location.Longitude
		lat = in.Location.Latitude
	}
	var logID int64
	if in.ClientID != "" {
		var same bool
		err = tx.QueryRow(ctx, `SELECT user_id=$3 AND to_step=$4 AND note=$5 AND override_reason=$6
   AND client_time IS NOT DISTINCT FROM $7::timestamptz AND ST_X(location) IS NOT DISTINCT FROM $8::float8
   AND ST_Y(location) IS NOT DISTINCT FROM $9::float8,id
   FROM step_log WHERE incident_id=$1 AND client_id=$2`, id, in.ClientID, user, in.Step, in.Note, in.Reason, in.ClientTime, lon, lat).Scan(&same, &logID)
		if err == nil {
			if !same {
				return &stepError{409, "This client ID already belongs to another step request."}
			}
			return readIncidentReceipt(ctx, tx, subject, slug, id, logID, result)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	var available, allowed, required, override, near bool
	var now time.Time
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM workflow_transitions WHERE template_id=$4 AND from_step=$5 AND to_step=$6),
 m.step IS NOT NULL, COALESCE(m.location_required,false),COALESCE(m.can_override,false),
 EXISTS(SELECT FROM incident_alert_roles($1,$2,$3) v JOIN alerts a ON a.id=v.alert_id
   JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
   WHERE ST_DWithin(d.geom::geography, ST_SetSRID(ST_Point($7,$8),4326)::geography,w.workflow_radius_metres)),
 clock_timestamp()
 FROM incidents i JOIN workspaces w ON w.id=i.workspace_id
 LEFT JOIN incident_moves($1,$2,$3) m ON m.step=$6 WHERE i.id=$3`, subject, slug, id, template, current, in.Step, lon, lat).Scan(&available, &allowed, &required, &override, &near, &now)
	if err != nil {
		return err
	}
	if err = checkIncidentMove(merged, available, allowed); err != nil {
		return err
	}
	if in.Reason != "" && (!override || !required) {
		return &stepError{403, "Only a supervising officer may give a location exception for a field step."}
	}
	if required && !near && in.Reason == "" {
		return &stepError{422, "Record a location near one of the incident's detections."}
	}
	at := now
	if in.ClientTime != nil {
		at = *in.ClientTime
		if at.Before(entered) || at.After(now) {
			return &stepError{422, "The client time must fall between the current step and now."}
		}
	}
	if _, err = tx.Exec(ctx, `SELECT 1 FROM incident_members m JOIN workflow_instances wi ON wi.alert_id=m.alert_id
 WHERE m.incident_id=$1 ORDER BY wi.alert_id FOR UPDATE OF wi`, id); err != nil {
		return err
	}
	var outcomes json.RawMessage
	if err = tx.QueryRow(ctx, `WITH `+incidentMemberOutcomesSQL+`SELECT alerts FROM member_outcomes`,
		subject, slug, id, in.Step).Scan(&outcomes); err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `INSERT INTO step_log(incident_id,template_id,from_step,to_step,user_id,occurred_at,location,note,override_reason,client_id,client_time,report_pending,incident_outcomes)
 SELECT $1,$2,$3,$4,$5,$6,ST_SetSRID(ST_Point($7,$8),4326),$9,$10,NULLIF($11,''),$12,report_pending,$13
 FROM workflow_steps WHERE template_id=$2 AND slug=$4 RETURNING id`, id, template, current, in.Step, user, at, lon, lat, in.Note, in.Reason, in.ClientID, in.ClientTime, outcomes).Scan(&logID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE workflow_instances SET current_step=$2,entered_at=$3 WHERE incident_id=$1`, id, in.Step, at); err != nil {
		return err
	}
	// Use the saved decisions for the write. The incident's location check
	// stands for the members it moves.
	if _, err = tx.Exec(ctx, `WITH logged AS (
    INSERT INTO step_log(alert_id,template_id,from_step,to_step,user_id,occurred_at,location,note,override_reason,report_pending,incident_step_id)
    SELECT wi.alert_id,wi.template_id,wi.current_step,$2,$3,GREATEST($4::timestamptz,wi.entered_at),
           ST_SetSRID(ST_Point($5,$6),4326),$7,$8,s.report_pending,$9
    FROM jsonb_to_recordset($1::jsonb) AS o("alertId" bigint,reason text)
    JOIN workflow_instances wi ON wi.alert_id=o."alertId"
    JOIN workflow_steps s ON (s.template_id,s.slug)=(wi.template_id,$2)
    WHERE o.reason='transition_allowed'
    ORDER BY wi.alert_id
    RETURNING alert_id,occurred_at
)
UPDATE workflow_instances wi SET current_step=$2,entered_at=l.occurred_at FROM logged l WHERE wi.alert_id=l.alert_id`,
		outcomes, in.Step, user, at, lon, lat, in.Note, in.Reason, logID); err != nil {
		return err
	}
	return readIncidentReceipt(ctx, tx, subject, slug, id, logID, result)
}

type incidentRule struct {
	DistanceMetres *float64 `json:"distanceMetres"`
	WindowHours    *float64 `json:"windowHours"`
}

// incidentSettings reads or replaces the workspace's grouping rule. Only a
// state administrator at the workspace root may use it. Changes apply to
// alerts created afterwards.
func (a api) incidentSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	subject := r.Context().Value(identityKey{}).(identity).Subject
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var input incidentRule
	if r.Method == http.MethodPut {
		if !decodeNotificationInput(w, r, &input) || input.DistanceMetres == nil || input.WindowHours == nil ||
			*input.DistanceMetres < 100 || *input.DistanceMetres > 20000 ||
			*input.WindowHours < 1 || *input.WindowHours > 168 || *input.WindowHours != math.Trunc(*input.WindowHours) {
			writeError(w, 400, "Provide distanceMetres from 100 to 20000 and windowHours as a whole number from 1 to 168.")
			return
		}
	}
	var result json.RawMessage
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		var workspaceID string
		query := `SELECT w.id::text FROM workspaces w WHERE w.slug=$2 AND EXISTS (
SELECT FROM alert_grants g WHERE g.workspace_id=w.id AND g.oidc_subject=$1 AND g.role_slug='state_admin' AND g.depth=0)`
		if r.Method == http.MethodPut {
			query += " FOR UPDATE OF w"
		}
		if err := tx.QueryRow(ctx, query, subject, chi.URLParam(r, "slug")).Scan(&workspaceID); err != nil {
			return err
		}
		if r.Method == http.MethodPut {
			if _, err := tx.Exec(ctx, `UPDATE workspaces SET settings=jsonb_set(settings,'{incidents}',
 jsonb_build_object('distanceMetres',$2::float8,'windowHours',$3::integer)) WHERE id=$1`,
				workspaceID, *input.DistanceMetres, int(*input.WindowHours)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,details)
VALUES($1,'user',$2,'incident.settings_update','workspace',$1::uuid::text,
 jsonb_build_object('distanceMetres',$3::float8,'windowHours',$4::integer))`,
				workspaceID, subject, *input.DistanceMetres, int(*input.WindowHours)); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `SELECT jsonb_build_object('distanceMetres',r.distance_m,
 'windowHours',(extract(epoch FROM r.time_window)/3600)::integer,'defaults',jsonb_build_object('distanceMetres',d.distance_m,
 'windowHours',(extract(epoch FROM d.time_window)/3600)::integer))
FROM workspaces w CROSS JOIN LATERAL incident_rule(w.settings) r CROSS JOIN LATERAL incident_rule('{}') d
WHERE w.id=$1`, workspaceID).Scan(&result)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 403, "A workspace administrator role at the root is required.")
	} else if err != nil {
		a.logger.Error("Could not process incident settings", "error", err)
		writeError(w, 500, "Could not load or save the incident settings.")
	} else {
		writeJSON(w, 200, result)
	}
}
