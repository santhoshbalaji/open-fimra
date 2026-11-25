package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Submissions are filled in for an alert at a workflow step, with a form
// assigned to that step. Who can read, submit and approve follows the
// alert: alert_roles applies the same grants and queue rules as the alert
// queue and the workflow.

type submissionRequest struct {
	ID         string          `json:"id"`
	Form       string          `json:"form"`
	Version    int             `json:"version"`
	Step       string          `json:"step"`
	Values     json.RawMessage `json:"values"`
	DeviceTime *time.Time      `json:"deviceTime"`
	Location   *struct {
		Longitude *float64 `json:"longitude"`
		Latitude  *float64 `json:"latitude"`
	} `json:"location"`
}

// maxSubmissionBytes is the largest submission body. Photos and files are
// uploaded separately, so the answers themselves stay small.
const maxSubmissionBytes = 2 << 20

// submissionJSON describes a submission and its decision, if any. It reads
// from submissionFrom.
const submissionJSON = `jsonb_build_object('id', s.id, 'alertId', s.alert_id::text, 'incidentId', s.incident_id::text,
    'form', f.form_key, 'version', v.version, 'step', s.step,
    'submittedBy', u.display_name, 'deviceTime', s.device_time, 'receivedAt', s.received_at,
    'location', CASE WHEN s.location IS NOT NULL THEN jsonb_build_object('longitude', ST_X(s.location), 'latitude', ST_Y(s.location)) END,
    'values', s.answers,
    'approval', (SELECT jsonb_build_object('decision', ap.decision, 'note', ap.note, 'by', au.display_name, 'at', ap.decided_at)
        FROM approvals ap JOIN users au ON au.id = ap.user_id WHERE ap.submission_id = s.id))`

const submissionFrom = `
FROM submissions s JOIN form_versions v ON v.id = s.form_version_id JOIN forms f ON f.id = v.form_id
JOIN users u ON u.id = s.user_id`

func alertRoute(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		return 0, problem(http.StatusBadRequest, "Invalid alert ID.")
	}
	return id, nil
}

// alertUser returns the caller's user ID when they can see the alert.
func alertUser(ctx context.Context, tx pgx.Tx, subject, slug string, alert int64) (string, error) {
	var user string
	err := tx.QueryRow(ctx, `SELECT user_id::text FROM alert_roles($1, $2, $3) LIMIT 1`, subject, slug, alert).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", problem(http.StatusNotFound, "Alert not found.")
	}
	return user, err
}

// alertForms lists the forms assigned to an alert's workflow, by step.
func (a api) alertForms(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	var result json.RawMessage
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		id, err := alertRoute(r)
		if err != nil {
			return err
		}
		if _, err := alertUser(ctx, tx, subject, chi.URLParam(r, "slug"), id); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
SELECT jsonb_build_object('step', i.current_step, 'forms', COALESCE((
    SELECT jsonb_agg(jsonb_build_object('form', f.form_key, 'version', f.version, 'title', f.title, 'step', f.step,
        'submitted', f.submitted, 'requiredNow', f.step = i.current_step AND NOT f.submitted) ORDER BY f.step, f.form_key)
    FROM alert_forms($1) f), '[]'))
FROM workflow_instances i WHERE i.alert_id = $1`, id).Scan(&result)
	})
	if err != nil {
		a.writeProblem(w, err, "Alert not found.", "Could not load the alert's forms")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// submissions lists an alert's submissions or records a new one.
func (a api) submissions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	var input submissionRequest
	var hash string
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSubmissionBytes))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "A submission can be at most 2 MB. Upload photos and files as attachments.")
			return
		}
		d := json.NewDecoder(strings.NewReader(string(body)))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || !uuidPattern.MatchString(input.ID) ||
			!formKeyPattern.MatchString(input.Form) || input.Version < 1 || (input.Step != "" && !stepSlug.MatchString(input.Step)) || len(input.Values) == 0 {
			writeError(w, http.StatusBadRequest, "Provide a submission ID, a form, its version and the values.")
			return
		}
		if p := input.Location; p != nil && (p.Latitude == nil || p.Longitude == nil || *p.Latitude < -90 || *p.Latitude > 90 || *p.Longitude < -180 || *p.Longitude > 180) {
			writeError(w, http.StatusBadRequest, "Provide a valid latitude and longitude.")
			return
		}
		// The hash covers the request exactly as decoded, so a retry of the
		// same request matches and any change does not.
		canonical, _ := json.Marshal(map[string]any{"body": json.RawMessage(compactJSON(body)), "alert": chi.URLParam(r, "id"), "subject": subject})
		sum := sha256.Sum256(canonical)
		hash = hex.EncodeToString(sum[:])
	}
	var result json.RawMessage
	status := http.StatusOK
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		id, err := alertRoute(r)
		if err != nil {
			return err
		}
		user, err := alertUser(ctx, tx, subject, slug, id)
		if err != nil {
			return err
		}
		if r.Method == http.MethodGet {
			return tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(`+submissionJSON+` ORDER BY s.received_at, s.id), '[]')`+
				submissionFrom+` WHERE s.alert_id = $1`, id).Scan(&result)
		}
		created, err := a.recordSubmission(ctx, tx, user, id, input, hash)
		if err != nil {
			return err
		}
		if created {
			status = http.StatusCreated
		}
		return tx.QueryRow(ctx, `SELECT `+submissionJSON+submissionFrom+` WHERE s.id = $1`, input.ID).Scan(&result)
	})
	if err != nil {
		a.writeProblem(w, err, "Alert not found.", "Could not load or save the submission")
		return
	}
	if status == http.StatusCreated {
		w.Header().Set("Location", "/api/v1/workspaces/"+slug+"/submissions/"+input.ID)
	}
	writeJSON(w, status, result)
}

func compactJSON(data []byte) []byte {
	var out bytes.Buffer
	if err := json.Compact(&out, data); err != nil {
		return data
	}
	return out.Bytes()
}

// recordSubmission checks and stores a submission. A retry of the same
// request by the same user finds the first one and returns created=false.
func (a api) recordSubmission(ctx context.Context, tx pgx.Tx, user string, alert int64, in submissionRequest, hash string) (bool, error) {
	// Serialize requests with the same ID, including concurrent retries.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('open-fimra:submission:' || $1, 0))`, in.ID); err != nil {
		return false, err
	}
	var sameRequest bool
	err := tx.QueryRow(ctx, `SELECT request_hash = $2 AND user_id = $3 AND alert_id = $4 FROM submissions WHERE id = $1`,
		in.ID, hash, user, alert).Scan(&sameRequest)
	if err == nil {
		if !sameRequest {
			return false, problem(http.StatusConflict, "This submission ID already belongs to another submission.")
		}
		return false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}

	var workspaceID, source string
	var versionID, formID, templateID int64
	err = tx.QueryRow(ctx, `SELECT a.workspace_id::text, v.id, f.id, v.source, i.template_id
FROM alerts a JOIN workflow_instances i ON i.alert_id = a.id
JOIN forms f ON f.workspace_id = a.workspace_id AND f.form_key = $2
JOIN form_versions v ON v.form_id = f.id AND v.version = $3 AND v.published_at IS NOT NULL
WHERE a.id = $1`, alert, in.Form, in.Version).Scan(&workspaceID, &versionID, &formID, &source, &templateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, problem(http.StatusUnprocessableEntity, "Version "+strconv.Itoa(in.Version)+" of the form "+in.Form+" is not published in this workspace.")
	}
	if err != nil {
		return false, err
	}
	// The form must be assigned to a step of this alert's workflow. Any
	// published version of it is accepted, because a phone that was offline
	// may still have an earlier version.
	var steps []string
	err = tx.QueryRow(ctx, `SELECT COALESCE(array_agg(step ORDER BY step), '{}') FROM alert_forms($1) WHERE form_id = $2`, alert, formID).Scan(&steps)
	if err != nil {
		return false, err
	}
	step := in.Step
	switch {
	case len(steps) == 0:
		return false, problem(http.StatusUnprocessableEntity, "The form "+in.Form+" is not assigned to this alert's workflow.")
	case step == "" && len(steps) == 1:
		step = steps[0]
	case step == "":
		return false, problem(http.StatusUnprocessableEntity, "The form "+in.Form+" is used at more than one step. Name the step: "+strings.Join(steps, ", ")+".")
	case !slices.Contains(steps, step):
		return false, problem(http.StatusUnprocessableEntity, "The form "+in.Form+" is not assigned to the step "+step+" for this alert.")
	}
	tree, err := parseFormTree([]byte(source))
	if err != nil {
		return false, err
	}
	stage, err := tree.stageAt(step)
	if err != nil {
		return false, problem(http.StatusUnprocessableEntity, "Version "+strconv.Itoa(in.Version)+" of the form "+in.Form+" has no stage at the step "+step+".")
	}

	var reached bool
	var facts alertFacts
	var now time.Time
	err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM step_log l WHERE l.alert_id = a.id AND l.to_step = $2),
    a.id, a.observed_at, a.created_at, ST_X(d.geom), ST_Y(d.geom),
    COALESCE((SELECT u.name FROM units u WHERE u.hierarchy_id = a.hierarchy_id AND u.path = a.unit_path
              ORDER BY u.valid_to DESC NULLS FIRST LIMIT 1), ''),
    a.forest_class, d.source, d.confidence, d.frp_mw::float8, clock_timestamp()
FROM alerts a JOIN detections d ON (d.id, d.observed_at) = (a.detection_id, a.observed_at) WHERE a.id = $1`, alert, step).Scan(
		&reached, &facts.ID, &facts.ObservedAt, &facts.RaisedAt, &facts.Longitude, &facts.Latitude,
		&facts.Unit, &facts.ForestClass, &facts.Source, &facts.Confidence, &facts.FirePower, &now)
	if err != nil {
		return false, err
	}
	if !reached {
		return false, problem(http.StatusConflict, "This alert has not reached the step "+step+" yet. Record the step first.")
	}
	if in.DeviceTime != nil && in.DeviceTime.After(now.Add(clockSkew)) {
		return false, problem(http.StatusUnprocessableEntity, "The device time cannot be in the future.")
	}

	attachments := map[string]storedAttachment{}
	rows, err := tx.Query(ctx, `SELECT id::text, content_type, file_name, size_bytes FROM attachments WHERE alert_id = $1`, alert)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var id string
		var file storedAttachment
		if err := rows.Scan(&id, &file.ContentType, &file.FileName, &file.SizeBytes); err != nil {
			rows.Close()
			return false, err
		}
		attachments[id] = file
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	check := &answerCheck{tree: tree, stage: stage, alert: facts, now: now, attachments: attachments,
		measure: func(geometry json.RawMessage) (float64, error) {
			var size float64
			err := tx.QueryRow(ctx, `SELECT CASE WHEN GeometryType(g) = 'POLYGON' THEN ST_Area(g::geography) / 10000
    ELSE ST_Length(g::geography) / 1000 END FROM (SELECT ST_SetSRID(ST_GeomFromGeoJSON($1::text), 4326) AS g) x`, string(geometry)).Scan(&size)
			return size, err
		}}
	answers, problems := checkAnswers(check, in.Values)
	if len(problems) > 0 {
		return false, problem(http.StatusUnprocessableEntity, "The answers do not fit the form.", problems...)
	}
	answerJSON, err := json.Marshal(answers)
	if err != nil {
		return false, err
	}
	var lon, lat *float64
	if in.Location != nil {
		lon, lat = in.Location.Longitude, in.Location.Latitude
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO submissions (id, workspace_id, form_version_id, alert_id, incident_id, template_id, step, user_id,
    device_time, location, answers, request_hash)
SELECT $1, $2, $3, $4, (SELECT incident_id FROM incident_members WHERE alert_id = $4), $5, $6, $7,
    $8, CASE WHEN $9::float8 IS NOT NULL THEN ST_SetSRID(ST_Point($9, $10), 4326) END, $11, $12`,
		in.ID, workspaceID, versionID, alert, templateID, step, user, in.DeviceTime, lon, lat, answerJSON, hash); err != nil {
		return false, err
	}
	for field, ids := range check.used {
		if _, err := tx.Exec(ctx, `INSERT INTO submission_attachments (submission_id, attachment_id, field)
SELECT $1, unnest($2::uuid[]), $3 ON CONFLICT DO NOTHING`, in.ID, ids, field); err != nil {
			return false, err
		}
	}
	return true, nil
}

// submissionAccess finds a submission the caller can see through its alert,
// and whether the caller is a supervising officer on that alert.
func submissionAccess(ctx context.Context, tx pgx.Tx, subject, slug, id string) (user string, officer bool, mine bool, err error) {
	if !uuidPattern.MatchString(id) {
		return "", false, false, problem(http.StatusNotFound, "Submission not found.")
	}
	err = tx.QueryRow(ctx, `SELECT min(r.user_id::text), bool_or(r.officer), bool_or(r.user_id = s.user_id)
FROM submissions s JOIN workspaces w ON w.id = s.workspace_id AND w.slug = $2
CROSS JOIN LATERAL alert_roles($3, $2, s.alert_id) r
WHERE s.id = $1::uuid GROUP BY s.id`, id, slug, subject).Scan(&user, &officer, &mine)
	if errors.Is(err, pgx.ErrNoRows) {
		err = problem(http.StatusNotFound, "Submission not found.")
	}
	return user, officer, mine, err
}

func (a api) submission(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	id := chi.URLParam(r, "submission")
	var result json.RawMessage
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		if _, _, _, err := submissionAccess(ctx, tx, subject, chi.URLParam(r, "slug"), id); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT `+submissionJSON+submissionFrom+` WHERE s.id = $1`, id).Scan(&result)
	})
	if err != nil {
		a.writeProblem(w, err, "Submission not found.", "Could not load the submission")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type approvalRequest struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

// approve records an officer's decision on a submission: approved, or
// returned with a note. A submission has one decision, which never changes.
// Repeating the same decision returns it again.
func (a api) approve(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	id := chi.URLParam(r, "submission")
	var input approvalRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || (input.Decision != "approved" && input.Decision != "returned") {
		writeError(w, http.StatusBadRequest, "Provide a decision: approved or returned.")
		return
	}
	input.Note = strings.TrimSpace(input.Note)
	if input.Decision == "returned" && input.Note == "" {
		writeError(w, http.StatusUnprocessableEntity, "Say why the submission is returned.")
		return
	}
	if len([]rune(input.Note)) > 2000 {
		writeError(w, http.StatusUnprocessableEntity, "A note can be at most 2,000 characters.")
		return
	}
	var result json.RawMessage
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		user, officer, _, err := submissionAccess(ctx, tx, subject, chi.URLParam(r, "slug"), id)
		if err != nil {
			return err
		}
		if !officer {
			return problem(http.StatusForbidden, "Only a supervising officer on this alert can approve or return a submission.")
		}
		var inserted bool
		err = tx.QueryRow(ctx, `WITH added AS (
    INSERT INTO approvals (submission_id, decision, note, user_id) VALUES ($1, $2, $3, $4)
    ON CONFLICT (submission_id) DO NOTHING RETURNING id)
SELECT EXISTS (SELECT FROM added)`, id, input.Decision, input.Note, user).Scan(&inserted)
		if err != nil {
			return err
		}
		if !inserted {
			var same bool
			if err := tx.QueryRow(ctx, `SELECT decision = $2 AND note = $3 AND user_id = $4 FROM approvals WHERE submission_id = $1`,
				id, input.Decision, input.Note, user).Scan(&same); err != nil {
				return err
			}
			if !same {
				return problem(http.StatusConflict, "This submission already has a decision. Decisions never change.")
			}
		}
		return tx.QueryRow(ctx, `SELECT `+submissionJSON+submissionFrom+` WHERE s.id = $1`, id).Scan(&result)
	})
	if err != nil {
		a.writeProblem(w, err, "Submission not found.", "Could not save the decision")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
