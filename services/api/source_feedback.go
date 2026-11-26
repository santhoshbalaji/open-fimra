package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/santhoshbalaji/open-fimra/packages/starter-pack"
)

//go:embed source_feedback_choices.json
var publicFeedbackChoices []byte

type feedbackChoices struct {
	FireSeen  map[string]string `json:"fireSeen"`
	LandClass map[string]string `json:"landClass"`
	FireType  map[string]string `json:"fireType"`
	Cause     map[string]string `json:"cause"`
}

func loadFeedbackChoices(path string) (feedbackChoices, error) {
	data := publicFeedbackChoices
	if path != "" {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return feedbackChoices{}, fmt.Errorf("read source feedback choices: %w", err)
		}
	}
	var choices feedbackChoices
	if err := json.Unmarshal(data, &choices); err != nil {
		return choices, fmt.Errorf("read source feedback choices: %w", err)
	}
	if choices.FireSeen["yes"] == "" || choices.FireSeen["no"] == "" ||
		len(choices.LandClass) == 0 || len(choices.FireType) == 0 || len(choices.Cause) == 0 {
		return choices, errors.New("source feedback choices need yes and no, land class, fire type and cause mappings")
	}
	return choices, nil
}

type feedbackPayload struct {
	FireSeen      string   `json:"fireSeen"`
	LandClass     string   `json:"landClass,omitempty"`
	FireType      string   `json:"fireType,omitempty"`
	OtherFireType string   `json:"otherFireType,omitempty"`
	Cause         string   `json:"cause,omitempty"`
	AreaHa        *float64 `json:"areaHa,omitempty"`
	Remarks       string   `json:"remarks,omitempty"`
}

type feedbackRequest struct {
	VerificationID string  `json:"verificationId"`
	AssessmentID   *string `json:"assessmentId"`
}

type feedbackDelivery struct {
	ID               string
	ExternalAlertRef string
	Payload          feedbackPayload
}

type feedbackReceipt struct {
	Reference string
	// Status is empty if the provider has yet to report a decision.
	Status string
}

// A sender must use the stable delivery ID for retries. A request can be
// retried if the provider received it but saving the receipt failed.
type feedbackSender interface {
	Send(context.Context, feedbackDelivery) (feedbackReceipt, error)
}

type feedbackReceiptReader interface {
	Receipt(context.Context, feedbackDelivery, string) (feedbackReceipt, error)
}

// The synthetic sender has no network connection. Live FSI alerts have no
// sender until the provider confirms the exchange.
type syntheticFeedbackSender struct{}

func (syntheticFeedbackSender) Send(_ context.Context, d feedbackDelivery) (feedbackReceipt, error) {
	if !strings.HasPrefix(d.ExternalAlertRef, "DEMO-FSI-") {
		return feedbackReceipt{}, errors.New("synthetic feedback requires a demo reference")
	}
	return feedbackReceipt{Reference: "demo/" + d.ExternalAlertRef, Status: "accepted"}, nil
}

func (a api) feedbackSenderFor(source string) feedbackSender {
	if source == "demo_fsi" {
		return syntheticFeedbackSender{}
	}
	return a.feedbackSenders[source]
}

func buildFeedback(choices feedbackChoices, verification, assessment map[string]any) (feedbackPayload, error) {
	answer := func(key string) string {
		value, _ := verification[key].(string)
		return value
	}
	seen := answer("fire_seen")
	out := feedbackPayload{FireSeen: choices.FireSeen[seen], Remarks: answer("remarks")}
	switch seen {
	case "yes":
		out.LandClass = choices.LandClass[answer("land_class")]
		out.FireType = choices.FireType[answer("fire_type")]
		out.Cause = choices.Cause[answer("fire_cause")]
		if answer("fire_type") == "other" {
			out.OtherFireType = answer("other_fire_type")
		}
		if area, ok := number(assessment, "alert_area_ha"); ok {
			out.AreaHa = &area
		}
		if out.FireSeen == "" || out.LandClass == "" || out.FireType == "" || out.Cause == "" ||
			(answer("fire_type") == "other" && strings.TrimSpace(out.OtherFireType) == "") {
			return feedbackPayload{}, problem(http.StatusUnprocessableEntity, "The verification contains a fire classification that cannot be sent.")
		}
	case "no":
		if assessment == nil && out.FireSeen != "" {
			return out, nil
		}
		return feedbackPayload{}, problem(http.StatusUnprocessableEntity, "An area can only be sent when fire was seen.")
	default:
		return feedbackPayload{}, problem(http.StatusUnprocessableEntity, "Record whether fire was seen before preparing feedback.")
	}
	return out, nil
}

const feedbackExternalRef = `CASE WHEN d.source IN ('fsi', 'demo_fsi')
    AND jsonb_typeof(d.raw->'externalAlertRef') = 'string'
    AND length(btrim(d.raw->>'externalAlertRef')) BETWEEN 1 AND 500
    THEN btrim(d.raw->>'externalAlertRef') END`

const feedbackAlert = ` FROM alerts a JOIN detections d ON (d.id, d.observed_at) = (a.detection_id, a.observed_at)`

const feedbackJSON = `jsonb_build_object('id', f.id, 'externalAlertRef', f.external_alert_ref,
    'verificationId', f.verification_id, 'assessmentId', f.assessment_id, 'payload', f.payload, 'status', f.status,
    'preparedAt', f.prepared_at, 'submittedAt', f.submitted_at, 'decidedAt', f.decided_at, 'providerReceipt', f.provider_receipt)`

func (a api) alertSourceFeedback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	alert, err := alertRoute(r)
	if err != nil {
		a.writeProblem(w, err, "Alert not found.", "Could not load source feedback")
		return
	}
	subject, slug := ctx.Value(identityKey{}).(identity).Subject, chi.URLParam(r, "slug")
	var in feedbackRequest
	if r.Method == http.MethodPost {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF || !uuidPattern.MatchString(in.VerificationID) ||
			(in.AssessmentID != nil && !uuidPattern.MatchString(*in.AssessmentID)) {
			writeError(w, http.StatusBadRequest, "Provide a verification submission ID and, if applicable, an assessment submission ID.")
			return
		}
	}
	var id, source string
	var result json.RawMessage
	err = pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		var officer *bool
		if err := tx.QueryRow(ctx, `SELECT bool_or(officer) FROM alert_roles($1, $2, $3)`, subject, slug, alert).Scan(&officer); err != nil {
			return err
		}
		if officer == nil {
			return problem(http.StatusNotFound, "Alert not found.")
		}
		if r.Method == http.MethodGet {
			return tx.QueryRow(ctx, `SELECT jsonb_build_object('externalAlertRef', `+feedbackExternalRef+`,
    'canPrepare', $2::boolean, 'records', COALESCE((SELECT jsonb_agg(`+feedbackJSON+` ORDER BY prepared_at DESC, id DESC)
        FROM source_feedback f WHERE f.alert_id = a.id), '[]'))
`+feedbackAlert+` WHERE a.id = $1`, alert, *officer).Scan(&result)
		}
		if !*officer {
			return problem(http.StatusForbidden, "Only a supervising officer can prepare source feedback.")
		}
		id, source, err = a.prepareSourceFeedback(ctx, tx, alert, subject, in)
		return err
	})
	if err == nil && r.Method == http.MethodPost {
		err = pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
			if err := deliverSourceFeedback(ctx, tx, id, a.feedbackSenderFor(source)); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT `+feedbackJSON+` FROM source_feedback f WHERE id = $1`, id).Scan(&result)
		})
	}
	if err != nil {
		a.writeProblem(w, err, "Alert not found.", "Could not load or save source feedback")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a api) prepareSourceFeedback(ctx context.Context, tx pgx.Tx, alert int64, subject string, in feedbackRequest) (id, source string, err error) {
	var ref *string
	err = tx.QueryRow(ctx, `SELECT d.source, `+feedbackExternalRef+feedbackAlert+` WHERE a.id = $1`, alert).Scan(&source, &ref)
	if err != nil {
		return "", "", err
	}
	if ref == nil {
		return "", "", problem(http.StatusUnprocessableEntity, "This alert has no external feedback reference.")
	}
	err = tx.QueryRow(ctx, `SELECT id FROM source_feedback WHERE alert_id = $1 AND verification_id = $2
    AND assessment_id IS NOT DISTINCT FROM $3::uuid`, alert, in.VerificationID, in.AssessmentID).Scan(&id)
	if !errors.Is(err, pgx.ErrNoRows) {
		return id, source, err
	}

	verification, err := readFeedbackAnswers(ctx, tx, alert, in.VerificationID, starterpack.ResponseFlash)
	if err != nil {
		return "", "", err
	}
	var assessment map[string]any
	if in.AssessmentID != nil {
		assessment, err = readFeedbackAnswers(ctx, tx, alert, *in.AssessmentID, starterpack.DamageAssessment)
		if err != nil {
			return "", "", err
		}
	}
	choices := a.feedbackChoices
	if choices.FireSeen == nil {
		if choices, err = loadFeedbackChoices(""); err != nil {
			return "", "", err
		}
	}
	payload, err := buildFeedback(choices, verification, assessment)
	if err != nil {
		return "", "", err
	}
	// On concurrent requests, the unique key chooses the original payload.
	_, err = tx.Exec(ctx, `WITH added AS (
    INSERT INTO source_feedback (alert_id, verification_id, assessment_id, external_alert_ref, payload)
    VALUES ($1, $2, $3, $4, $5) ON CONFLICT (alert_id, verification_id, assessment_id) DO NOTHING RETURNING id, alert_id
)
INSERT INTO audit_log(workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
SELECT a.workspace_id, 'user', $6, 'feedback.prepare', 'source_feedback', f.id::text, jsonb_build_object('alertId', a.id)
FROM added f JOIN alerts a ON a.id = f.alert_id`, alert, in.VerificationID, in.AssessmentID, *ref, payload, subject)
	if err != nil {
		return "", "", err
	}
	err = tx.QueryRow(ctx, `SELECT id FROM source_feedback WHERE alert_id = $1 AND verification_id = $2
    AND assessment_id IS NOT DISTINCT FROM $3::uuid`, alert, in.VerificationID, in.AssessmentID).Scan(&id)
	return id, source, err
}

func readFeedbackAnswers(ctx context.Context, tx pgx.Tx, alert int64, id, form string) (map[string]any, error) {
	var answers map[string]any
	err := tx.QueryRow(ctx, `SELECT s.answers FROM submissions s
JOIN form_versions v ON v.id = s.form_version_id JOIN forms f ON f.id = v.form_id
JOIN approvals ap ON ap.submission_id = s.id AND ap.decision = 'approved'
WHERE s.id = $1 AND s.alert_id = $2 AND f.form_key = $3`, id, alert, form).Scan(&answers)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, problem(http.StatusUnprocessableEntity, "Choose approved reports from this alert.")
	}
	return answers, err
}

func deliverSourceFeedback(ctx context.Context, tx pgx.Tx, id string, sender feedbackSender) error {
	if sender == nil {
		return nil
	}
	var d feedbackDelivery
	var status string
	var reference *string
	d.ID = id
	if err := tx.QueryRow(ctx, `SELECT external_alert_ref, payload, status, provider_receipt
FROM source_feedback WHERE id = $1 FOR UPDATE`, id).Scan(&d.ExternalAlertRef, &d.Payload, &status, &reference); err != nil {
		return err
	}
	var receipt feedbackReceipt
	var err error
	switch status {
	case "prepared":
		receipt, err = sender.Send(ctx, d)
	case "submitted":
		reader, ok := sender.(feedbackReceiptReader)
		if !ok {
			return nil
		}
		var previous string
		if reference != nil {
			previous = *reference
		}
		receipt, err = reader.Receipt(ctx, d, previous)
	default:
		return nil
	}
	if err != nil {
		return problem(http.StatusBadGateway, "Feedback delivery could not be confirmed. Retry to check its status.")
	}
	if receipt.Status != "" && receipt.Status != "accepted" && receipt.Status != "rejected" {
		return problem(http.StatusBadGateway, "The feedback provider returned an unknown status.")
	}
	status = receipt.Status
	if status == "" {
		status = "submitted"
	}
	if len([]rune(receipt.Reference)) > 500 {
		return problem(http.StatusBadGateway, "The feedback provider returned an invalid receipt.")
	}
	_, err = tx.Exec(ctx, `WITH t AS (SELECT clock_timestamp() AS at), updated AS (
    UPDATE source_feedback SET status = $2, submitted_at = COALESCE(submitted_at, t.at),
        decided_at = CASE WHEN $2 IN ('accepted', 'rejected') THEN t.at END,
        provider_receipt = COALESCE(NULLIF($3, ''), provider_receipt)
    FROM t WHERE id = $1 AND (status <> $2 OR provider_receipt IS DISTINCT FROM COALESCE(NULLIF($3, ''), provider_receipt))
    RETURNING alert_id, status
)
INSERT INTO audit_log(workspace_id, actor_kind, action, target_type, target_id, details)
SELECT a.workspace_id, 'system', 'feedback.' || f.status, 'source_feedback', $1::uuid::text, jsonb_build_object('alertId', a.id)
FROM updated f JOIN alerts a ON a.id = f.alert_id`, id, status, receipt.Reference)
	return err
}
