package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type nationalActor struct {
	slug, subject string
}

func nationalCaller(r *http.Request) nationalActor {
	return nationalActor{chi.URLParam(r, "slug"), r.Context().Value(identityKey{}).(identity).Subject}
}

func nationalProblem(err error) (status int, message string) {
	var problem *reportProblem
	var dbErr *pgconn.PgError
	switch {
	case errors.As(err, &problem):
		return problem.status, problem.message
	case errors.As(err, &dbErr) && (dbErr.Code == "23505" || dbErr.Code == "23P01" || dbErr.Code == "23503" || dbErr.Code == "40001"):
		return 409, "The national record conflicts with an existing record."
	default:
		// Database diagnostics can include rejected field values.
		return 500, "Could not process the national record."
	}
}

func nationalFailure(w http.ResponseWriter, err error) {
	status, message := nationalProblem(err)
	writeError(w, status, message)
}

func (a api) nationalOperation(w http.ResponseWriter, r *http.Request, operation func(context.Context, pgx.Tx) (int, any, error)) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var result any
	var status int
	err := pgx.BeginFunc(ctx, a.pool, func(tx pgx.Tx) error {
		var err error
		status, result, err = operation(ctx, tx)
		return err
	})
	if err != nil {
		nationalFailure(w, err)
		return
	}
	writeJSON(w, status, result)
}

func nationalAudit(ctx context.Context, tx pgx.Tx, workspace, subject, action, targetType, id string, details any) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,details)
VALUES($1,CASE WHEN $2='' THEN 'system' ELSE 'user' END,nullif($2,''),'national.'||$3,$4,nullif($5,''),coalesce($6::jsonb,'{}'))`,
		workspace, subject, action, targetType, id, details)
	return err
}

var errNationalScope = dailyError(403, "A national role for this record is required.")

func nationalWorkspaceID(ctx context.Context, tx pgx.Tx, slug string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM workspaces WHERE slug=$1 AND kind='national'`, slug).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errNationalScope
	}
	return id, err
}

func nationalStateForRole(ctx context.Context, tx pgx.Tx, caller nationalActor, code, role string) (id int64, workspace string, err error) {
	err = tx.QueryRow(ctx, `SELECT s.id,s.workspace_id::text FROM national_states s JOIN national_state_grants g ON g.state_id=s.id
WHERE g.slug=$1 AND g.oidc_subject=$2 AND s.state_code=$3 AND g.role_slug=$4 FOR UPDATE OF s`,
		caller.slug, caller.subject, code, role).Scan(&id, &workspace)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errNationalScope
	}
	return
}

type nationalWindow struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (w nationalWindow) valid() bool {
	if !dailyDate(w.From) || !dailyDate(w.To) || w.To < w.From {
		return false
	}
	from, _ := time.Parse(time.DateOnly, w.From)
	to, _ := time.Parse(time.DateOnly, w.To)
	return to.Sub(from) < 366*24*time.Hour
}

func nationalWindowID(ctx context.Context, tx pgx.Tx, state int64, window nationalWindow, create bool) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM national_windows WHERE state_id=$1 AND days=daterange($2::date,$3::date,'[]')`,
		state, window.From, window.To).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) && create {
		err = tx.QueryRow(ctx, `INSERT INTO national_windows(state_id,days) VALUES ($1,daterange($2::date,$3::date,'[]')) RETURNING id`,
			state, window.From, window.To).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = dailyError(404, "The reporting window was not found.")
	}
	return id, err
}

func (a api) nationalSharing(w http.ResponseWriter, r *http.Request) {
	var input struct {
		nationalWindow
		Ministry     bool `json:"ministry"`
		Survey       bool `json:"survey"`
		Preparedness bool `json:"preparedness"`
		Feedback     bool `json:"feedback"`
	}
	if !decodeDaily(w, r, &input) || !input.valid() {
		writeError(w, 400, "Check the reporting window.")
		return
	}
	a.nationalOperation(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		caller := nationalCaller(r)
		state, workspace, err := nationalStateForRole(ctx, tx, caller, chi.URLParam(r, "state"), "national_nodal")
		if err != nil {
			return 0, nil, err
		}
		id, err := nationalWindowID(ctx, tx, state, input.nationalWindow, true)
		if err != nil {
			return 0, nil, err
		}
		_, err = tx.Exec(ctx, `UPDATE national_windows SET ministry_shared=$2,survey_shared=$3,preparedness_shared=$4,feedback_shared=$5 WHERE id=$1`,
			id, input.Ministry, input.Survey, input.Preparedness, input.Feedback)
		if err == nil {
			err = nationalAudit(ctx, tx, workspace, caller.subject, "sharing", "national_window", chi.URLParam(r, "state"), input)
		}
		return 200, input, err
	})
}

func (a api) nationalPublication(w http.ResponseWriter, r *http.Request) {
	var input struct {
		nationalWindow
		ExtractID *string `json:"extractId"`
	}
	if !decodeDaily(w, r, &input) || !input.valid() || (input.ExtractID != nil && !stateExtractID.MatchString(*input.ExtractID)) {
		writeError(w, 400, "Check the reporting window and extract reference.")
		return
	}
	a.nationalOperation(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		caller := nationalCaller(r)
		state, workspace, err := nationalStateForRole(ctx, tx, caller, chi.URLParam(r, "state"), "national_nodal")
		if err != nil {
			return 0, nil, err
		}
		id, err := nationalWindowID(ctx, tx, state, input.nationalWindow, false)
		if err != nil {
			return 0, nil, err
		}
		if input.ExtractID == nil {
			_, err = tx.Exec(ctx, `DELETE FROM national_publications WHERE window_id=$1`, id)
		} else {
			tag, insertErr := tx.Exec(ctx, `INSERT INTO national_publications(window_id,extract_id)
SELECT window_id,id FROM national_current_extracts WHERE window_id=$1 AND kind='incident' AND id=$2
ON CONFLICT(window_id) DO UPDATE SET extract_id=excluded.extract_id`, id, *input.ExtractID)
			err = insertErr
			if err == nil && tag.RowsAffected() == 0 {
				err = dailyError(409, "Approve the current incident extract for this window.")
			}
		}
		if err == nil {
			err = nationalAudit(ctx, tx, workspace, caller.subject, "publication", "national_window", chi.URLParam(r, "state"), input)
		}
		return 200, input, err
	})
}

// State routes work with operational records and maps. Keep national role
// assignments out of those handlers, including ones that grant any member access.
func (a api) stateWorkspaceOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug := chi.URLParam(r, "slug")
		if slug == "" {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		var national bool
		err := a.reader.QueryRow(ctx, `SELECT EXISTS(SELECT FROM workspaces WHERE slug=$1 AND kind='national')`,
			slug).Scan(&national)
		if err != nil {
			nationalFailure(w, err)
		} else if national {
			writeError(w, 403, "Use the national portal for this workspace.")
		} else {
			next.ServeHTTP(w, r)
		}
	})
}

// nationalJSON executes a query whose result is a single JSON document.
func nationalJSON(ctx context.Context, tx pgx.Tx, query string, args ...any) (int, any, error) {
	var data json.RawMessage
	err := tx.QueryRow(ctx, query, args...).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errNationalScope
	}
	return 200, data, err
}
