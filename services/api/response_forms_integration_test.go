package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/santhoshbalaji/open-fimra/packages/starter-pack"
)

func TestResponseFormsIntegration(t *testing.T) {
	ctx, pool := notificationTestDB(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	suffix, slug, prefix := loadSeedCopy(t, ctx, tx)
	admin, err := formAccess(ctx, tx, prefix+"a1", slug)
	if err != nil {
		t.Fatal(err)
	}
	setGuardBeat := func(path string) {
		t.Helper()
		if _, err := tx.Exec(ctx, `UPDATE role_assignments SET unit_path=$2
WHERE workspace_id=$1 AND role_slug='field_staff'`, admin.workspaceID, path); err != nil {
			t.Fatal(err)
		}
	}
	setGuardBeat("ds.c1.d1.r2.b05")
	caller := api{reader: tx, logger: slog.Default()}
	request := notificationRequests(t, caller.handler, prefix, "")
	call := func(user, method, path string, body any, status int, result any) {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		response := request(user, method, "/api/v1/workspaces/"+slug+path, string(payload))
		if response.Code != status {
			t.Fatalf("%s %s as %s: HTTP %d, want %d: %s", method, path, user, response.Code, status, response.Body)
		}
		if result != nil && json.Unmarshal(response.Body.Bytes(), result) != nil {
			t.Fatalf("invalid response from %s: %s", path, response.Body)
		}
	}
	call("a1", "POST", "/starter-pack", nil, 200, nil)
	call("a1", "PUT", "/settings/incident-response", responseSettings{Extended: true, Targets: []responseTarget{}}, 200, nil)
	var pack packState
	call("a1", "GET", "/starter-pack", nil, 200, &pack)
	if !pack.Enabled || len(pack.Forms) < 10 {
		t.Fatalf("the extended pack should include the response forms: %+v", pack)
	}
	for _, form := range pack.Forms {
		if len(form.Assigned) != len(form.Steps) {
			t.Errorf("%s assigned to %v, want %v", form.Form, form.Assigned, form.Steps)
		}
	}

	now := time.Now().UTC().Truncate(time.Minute)
	alerts, err := ingestResponseDemoAlerts(ctx, tx, admin.workspaceID, "DEMO-FSI-TEST-"+suffix+"-", now.Add(-8*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	first, second := fmt.Sprintf("/alerts/%d", alerts[0].id), fmt.Sprintf("/alerts/%d", alerts[1].id)
	for _, alert := range alerts {
		if err := seedResponseReportHistory(ctx, tx, alert.id, admin.userID, "final_report"); err != nil {
			t.Fatal(err)
		}
	}

	provisional := "5ca13200-0000-4000-8000-000000000001"
	revision := "5ca13200-0000-4000-8000-000000000002"
	const firstPhoto = "5ca13200-0000-4000-8000-000000000011"
	const secondPhoto = "5ca13200-0000-4000-8000-000000000012"
	const verification = "5ca13200-0000-4000-8000-000000000003"
	const notSeen = "5ca13200-0000-4000-8000-000000000004"

	if _, err := tx.Exec(ctx, `INSERT INTO attachments (id, workspace_id, alert_id, sha256, size_bytes, content_type, file_name, uploaded_by)
SELECT unnest($1::uuid[]), $2, $3, repeat('a',64), 100, 'image/png', 'synthetic.png', $4`,
		[]string{firstPhoto, secondPhoto}, admin.workspaceID, alerts[0].id, admin.userID); err != nil {
		t.Fatal(err)
	}
	report := func(id, form, step string, values map[string]any, parent *string) submissionRequest {
		raw, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		return submissionRequest{ID: id, Form: form, Version: 1, Step: step, Values: raw, ProvisionalSubmissionID: parent}
	}
	decision := func(user, id, value, note string, status int) {
		t.Helper()
		call(user, "POST", "/submissions/"+id+"/approval", approvalRequest{Decision: value, Note: note}, status, nil)
	}

	values := map[string]any{"assessed_at": now, "measurement": "estimated", "area_ha": 10.5,
		"method": "Estimated from the track", "uncertainty": "Far edge hidden", "area_photo": []string{firstPhoto}}
	call("a3", "POST", first+"/submissions", report(provisional, starterpack.DamageAssessment, "final_report", values, nil), 201, nil)
	decision("a3", provisional, "returned", "Walk the perimeter.", 403)
	decision("a2", provisional, "returned", "Walk the perimeter.", 200)
	call("a4", "GET", "/submissions/"+provisional, nil, 404, nil)

	values["measurement"], values["area_ha"], values["method"] = "surveyed", 7.25, "Walked the perimeter"
	values["area_photo"] = []string{secondPhoto}
	values["alert_area_ha"], values["area_basis"] = 7.26, "Burnt strip nearest the first alert"
	call("a3", "POST", first+"/submissions", report(revision, starterpack.DamageAssessment, "final_report", values, &provisional), 422, nil)
	values["alert_area_ha"] = 2.5
	delete(values, "area_photo")
	call("a2", "POST", second+"/submissions", report(revision, starterpack.DamageAssessment, "final_report", values, &provisional), 422, nil)
	values["area_photo"] = []string{secondPhoto}
	revised := report(revision, starterpack.DamageAssessment, "final_report", values, &provisional)
	call("a3", "POST", first+"/submissions", revised, 201, nil)
	call("a3", "POST", first+"/submissions", revised, 200, nil)
	decision("a2", revision, "approved", "", 200)

	var receipt struct {
		Provisional *struct {
			ID, Decision string
			AreaHa       float64
		} `json:"provisionalAssessment"`
		Values   map[string]any
		Approval struct{ Decision string }
	}
	for _, user := range []string{"a2", "a3"} {
		call(user, "GET", "/submissions/"+revision, nil, 200, &receipt)
		if receipt.Values["area_ha"] != 7.25 || receipt.Approval.Decision != "approved" ||
			receipt.Provisional == nil || receipt.Provisional.ID != provisional ||
			receipt.Provisional.AreaHa != 10.5 || receipt.Provisional.Decision != "returned" {
			t.Fatalf("the %s revision lost the original figures or decision: %+v", user, receipt)
		}
	}
	call("a4", "GET", "/submissions/"+revision, nil, 404, nil)
	setGuardBeat("ds.c1.d1.r1.b01")
	call("a3", "GET", "/submissions/"+revision, nil, 404, nil)
	call("a3", "GET", "/submissions/"+provisional, nil, 404, nil)
	setGuardBeat("ds.c1.d1.r2.b05")
	call("a3", "GET", "/submissions/"+provisional, nil, 200, &receipt)
	if receipt.Provisional != nil || receipt.Values["area_ha"] != 10.5 || receipt.Approval.Decision != "returned" {
		t.Fatalf("the provisional record changed: %+v", receipt)
	}
	var attachments int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM submission_attachments
WHERE (submission_id=$1 AND attachment_id=$2) OR (submission_id=$3 AND attachment_id=$4)`,
		provisional, firstPhoto, revision, secondPhoto).Scan(&attachments); err != nil || attachments != 2 {
		t.Fatalf("both reports should retain their own evidence: %d (%v)", attachments, err)
	}

	flash := map[string]any{"checked_at": now, "site_location": point(86.297475, 16.418669), "fire_seen": "yes",
		"fire_status": "Surface fire on the ridge", "land_class": "forest", "fire_type": "surface", "fire_cause": "unknown", "remarks": "First point checked."}
	call("a3", "POST", first+"/submissions", report(verification, starterpack.ResponseFlash, "flash_update", flash, nil), 201, nil)
	feedback := feedbackRequest{VerificationID: verification, AssessmentID: &revision}
	call("a2", "POST", first+"/source-feedback", feedback, 422, nil)
	decision("a2", verification, "approved", "", 200)

	flash["fire_seen"], flash["fire_status"], flash["remarks"] = "no", "No fire at this point", "Second point checked."
	call("a3", "POST", second+"/submissions", report(notSeen, starterpack.ResponseFlash, "flash_update", flash, nil), 201, nil)
	decision("a2", notSeen, "approved", "", 200)
	call("a3", "POST", first+"/source-feedback", feedback, 403, nil)
	call("a4", "GET", first+"/source-feedback", nil, 404, nil)
	call("a2", "POST", second+"/source-feedback", feedback, 422, nil)
	call("a2", "POST", second+"/source-feedback", feedbackRequest{VerificationID: notSeen, AssessmentID: &revision}, 422, nil)

	type entry struct {
		ID, ExternalAlertRef, Status, ProviderReceipt string
		Payload                                       feedbackPayload
		SubmittedAt, DecidedAt                        *time.Time
	}
	for i, tc := range []struct {
		path string
		body feedbackRequest
		seen string
	}{{first, feedback, "Yes"}, {second, feedbackRequest{VerificationID: notSeen}, "No"}} {
		var got, retry entry
		call("a2", "POST", tc.path+"/source-feedback", tc.body, 200, &got)
		call("a2", "POST", tc.path+"/source-feedback", tc.body, 200, &retry)
		if got.ID != retry.ID || got.ExternalAlertRef != alerts[i].ref || got.Status != "accepted" ||
			got.SubmittedAt == nil || got.DecidedAt == nil || !strings.HasSuffix(got.ProviderReceipt, alerts[i].ref) ||
			got.Payload.FireSeen != tc.seen {
			t.Fatalf("alert %d must have its own feedback and one acceptance: %+v, retry %+v", i, got, retry)
		}
		if i == 0 && (got.Payload.AreaHa == nil || *got.Payload.AreaHa != 2.5 || got.Payload.LandClass != "Forest Land" ||
			got.Payload.FireType != "Surface Fire" || got.Payload.Cause != "Unknown" || got.Payload.Remarks != "First point checked.") {
			t.Fatalf("the first alert has the wrong feedback: %+v", got.Payload)
		}
		if i == 1 && (got.Payload.AreaHa != nil || got.Payload.LandClass != "" || got.Payload.Remarks != "Second point checked.") {
			t.Fatalf("the second alert inherited details: %+v", got.Payload)
		}
		var state struct {
			CanPrepare bool
			Records    []entry
		}
		call("a3", "GET", tc.path+"/source-feedback", nil, 200, &state)
		if state.CanPrepare || len(state.Records) != 1 || state.Records[0].ID != got.ID {
			t.Fatalf("field staff should see the receipt on their alert: %+v", state)
		}
	}
}
