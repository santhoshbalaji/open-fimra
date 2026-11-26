package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/santhoshbalaji/open-fimra/packages/starter-pack"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

const responseDemoSource = "demo_fsi"

type responseDemoAlert struct {
	id, incident int64
	ref          string
}

func ingestResponseDemoAlerts(ctx context.Context, tx pgx.Tx, workspace, referencePrefix string, observed time.Time) ([]responseDemoAlert, error) {
	run := ingestRun{Source: responseDemoSource, Product: demoProduct, Kind: runImport, WorkspaceID: &workspace}
	_, err := runIngest(ctx, tx, run, func(context.Context) ([]firms.Observation, error) {
		return []firms.Observation{
			{Satellite: "N", Instrument: "VIIRS", Longitude: 86.297475, Latitude: 16.418669, ObservedAt: observed,
				Raw: map[string]string{"externalAlertRef": referencePrefix + "1"}},
			{Satellite: "N", Instrument: "VIIRS", Longitude: 86.298475, Latitude: 16.418869, ObservedAt: observed.Add(10 * time.Minute),
				Raw: map[string]string{"externalAlertRef": referencePrefix + "2"}},
		}, nil
	})
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT a.id, m.incident_id, d.raw->>'externalAlertRef'
FROM alerts a JOIN incident_members m ON m.alert_id = a.id
JOIN detections d ON (d.id, d.observed_at) = (a.detection_id, a.observed_at)
WHERE a.workspace_id = $1 AND d.source = $2 AND d.raw->>'externalAlertRef' IN ($3, $4)
ORDER BY a.observed_at`, workspace, run.Source, referencePrefix+"1", referencePrefix+"2")
	if err != nil {
		return nil, err
	}
	alerts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (responseDemoAlert, error) {
		var alert responseDemoAlert
		err := row.Scan(&alert.id, &alert.incident, &alert.ref)
		return alert, err
	})
	if err != nil {
		return nil, err
	}
	if len(alerts) != 2 || alerts[0].incident != alerts[1].incident {
		return nil, fmt.Errorf("the two synthetic alerts must belong to the same case")
	}
	return alerts, nil
}

// The demo starts with crews back from the field. Seed the completed steps
// so the example opens at the reports and review stage.
func seedResponseReportHistory(ctx context.Context, tx pgx.Tx, id int64, actor, target string) error {
	steps := strings.Fields("acknowledged assigned team_sent reached_site flash_update controlled extinguished first_report final_report")
	if target == "flash_update" {
		steps = steps[:5]
	}
	_, err := tx.Exec(ctx, `WITH entries AS (
    SELECT i.alert_id, i.template_id, COALESCE(lag(s.step) OVER (ORDER BY s.n), 'new') AS previous,
           s.step, i.entered_at + s.n * interval '1 second' AS at
    FROM workflow_instances i CROSS JOIN unnest($3::text[]) WITH ORDINALITY s(step,n)
    WHERE i.alert_id = $1
), logged AS (
    INSERT INTO step_log(alert_id, template_id, from_step, to_step, user_id, occurred_at, report_pending)
    SELECT e.alert_id, e.template_id, e.previous, e.step, $2, e.at, w.report_pending
    FROM entries e JOIN workflow_steps w ON (w.template_id,w.slug) = (e.template_id,e.step)
    ORDER BY e.at RETURNING alert_id, to_step, occurred_at
)
UPDATE workflow_instances i SET current_step = l.to_step, entered_at = l.occurred_at
FROM logged l WHERE i.alert_id = l.alert_id AND l.to_step = $4`, id, actor, steps, target)
	return err
}

func responseFormsDemo(ctx context.Context, cfg config) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	pool, err := openPool(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := ensureDemoSeed(ctx, pool); err != nil {
		return err
	}

	const slug, subject = "demo", "d0000000-0000-4000-8000-0000000000a1"
	const prefix = "DEMO-FSI-RIDGE-"
	var incident int64
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, slug)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, detectionWriteLock); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, workflowFormsLock, access.workspaceID); err != nil {
			return err
		}
		var loaded bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM source_feedback f JOIN alerts a ON a.id=f.alert_id
WHERE a.workspace_id=$1 AND f.external_alert_ref=$2)`, access.workspaceID, prefix+"1").Scan(&loaded); err != nil || loaded {
			return err
		}
		if _, err := selectResponseWorkflow(ctx, tx, access, subject, true); err != nil {
			return err
		}
		if _, err := enableStarterPack(ctx, tx, access, subject); err != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Minute)
		alerts, err := ingestResponseDemoAlerts(ctx, tx, access.workspaceID, prefix, now.Add(-35*time.Hour))
		if err != nil {
			return err
		}
		incident = alerts[0].incident
		if err := seedResponseReportHistory(ctx, tx, alerts[0].id, access.userID, "final_report"); err != nil {
			return err
		}
		if err := seedResponseReportHistory(ctx, tx, alerts[1].id, access.userID, "flash_update"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE workflow_instances i SET current_step='final_report', entered_at=a.entered_at
FROM workflow_instances a WHERE i.incident_id=$1 AND a.alert_id=$2`, incident, alerts[0].id); err != nil {
			return err
		}
		return saveResponseDemoReports(ctx, tx, access, subject, alerts, now.Add(-30*time.Hour))
	})
	if err != nil {
		return err
	}
	if incident == 0 {
		fmt.Println("The synthetic response reports are already loaded.")
	} else {
		fmt.Printf("Incident #%d has two source feedback receipts and both damage assessments.\n", incident)
	}
	return nil
}

func saveResponseDemoReports(ctx context.Context, tx pgx.Tx, access workspaceAccess, subject string, alerts []responseDemoAlert, assessed time.Time) error {
	var a api
	save := func(alert int64, form, step string, values map[string]any, parent *string, decision, note string) (string, error) {
		in := submissionRequest{Form: form, Version: 1, Step: step, ProvisionalSubmissionID: parent}
		if err := tx.QueryRow(ctx, "SELECT gen_random_uuid()").Scan(&in.ID); err != nil {
			return "", err
		}
		in.Values, _ = json.Marshal(values)
		if _, err := a.recordSubmission(ctx, tx, access.userID, alert, in, fmt.Sprintf("%x", sha256.Sum256(in.Values))); err != nil {
			return "", err
		}
		_, err := tx.Exec(ctx, `INSERT INTO approvals (submission_id, decision, note, user_id) VALUES ($1,$2,$3,$4)`,
			in.ID, decision, note, access.userID)
		return in.ID, err
	}
	provisional, err := save(alerts[0].id, starterpack.DamageAssessment, "final_report", map[string]any{
		"assessed_at": assessed, "measurement": "estimated", "area_ha": 8.4,
		"method": "Estimated from the track", "uncertainty": "The far edge was hidden by smoke",
	}, nil, "returned", "Walk the far edge to check the estimate.")
	if err != nil {
		return err
	}
	revised, err := save(alerts[0].id, starterpack.DamageAssessment, "final_report", map[string]any{
		"assessed_at": assessed.Add(time.Hour), "measurement": "surveyed", "area_ha": 6.2,
		"method": "Walked the perimeter", "uncertainty": "The path was clear on the second visit",
		"alert_area_ha": 3.4, "area_basis": "Only the burnt strip beside this alert was linked to the reading",
		"vegetation": "Grass and dry leaves burnt", "property": "No damage to buildings seen",
	}, &provisional, "approved", "")
	if err != nil {
		return err
	}
	for i, alert := range alerts {
		values := map[string]any{
			"checked_at": assessed, "fire_seen": "no", "fire_status": "No fire found at the second point",
			"site_location": map[string]any{"type": "Point", "coordinates": []float64{86.297475 + float64(i)*0.001, 16.418669}},
			"remarks":       "The crew checked this alert on foot.",
		}
		var parent *string
		if i == 0 {
			values["fire_seen"], values["fire_status"] = "yes", "Surface fire on the ridge"
			values["land_class"], values["fire_type"], values["fire_cause"] = "forest", "surface", "unknown"
			parent = &revised
		}
		verified, err := save(alert.id, starterpack.ResponseFlash, "flash_update", values, nil, "approved", "")
		if err != nil {
			return err
		}
		id, source, err := a.prepareSourceFeedback(ctx, tx, alert.id, subject, feedbackRequest{VerificationID: verified, AssessmentID: parent})
		if err != nil {
			return err
		}
		if err := deliverSourceFeedback(ctx, tx, id, a.feedbackSenderFor(source)); err != nil {
			return err
		}
	}
	return nil
}
