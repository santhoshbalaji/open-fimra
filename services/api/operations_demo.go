package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	formschema "github.com/santhoshbalaji/open-fimra/packages/form-schema"
	starterpack "github.com/santhoshbalaji/open-fimra/packages/starter-pack"
)

func operationsRecordsDemo(ctx context.Context, cfg config) error {
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
	const prefix = "de330000-0000-4000-8000-0000000000"
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, slug)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SET LOCAL jit=off"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, detectionWriteLock); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, incidentGroupingLock, access.workspaceID); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT FROM operations_records WHERE workspace_id = $1 AND id = $2)",
			access.workspaceID, prefix+"01").Scan(&exists); err != nil || exists {
			return err
		}
		now := time.Now().UTC().Truncate(time.Second)
		lon, lat := 86.295, 16.420
		intake := func(suffix string, observed time.Time, note string) (int64, int64, error) {
			alert, err := saveIntake(ctx, tx, access, subject, slug, intakeRequest{ID: prefix + suffix,
				Source: "staff", ObservedAt: observed, Longitude: &lon, Latitude: &lat, Note: note}, nil, suffix)
			if err != nil {
				return 0, 0, err
			}
			var incident int64
			err = tx.QueryRow(ctx, "SELECT incident_id FROM incident_members WHERE alert_id = $1", alert).Scan(&incident)
			return alert, incident, err
		}
		alert, incident, err := intake("f1", now.Add(-7*24*time.Hour), "Fictional crew investigated smoke by the ridge track.")
		if err != nil {
			return err
		}
		var extended bool
		if err := tx.QueryRow(ctx, "SELECT workflow_template_extended(template_id) FROM workflow_instances WHERE incident_id = $1", incident).Scan(&extended); err != nil {
			return err
		}
		steps := []stepRequest{{Step: "acknowledged"}}
		if extended {
			steps = append(steps, stepRequest{Step: "assigned", Note: "Demo ridge crew"})
		}
		dispatched := stepRequest{Step: "team_sent"}
		if extended {
			eta := now.Add(time.Hour)
			dispatched.EstimatedAt = &eta
		}
		steps = append(steps, dispatched, stepRequest{Step: "reached_site", Reason: "Fictional field confirmation."})
		if extended {
			steps = append(steps, stepRequest{Step: "flash_update", Response: &responseNote{Status: "Fictional crew is on site."}})
		}
		steps = append(steps, stepRequest{Step: "controlled", Reason: "Fictional field confirmation."},
			stepRequest{Step: "extinguished", Reason: "Fictional field confirmation."})
		if extended {
			steps = append(steps, stepRequest{Step: "watch"})
		}
		for _, step := range []string{"first_report", "final_report", "verified", "closed"} {
			steps = append(steps, stepRequest{Step: step})
		}
		for _, step := range steps {
			var result json.RawMessage
			if err := recordIncidentStep(ctx, tx, subject, slug, incident, step, &result); err != nil {
				return fmt.Errorf("close the fictional case at %s: %w", step.Step, err)
			}
		}
		burnAlert, _, err := intake("f2", now.Add(-72*time.Hour), "Fictional authorised burn near the ridge track.")
		if err != nil {
			return err
		}
		if err := publishOperationsDemoForms(ctx, tx, access, subject); err != nil {
			return err
		}
		var hierarchy, path, owner string
		err = tx.QueryRow(ctx, `SELECT g.hierarchy_id::text, g.unit_path::text, g.user_id::text
FROM permanent_alert_grants g JOIN alerts a ON a.workspace_id = g.workspace_id
WHERE g.slug = $1 AND g.role_slug = 'division_officer' AND a.id = $2
  AND match_under_grant(a.hierarchy_unit_paths, g.hierarchy_id, g.unit_path, g.depth)
ORDER BY g.depth DESC LIMIT 1`, slug, alert).Scan(&hierarchy, &path, &owner)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO referral_grants(workspace_id, hierarchy_id, unit_path, user_id, kind)
SELECT $1, $2, $3::ltree, user_id, 'offence' FROM unnest(ARRAY[$4::uuid, $5::uuid]) AS user_id
ON CONFLICT DO NOTHING`, access.workspaceID, hierarchy, path, access.userID, owner); err != nil {
			return err
		}
		makeRecord := func(id, kind string, linked *int64) error {
			_, err := tx.Exec(ctx, `INSERT INTO operations_records(id, workspace_id, hierarchy_id, unit_path, kind, incident_id, owner_id)
VALUES ($1, $2, $3, $4::ltree, $5, $6, $7)`, id, access.workspaceID, hierarchy, path, kind, linked, owner)
			return err
		}
		add := func(id, kind, form, entry string, values map[string]string) error {
			raw, err := json.Marshal(values)
			if err != nil {
				return err
			}
			err = storeOperationsSubmission(ctx, tx, operationsAccess{workspace: access.workspaceID, user: access.userID, kind: kind},
				id, operationsSubmissionRequest{submissionRequest: submissionRequest{ID: entry, Form: form, Version: 1, Values: raw}},
				fmt.Sprintf("%064x", len(entry)))
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "INSERT INTO approvals(submission_id, decision, user_id) VALUES ($1, 'approved', $2)", entry, access.userID)
			return err
		}
		if err := makeRecord(prefix+"01", "offence", &incident); err != nil {
			return err
		}
		if err := add(prefix+"01", "offence", "offence-referral", prefix+"11", map[string]string{
			"case_reference": "DEMO-ENQUIRY-17", "evidence_reference": "DEMO-REGISTER-6",
			"public_summary": "The case is closed. A separate enquiry is reviewing the fictional field notes.",
		}); err != nil {
			return err
		}
		if err := makeRecord(prefix+"02", "burn", nil); err != nil {
			return err
		}
		if err := add(prefix+"02", "burn", "prescribed-burn-authorisation", prefix+"12", map[string]string{
			"authorisation_reference": "DEMO-BURN-04", "site": "Fictional ridge training plot",
			"window_start": now.Add(-96 * time.Hour).Format(time.DateOnly), "window_end": now.Add(-48 * time.Hour).Format(time.DateOnly),
		}); err != nil {
			return err
		}
		if err := linkBurnAlert(ctx, tx, subject, slug, access.workspaceID, prefix+"02", burnAlert); err != nil {
			return err
		}
		var receipt json.RawMessage
		if err := recordStep(ctx, tx, subject, slug, burnAlert, stepRequest{Step: "controlled_burn"}, &receipt); err != nil {
			return err
		}
		if err := makeRecord(prefix+"03", "after-action", nil); err != nil {
			return err
		}
		if err := add(prefix+"03", "after-action", "after-action-finding", prefix+"13", map[string]string{
			"finding": "The drill crew could not find the replacement battery.",
			"action":  "Label the battery box and verify it during the next check.", "target_date": now.Add(168 * time.Hour).Format(time.DateOnly),
		}); err != nil {
			return err
		}
		fmt.Println("Fictional referral on closed case #" + strconv.FormatInt(incident, 10) +
			", authorised burn linked to alert #" + strconv.FormatInt(burnAlert, 10) + ", and an assigned after-action finding.")
		return nil
	})
}

// Publish the demo's standalone forms without adding workflow gates to the
// alert forms that other demos and integration tests use.
func publishOperationsDemoForms(ctx context.Context, tx pgx.Tx, access workspaceAccess, subject string) error {
	if _, err := tx.Exec(ctx, workflowFormsLock, access.workspaceID); err != nil {
		return err
	}
	for _, form := range starterpack.Forms() {
		var needed bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM operations_forms WHERE form_key = $1)
    AND NOT EXISTS (SELECT FROM forms WHERE workspace_id = $2 AND form_key = $1)`, form.Key, access.workspaceID).Scan(&needed)
		if err != nil {
			return err
		}
		if !needed {
			continue
		}
		columns, problems := formschema.Columns(form.Source)
		if len(problems) != 0 {
			return fmt.Errorf("invalid starter form %s: %v", form.Key, problems)
		}
		tree, err := parseFormTree(form.Source)
		if err != nil {
			return err
		}
		upload := &formUpload{source: form.Source, columns: columns, tree: tree, key: form.Key, version: form.Version, level: "state"}
		var result json.RawMessage
		if err := createFormVersion(ctx, tx, subject, access, upload, &result); err != nil {
			return err
		}
		var versionID int64
		if err := tx.QueryRow(ctx, `SELECT v.id FROM form_versions v JOIN forms f ON f.id = v.form_id
WHERE f.workspace_id = $1 AND f.form_key = $2 AND v.version = $3`, access.workspaceID, form.Key, form.Version).Scan(&versionID); err != nil {
			return err
		}
		if err := publishFormVersion(ctx, tx, versionID, string(form.Source), access.userID); err != nil {
			return err
		}
	}
	return nil
}
