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
	Note       string     `json:"note"`
	Reason     string     `json:"reason"`
	ClientID   string     `json:"clientId"`
	ClientTime *time.Time `json:"clientTime"`
}

const historySQL = `
SELECT jsonb_build_object('moves',COALESCE((SELECT jsonb_agg(to_jsonb(m) ORDER BY terminal,label)
 FROM alert_moves($1,$2,$3) m),'[]'),'entries',(
 SELECT jsonb_agg(jsonb_build_object('from',l.from_step,'step',l.to_step,'label',s.label,
  'at',l.occurred_at,'actor',u.display_name,'note',l.note,'reason',l.override_reason,
  'longitude',ST_X(l.location),'latitude',ST_Y(l.location),'reportPending',l.report_pending) ORDER BY l.id)
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
	// Serialize requests for one alert, including concurrent offline retries.
	err := tx.QueryRow(ctx, `SELECT current_step,entered_at,template_id,r.user_id::text
 FROM workflow_instances i CROSS JOIN alert_roles($1,$2,$3) r
 WHERE alert_id=$3 LIMIT 1 FOR UPDATE OF i`, subject, slug, id).Scan(&current, &entered, &template, &user)
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
   AND ST_Y(location) IS NOT DISTINCT FROM $9::float8,jsonb_build_object('id',id::text,'step',to_step,'at',occurred_at)
   FROM step_log WHERE alert_id=$1 AND client_id=$2`, id, in.ClientID, user, in.Step, in.Note, in.Reason, in.ClientTime, lon, lat).Scan(&same, result)
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
	if in.Reason != "" && (!override || !required) {
		return &stepError{403, "Only a supervising officer may give a location exception for a field step."}
	}
	if required && !near && in.Reason == "" {
		return &stepError{422, "Record a location near the detection."}
	}
	at := now
	if in.ClientTime != nil {
		at = *in.ClientTime
		if at.Before(entered) || at.After(now) {
			return &stepError{422, "The client time must fall between the current step and now."}
		}
	}
	err = tx.QueryRow(ctx, `INSERT INTO step_log(alert_id,template_id,from_step,to_step,user_id,occurred_at,location,note,override_reason,client_id,client_time,report_pending)
 SELECT $1,$2,$3,$4,$5,$6,ST_SetSRID(ST_Point($7,$8),4326),$9,$10,NULLIF($11,''),$12,report_pending
 FROM workflow_steps WHERE template_id=$2 AND slug=$4
 RETURNING jsonb_build_object('id',id::text,'step',to_step,'at',occurred_at)`, id, template, current, in.Step, user, at, lon, lat, in.Note, in.Reason, in.ClientID, in.ClientTime).Scan(result)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE workflow_instances SET current_step=$2,entered_at=$3 WHERE alert_id=$1`, id, in.Step, at)
	return err
}
