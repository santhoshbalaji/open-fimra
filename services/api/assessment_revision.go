package main

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/santhoshbalaji/open-fimra/packages/starter-pack"
)

// Both reports are on the same alert, so its current grants cover this
// summary as well as the complete report, evidence and review decision.
const provisionalAssessmentJSON = `(
    SELECT jsonb_build_object('id', p.id, 'areaHa', p.answers->'area_ha',
        'measurement', p.answers->'measurement', 'method', p.answers->'method',
        'uncertainty', p.answers->'uncertainty', 'decision', COALESCE(ap.decision, 'pending'))
    FROM submissions p LEFT JOIN approvals ap ON ap.submission_id = p.id
    WHERE p.id = s.provisional_submission_id
)`

func checkAssessment(ctx context.Context, tx pgx.Tx, in submissionRequest, alert, formID int64, answers map[string]any) error {
	if in.Form != starterpack.DamageAssessment {
		if in.ProvisionalSubmissionID != nil {
			return problem(http.StatusUnprocessableEntity, "Only a damage assessment can revise a provisional assessment.")
		}
		return nil
	}
	total, measured := number(answers, "area_ha")
	if area, ok := number(answers, "alert_area_ha"); ok && (!measured || area > total) {
		return problem(http.StatusUnprocessableEntity, "The area attributable to this alert cannot exceed the total burnt area.")
	}
	if in.ProvisionalSubmissionID == nil {
		return nil
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
    SELECT FROM submissions s JOIN form_versions v ON v.id = s.form_version_id
    WHERE s.id = $1 AND s.alert_id = $2 AND v.form_id = $3 AND s.provisional_submission_id IS NULL
)`, *in.ProvisionalSubmissionID, alert, formID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return problem(http.StatusUnprocessableEntity, "Choose a provisional damage assessment for this alert.")
	}
	return nil
}
