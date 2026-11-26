package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	starterpack "github.com/santhoshbalaji/open-fimra/packages/starter-pack"
)

// A manual fire report records a fire reported by another source: a phone
// call, forest staff, another agency or another satellite service (PLAN.md
// sections 5.4 and 15). Saving it makes a detection with the source
// "manual" at the reported place and time. The detection is matched like a
// satellite one, but in this workspace only, so its alert gets the same
// forest class, units, queue, incident, notifications and workflow. The
// report itself is kept as a submission of the manual fire report form at
// the alert's New step.

const (
	manualSource  = "manual"
	manualProduct = "manual-fire-report"
)

// manualReportSQL inserts the detection, matches it in one workspace and
// routes its alert. It answers whether the place is in the workspace, its
// forest class and the new alert, if any.
const manualReportSQL = `
WITH inserted AS (
    INSERT INTO detections (source, product, source_key, observed_at, geom, raw, workspace_id)
    VALUES ($2, $3, $4, $5, ST_SetSRID(ST_Force2D(ST_GeomFromGeoJSON($6::text)), 4326), $7, $1)
    RETURNING id, observed_at, geom
),
matched AS (
    INSERT INTO detection_matches (workspace_id, detection_id, observed_at, forest_class,
                                   forest_distance_m, forest_feature_id, unit_paths, hierarchy,
                                   hierarchy_unit_paths, triage_reason, layer_version_ids, matched_at,
                                   beat_paths, boundary_snapshot)
    SELECT m.workspace_id, m.detection_id, m.observed_at, m.forest_class,
           m.forest_distance_m, m.forest_feature_id, m.unit_paths, m.hierarchy,
           m.hierarchy_unit_paths, m.triage_reason, m.layer_version_ids, clock_timestamp(),
           m.beat_paths, m.boundary_snapshot
    FROM inserted d CROSS JOIN LATERAL classify_detection($1, d.id, d.observed_at, d.geom) m
    RETURNING detection_matches AS match
),
routed AS (
    -- The alert insert trigger opens its workflow, joins it to an incident
    -- and notifies the responsible officers in this transaction.
    INSERT INTO alerts (workspace_id, detection_id, observed_at, hierarchy, queue, hierarchy_id, unit_path,
                        hierarchy_unit_paths, unit_paths, forest_class, triage_reason, layer_version_ids, boundary_snapshot, intake_incident_id)
    SELECT (match).workspace_id, (match).detection_id, (match).observed_at, (match).hierarchy, route.*,
           (match).hierarchy_unit_paths, (match).unit_paths, (match).forest_class, (match).triage_reason,
           (match).layer_version_ids, (match).boundary_snapshot, $9
    FROM matched CROSS JOIN LATERAL (
        SELECT * FROM alert_route(match)
        UNION ALL SELECT 'triage', NULL::uuid, NULL::ltree
        WHERE $8 AND (match).forest_class IN ('outside','fixed_heat_source')
    ) route
    RETURNING id
)
SELECT EXISTS (SELECT FROM matched), (SELECT (match).forest_class FROM matched), (SELECT id FROM routed)`

// manualFireReport records a manual fire report and makes its alert.
func (a api) manualFireReport(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSubmissionBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "A report can be at most 2 MB.")
		return
	}
	var in submissionRequest
	d := json.NewDecoder(strings.NewReader(string(body)))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || !uuidPattern.MatchString(in.ID) ||
		(in.Form != "" && in.Form != starterpack.ManualFireReport) || in.Step != "" ||
		in.ProvisionalSubmissionID != nil || in.Version < 1 || len(in.Values) == 0 {
		writeError(w, http.StatusBadRequest, "Provide a submission ID, the version of the manual fire report form and the values.")
		return
	}
	in.Form = starterpack.ManualFireReport
	if p := in.Location; p != nil && (p.Latitude == nil || p.Longitude == nil || *p.Latitude < -90 || *p.Latitude > 90 || *p.Longitude < -180 || *p.Longitude > 180) {
		writeError(w, http.StatusBadRequest, "Provide a valid latitude and longitude.")
		return
	}
	canonical, _ := json.Marshal(map[string]any{"body": json.RawMessage(compactJSON(body)), "manual": slug, "subject": subject})
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])

	var result json.RawMessage
	var alert int64
	status := http.StatusOK
	err = pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, slug)
		if err != nil {
			return err
		}
		found, err := earlierSubmission(ctx, tx, in.ID, hash, access.userID, &alert)
		if err != nil {
			return err
		}
		if !found {
			if alert, err = recordManualReport(ctx, tx, access, subject, slug, in, hash); err != nil {
				return err
			}
			status = http.StatusCreated
		}
		return tx.QueryRow(ctx, `SELECT `+submissionJSON+submissionFrom+` WHERE s.id = $1`, in.ID).Scan(&result)
	})
	if err != nil {
		a.writeProblem(w, err, "Workspace not found.", "Could not save the manual fire report")
		return
	}
	if status == http.StatusCreated {
		w.Header().Set("Location", "/api/v1/workspaces/"+slug+"/alerts/"+strconv.FormatInt(alert, 10))
	}
	writeJSON(w, status, result)
}

func recordManualReport(ctx context.Context, tx pgx.Tx, access workspaceAccess, subject, slug string, in submissionRequest, hash string) (int64, error) {
	var versionID int64
	var source string
	err := tx.QueryRow(ctx, `SELECT v.id, v.source FROM forms f JOIN form_versions v ON v.form_id = f.id
WHERE f.workspace_id = $1 AND f.form_key = $2 AND v.version = $3 AND v.published_at IS NOT NULL`,
		access.workspaceID, in.Form, in.Version).Scan(&versionID, &source)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, problem(http.StatusUnprocessableEntity, "Version "+strconv.Itoa(in.Version)+
			" of the manual fire report form is not published in this workspace. Switch on the starter forms first.")
	}
	if err != nil {
		return 0, err
	}
	tree, err := parseFormTree([]byte(source))
	if err != nil {
		return 0, err
	}
	when, place := tree.byKey["reported_at"], tree.byKey["fire_location"]
	if tree.hasStages || when == nil || when.kind != "date-time" || when.repeat != nil ||
		place == nil || place.kind != "point" || place.repeat != nil {
		return 0, problem(http.StatusUnprocessableEntity, "The manual fire report form needs a date and time called reported_at "+
			"and a point called fire_location, outside any repeating group, and no stages.")
	}

	// Check the answers before there is an alert. The form cannot take
	// files: they are uploaded for an alert, and this report makes the alert.
	check := &answerCheck{tree: tree, stage: -1, attachments: map[string]storedAttachment{},
		measure: func(json.RawMessage) (float64, error) { return 0, nil }}
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&check.now); err != nil {
		return 0, err
	}
	if in.DeviceTime != nil && in.DeviceTime.After(check.now.Add(clockSkew)) {
		return 0, problem(http.StatusUnprocessableEntity, "The device time cannot be in the future.")
	}
	answers, problems := checkAnswers(check, in.Values)
	if len(problems) > 0 {
		return 0, problem(http.StatusUnprocessableEntity, "The answers do not fit the form.", problems...)
	}
	reported, _ := answers["reported_at"].(string)
	location, _ := answers["fire_location"].(json.RawMessage)
	observed, ok := parseDateTime(reported)
	if !ok || location == nil {
		return 0, problem(http.StatusUnprocessableEntity, "Give the time the fire was reported and its location.")
	}

	// The same lock and partition rules as satellite ingest.
	if _, err := tx.Exec(ctx, detectionWriteLock); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, "SELECT ensure_detection_partition($1::date)", observed.UTC()); err != nil {
		return 0, err
	}
	raw, _ := json.Marshal(map[string]any{"submissionId": in.ID, "reportSource": answers["report_source"]})
	var inWorkspace bool
	var forestClass *string
	var alert *int64
	if err := tx.QueryRow(ctx, manualReportSQL, access.workspaceID, manualSource, manualProduct, in.ID, observed,
		string(location), raw, false, nil).Scan(&inWorkspace, &forestClass, &alert); err != nil {
		return 0, err
	}
	switch {
	case !inWorkspace:
		return 0, problem(http.StatusUnprocessableEntity, "This place is outside the workspace's area. Check the location.")
	case alert == nil && forestClass != nil && *forestClass == "fixed_heat_source":
		return 0, problem(http.StatusUnprocessableEntity, "This place is on the list of known heat sources, so it makes no alert. Check the location.")
	case alert == nil:
		return 0, problem(http.StatusUnprocessableEntity, "This place is outside the forest and the edge around it, so it makes no alert. Check the location.")
	}

	// The reporter must be able to follow the alert they made.
	user, err := alertUser(ctx, tx, subject, slug, *alert)
	var p *apiProblem
	if errors.As(err, &p) && p.status == http.StatusNotFound {
		return 0, problem(http.StatusUnprocessableEntity, "This place is outside the units you work in. Ask your control room to record the report.")
	}
	if err != nil {
		return 0, err
	}
	// Check again with the alert, so any values the form copies from it are filled in.
	full, err := newAnswerCheck(ctx, tx, tree, -1, *alert)
	if err != nil {
		return 0, err
	}
	full.now = check.now
	answers, problems = checkAnswers(full, in.Values)
	if len(problems) > 0 {
		return 0, problem(http.StatusUnprocessableEntity, "The answers do not fit the form.", problems...)
	}
	var templateID int64
	if err := tx.QueryRow(ctx, `SELECT template_id FROM workflow_instances WHERE alert_id = $1`, *alert).Scan(&templateID); err != nil {
		return 0, err
	}
	if err := storeSubmission(ctx, tx, in, storedSubmission{workspaceID: access.workspaceID, versionID: versionID, alert: *alert,
		templateID: templateID, step: "new", user: user, hash: hash, answers: answers, used: full.used}); err != nil {
		return 0, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
VALUES ($1, 'user', $2, 'alert.manual_report', 'alert', $3, jsonb_build_object('submissionId', $4::text))`,
		access.workspaceID, subject, strconv.FormatInt(*alert, 10), in.ID)
	return *alert, err
}
