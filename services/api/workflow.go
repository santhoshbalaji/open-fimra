package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type stepRequest struct {
	Step     string `json:"step"`
	Location *struct {
		Longitude *float64 `json:"longitude"`
		Latitude  *float64 `json:"latitude"`
	} `json:"location"`
	Note        string        `json:"note"`
	Reason      string        `json:"reason"`
	ClientID    string        `json:"clientId"`
	ClientTime  *time.Time    `json:"clientTime"`
	ActualAt    *time.Time    `json:"actualAt"`
	EstimatedAt *time.Time    `json:"estimatedAt"`
	Response    *responseNote `json:"response"`
}

type responseNote struct {
	Status          string     `json:"status"`
	EstimatedExtent string     `json:"estimatedExtent,omitempty"`
	Threats         string     `json:"threats,omitempty"`
	HelpNeeded      string     `json:"helpNeeded,omitempty"`
	NextContact     *time.Time `json:"nextContact,omitempty"`
}

func stepTimes(in stepRequest, extended bool, entered, now time.Time) (time.Time, *time.Time, error) {
	if !extended && (in.ActualAt != nil || in.EstimatedAt != nil || in.Response != nil) {
		return time.Time{}, nil, &stepError{422, "These details require the extended incident response workflow."}
	}
	if !extended {
		if in.ClientTime == nil {
			return now, nil, nil
		}
		if in.ClientTime.Before(entered) || in.ClientTime.After(now) {
			return time.Time{}, nil, &stepError{422, "The client time must fall between the current step and now."}
		}
		return *in.ClientTime, nil, nil
	}
	actual := in.ActualAt
	if actual == nil && (in.EstimatedAt == nil || in.Step == "team_sent") {
		actual = in.ClientTime
		if actual == nil {
			actual = &now
		}
	}
	event := actual
	if event == nil {
		event = in.EstimatedAt
	}
	validThrough := now.Add(clockSkew)
	if event.IsZero() || event.After(validThrough) ||
		(in.EstimatedAt != nil && (in.EstimatedAt.IsZero() || (in.Step != "team_sent" && in.EstimatedAt.After(validThrough)))) {
		return time.Time{}, nil, &stepError{422, "Provide a valid event time at or before now."}
	}
	if in.Step == "team_sent" && (in.EstimatedAt == nil || in.EstimatedAt.Before(*event)) {
		return time.Time{}, nil, &stepError{422, "Provide an estimated arrival at or after dispatch."}
	}
	// Keep the workflow clock ordered while retaining the time reported by the field team.
	at := *event
	if at.Before(entered) {
		at = entered
	}
	if at.After(now) {
		at = now
	}
	return at, actual, nil
}

func validateResponseStep(in stepRequest, required, override, near bool) error {
	if len([]rune(in.Note)) > 2000 || len([]rune(in.Reason)) > 2000 {
		return &stepError{422, "Give a note and reason of at most 2,000 characters each."}
	}
	switch {
	case in.Step == "reopened":
		if !override {
			return &stepError{403, "Only a supervising officer may reopen an incident or alert."}
		}
		if in.Reason == "" {
			return &stepError{422, "Give a reason for reopening."}
		}
	case in.Reason != "" && (!override || !required):
		return &stepError{403, "Only a supervising officer may give a location exception for a field step."}
	case required && !near && in.Reason == "":
		return &stepError{422, "Record a location near the detection."}
	}
	if in.Step == "assigned" && strings.TrimSpace(in.Note) == "" {
		return &stepError{422, "Name the team or officer being assigned."}
	}
	if in.Step == "flash_update" || in.Step == "response_update" {
		if in.Response == nil || strings.TrimSpace(in.Response.Status) == "" {
			return &stepError{422, "Give the fire status for this update."}
		}
		for _, value := range []string{in.Response.Status, in.Response.EstimatedExtent, in.Response.Threats, in.Response.HelpNeeded} {
			if len([]rune(value)) > 300 {
				return &stepError{422, "Give a short response update of at most 300 characters per field."}
			}
		}
		if in.Response.NextContact != nil && in.Response.NextContact.IsZero() {
			return &stepError{422, "Provide a valid time for the next contact."}
		}
	} else if in.Response != nil {
		return &stepError{422, "Record a structured note on a flash or response update."}
	}
	return nil
}

const historySQL = `
SELECT jsonb_build_object('moves',COALESCE((SELECT jsonb_agg(to_jsonb(m) || jsonb_build_object(
 'extended',workflow_template_extended((SELECT template_id FROM workflow_instances WHERE alert_id=$3)),
 'missingReports', ARRAY(SELECT f.form_key FROM alert_missing_forms($3,m.step) f)) ORDER BY terminal,label,step)
 FROM alert_moves($1,$2,$3) m),'[]'),
 'offlineMoves',COALESCE((
 SELECT jsonb_object_agg(from_step,moves) FROM (
  SELECT from_step,jsonb_agg(to_jsonb(m) - 'from_step' ORDER BY terminal,label,step) AS moves FROM (
   SELECT t.from_step,t.to_step AS step,s.label,t.location_required,bool_or(r.officer) AS can_override,s.terminal,workflow_template_extended(i.template_id) AS extended
   FROM workflow_instances i JOIN workflow_transitions t ON t.template_id=i.template_id
   JOIN workflow_steps s ON (s.template_id,s.slug)=(t.template_id,t.to_step)
   JOIN alert_roles($1,$2,$3) r ON r.role_slug=ANY(t.allowed_roles)
     AND (r.role_slug <> 'division_officer' OR r.officer)
   WHERE i.alert_id=$3 GROUP BY t.from_step,t.to_step,s.label,t.location_required,s.terminal,i.template_id
  ) m GROUP BY from_step
 ) by_step
 ),'{}'),'entries',(
 SELECT jsonb_agg(jsonb_build_object('from',l.from_step,'step',l.to_step,'label',s.label,
  'at',l.occurred_at,'actualAt',l.actual_at,'estimatedAt',l.estimated_at,'syncedAt',l.recorded_at,'response',l.response_note,'actor',u.display_name,'note',l.note,'reason',l.override_reason,
  'longitude',ST_X(l.location),'latitude',ST_Y(l.location),'reportPending',l.report_pending,
  'incidentId',(SELECT x.incident_id::text FROM step_log x WHERE x.id=l.incident_step_id)) ORDER BY l.id)
 FROM step_log l JOIN workflow_steps s ON (s.template_id,s.slug)=(l.template_id,l.to_step)
 LEFT JOIN users u ON u.id=l.user_id WHERE l.alert_id=$3))
WHERE EXISTS (SELECT FROM alert_roles($1,$2,$3))`

func (a api) workflow(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "Invalid alert ID.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	var result json.RawMessage
	if r.Method == http.MethodGet {
		err = a.reader.QueryRow(ctx, historySQL, subject, slug, id).Scan(&result)
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
			return recordStep(ctx, tx, subject, slug, id, input, &result)
		})
	}
	var problem *stepError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, 404, "Alert not found.")
	case errors.As(err, &problem):
		writeError(w, problem.status, problem.message)
	case err != nil:
		a.logger.Error("Could not process alert workflow", "error", err)
		writeError(w, 500, "Could not load or save this step.")
	default:
		writeJSON(w, 200, result)
	}
}

type stepError struct {
	status  int
	message string
}

func (e *stepError) Error() string { return e.message }

func recordStep(ctx context.Context, tx pgx.Tx, subject, slug string, id int64, in stepRequest, result *json.RawMessage) error {
	var current, user string
	var entered time.Time
	var template int64
	var extended bool
	// Serialize requests for one alert, including concurrent offline retries.
	err := tx.QueryRow(ctx, `SELECT current_step,entered_at,template_id,r.user_id::text,workflow_template_extended(i.template_id)
 FROM workflow_instances i CROSS JOIN alert_roles($1,$2,$3) r
 WHERE alert_id=$3 LIMIT 1 FOR UPDATE OF i`, subject, slug, id).Scan(&current, &entered, &template, &user, &extended)
	if err != nil {
		return err
	}
	var lon, lat *float64
	if in.Location != nil {
		lon = in.Location.Longitude
		lat = in.Location.Latitude
	}
	if in.ClientID != "" {
		var same bool
		err = tx.QueryRow(ctx, `SELECT user_id=$3 AND to_step=$4 AND note=$5 AND override_reason=$6
   AND client_time IS NOT DISTINCT FROM $7::timestamptz AND ST_X(location) IS NOT DISTINCT FROM $8::float8
   AND ST_Y(location) IS NOT DISTINCT FROM $9::float8
   AND (actual_at_supplied = ($10::timestamptz IS NOT NULL) AND ($10 IS NULL OR actual_at=$10))
   AND estimated_at IS NOT DISTINCT FROM $11::timestamptz AND response_note IS NOT DISTINCT FROM $12::jsonb,
   jsonb_build_object('id',id::text,'step',to_step,'at',occurred_at,'actualAt',actual_at,'estimatedAt',estimated_at,'syncedAt',recorded_at)
   FROM step_log WHERE alert_id=$1 AND client_id=$2`, id, in.ClientID, user, in.Step, in.Note, in.Reason, in.ClientTime, lon, lat, in.ActualAt, in.EstimatedAt, in.Response).Scan(&same, result)
		if err == nil {
			if !same {
				return &stepError{409, "This client ID already belongs to another step request."}
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	var available, allowed, required, override, near bool
	var now time.Time
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM workflow_transitions WHERE template_id=$4 AND from_step=$5 AND to_step=$6),
 m.step IS NOT NULL, COALESCE(m.location_required,false),COALESCE(m.can_override,false),
 COALESCE(ST_DWithin(d.geom::geography, ST_SetSRID(ST_Point($7,$8),4326)::geography,w.workflow_radius_metres),false),
 clock_timestamp()
 FROM alerts a JOIN workspaces w ON w.id=a.workspace_id JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
 LEFT JOIN alert_moves($1,$2,$3) m ON m.step=$6 WHERE a.id=$3`, subject, slug, id, template, current, in.Step, lon, lat).Scan(&available, &allowed, &required, &override, &near, &now)
	if err != nil {
		return err
	}
	if !available {
		return &stepError{409, "This move is unavailable from the current step."}
	}
	if !allowed {
		return &stepError{403, "Your role cannot record this step."}
	}
	if err := validateResponseStep(in, required, override, near); err != nil {
		return err
	}
	// A step with an assigned form is complete only once the form is in.
	var missing []string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(array_agg(title ORDER BY form_key),'{}') FROM alert_missing_forms($1,$2)`, id, in.Step).Scan(&missing); err != nil {
		return err
	}
	if len(missing) > 0 {
		return &stepError{422, "Submit the " + strings.Join(missing, ", ") + " form before leaving this step."}
	}
	at, actual, err := stepTimes(in, extended, entered, now)
	if err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `INSERT INTO step_log(alert_id,template_id,from_step,to_step,user_id,occurred_at,location,note,override_reason,client_id,client_time,report_pending,actual_at,estimated_at,response_note,actual_at_supplied)
 SELECT $1,$2,$3,$4,$5,$6,ST_SetSRID(ST_Point($7,$8),4326),$9,$10,NULLIF($11,''),$12,report_pending,$13,$14,$15,$16
 FROM workflow_steps WHERE template_id=$2 AND slug=$4
 RETURNING jsonb_build_object('id',id::text,'step',to_step,'at',occurred_at,'actualAt',actual_at,'estimatedAt',estimated_at,'syncedAt',recorded_at)`, id, template, current, in.Step, user, at, lon, lat, in.Note, in.Reason, in.ClientID, in.ClientTime, actual, in.EstimatedAt, in.Response, in.ActualAt != nil).Scan(result)
	if err != nil || in.Step == "response_update" {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE workflow_instances SET current_step=$2,entered_at=$3 WHERE alert_id=$1`, id, in.Step, at)
	return err
}
