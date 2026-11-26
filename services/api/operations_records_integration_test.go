package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestOperationsRecordsIntegration(t *testing.T) {
	ctx, pool := notificationTestDB(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "SET LOCAL jit=off"); err != nil {
		t.Fatal(err)
	}
	_, slug, prefix := loadSeedCopy(t, ctx, tx)
	workspace := prefix + "01"
	store, err := newLocalAttachments(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := api{reader: tx, logger: slog.Default()}
	withAttachments(store, defaultAttachmentBytes)(&a)
	request := notificationRequests(t, a.handler, prefix, "")
	base := "/api/v1/workspaces/" + slug
	const records = "/operations-records"
	call := func(user, method, path string, in any, status int) string {
		t.Helper()
		var raw string
		if in != nil {
			body, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			raw = string(body)
		}
		w := request(user, method, base+path, raw)
		if w.Code != status {
			t.Fatalf("%s %s as %s: HTTP %d, want %d: %s", method, path, user, w.Code, status, w.Body)
		}
		return w.Body.String()
	}
	excludes := func(body string, forbidden ...string) {
		t.Helper()
		for _, term := range forbidden {
			if strings.Contains(body, term) {
				t.Fatalf("restricted value %q in %s", term, body)
			}
		}
	}
	includes := func(body string, expected string) {
		t.Helper()
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q in %s", expected, body)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	makeAlert := func(suffix string, hours int) (incident, alert string) {
		t.Helper()
		lon, lat := 86.295, 16.420
		req := intakeRequest{ID: prefix + suffix, Source: "radio",
			ObservedAt: now.Add(-time.Duration(hours) * time.Hour), Longitude: &lon, Latitude: &lat}
		var receipt struct{ IncidentID, AlertID string }
		if err := json.Unmarshal([]byte(call("a2", "POST", "/fire-intake", req, 201)), &receipt); err != nil {
			t.Fatal(err)
		}
		return receipt.IncidentID, receipt.AlertID
	}
	caseID, alert := makeAlert("d1", 4)
	_, burnAlert := makeAlert("d2", 52)
	if _, err := tx.Exec(ctx, `UPDATE role_assignments SET unit_path = (SELECT unit_path FROM alerts WHERE id = $1::text::bigint)
WHERE workspace_id = $2 AND role_slug = 'field_staff'`, alert, workspace); err != nil {
		t.Fatal(err)
	}
	// Close the real case workflow before following up on its separate referral.
	for _, step := range []string{"acknowledged", "team_sent", "reached_site", "controlled", "extinguished",
		"first_report", "final_report", "verified", "closed"} {
		in := stepRequest{Step: step}
		switch step {
		case "reached_site", "controlled", "extinguished":
			in.Reason = "Confirmed by the fictional field team."
		}
		call("a2", "POST", "/incidents/"+caseID+"/steps", in, 200)
	}
	includes(call("a2", "GET", "/incidents/"+caseID+"/history", nil, 200), `"step":"closed"`)
	call("a1", "POST", "/starter-pack", nil, 200)
	call("a1", "PUT", "/forms/offence-referral/assignments/first_report", map[string]int{"version": 1}, 422)
	report := call("a2", "GET", "/situation-report?hours=24", nil, 200)
	before := readReport(t, []byte(report))

	unitAt := func(path string) string {
		t.Helper()
		var unit string
		if err := tx.QueryRow(ctx, `SELECT u.id::text FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
WHERE h.workspace_id = $1 AND u.current_path = $2::ltree`, workspace, path).Scan(&unit); err != nil {
			t.Fatal(err)
		}
		return unit
	}
	division := unitAt("ds.c1.d1")
	var beat string
	if err := tx.QueryRow(ctx, `SELECT unit_path::text FROM alerts WHERE id = $1::text::bigint`, alert).Scan(&beat); err != nil {
		t.Fatal(err)
	}
	beatUnit := unitAt(beat)
	userID := func(suffix string) string {
		t.Helper()
		var id string
		if err := tx.QueryRow(ctx, "SELECT id::text FROM users WHERE oidc_subject = $1", prefix+suffix).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	grant := map[string]string{"kind": "offence", "unit": division, "user": userID("a2")}
	call("a2", "PUT", "/referral-grants", grant, 403)
	call("a1", "PUT", "/referral-grants", grant, 204)
	call("a1", "PUT", "/referral-grants", map[string]string{"kind": "relief", "unit": division, "user": userID("a1")}, 204)
	includes(call("a2", "GET", records+"/options?unit="+division, nil, 200), "offence-referral")
	excludes(call("a2", "GET", records+"/options?unit="+division, nil, 200), "relief-referral")
	call("a4", "GET", records, nil, 403)

	referral, entry, followup := prefix+"e1", prefix+"e2", prefix+"e3"
	referralPath := records + "/" + referral
	rec := operationsRequest{ID: referral, Kind: "offence", Unit: division, IncidentID: caseID}
	call("a1", "POST", records, rec, 403)
	call("a2", "POST", records, rec, 201)
	call("a2", "POST", records, rec, 200)
	call("a2", "POST", records, operationsRequest{ID: prefix + "ac", Kind: "offence", Unit: unitAt("ds.c2"), IncidentID: caseID}, 404)
	call("a3", "GET", referralPath, nil, 404)
	const ref = "DEMO-REGISTER-PRIVATE"
	const public = "An enquiry is reviewing the field notes."
	makeEntry := func(user, path, id, form string, values map[string]any, completes bool, status int) string {
		return call(user, "POST", path+"/submissions", map[string]any{"id": id, "form": form, "version": 1,
			"values": values, "completesRecord": completes}, status)
	}
	values := map[string]any{"case_reference": "DEMO-CASE-32", "evidence_reference": ref, "public_summary": public}
	values["public_summary"] = "See " + ref
	makeEntry("a2", referralPath, entry, "offence-referral", values, false, 422)
	values["public_summary"] = public
	includes(makeEntry("a2", referralPath, entry, "offence-referral", values, false, 201), ref)
	makeEntry("a2", referralPath, entry, "offence-referral", values, false, 200)
	excludes(call("a3", "GET", records+"?incident="+caseID, nil, 200), referral, public)
	call("a3", "GET", "/submissions/"+entry, nil, 404)
	call("a1", "POST", "/submissions/"+entry+"/approval", map[string]string{"decision": "approved"}, 404)
	includes(call("a2", "GET", "/submissions/"+entry, nil, 200), ref)
	call("a2", "POST", "/submissions/"+entry+"/approval", map[string]string{"decision": "approved"}, 200)
	readSummary := func(user string) {
		t.Helper()
		for _, path := range []string{referralPath, records + "?incident=" + caseID} {
			body := call(user, "GET", path, nil, 200)
			includes(body, public)
			excludes(body, ref, entry, `"owner"`, `"entries"`, `"completed"`, `"canWrite"`, `"unitPath"`)
		}
	}
	readSummary("a3")
	readSummary("a1")

	values["public_summary"] = "Additional notes are awaiting review."
	makeEntry("a2", referralPath, followup, "offence-referral", values, false, 201)
	readSummary("a3")
	call("a2", "POST", "/submissions/"+followup+"/approval", map[string]string{"decision": "returned", "note": "Check the summary."}, 200)
	readSummary("a3")

	reliefID, reliefEntry := prefix+"e4", prefix+"e5"
	call("a1", "POST", records, operationsRequest{ID: reliefID, Kind: "relief", Unit: division, IncidentID: caseID}, 201)
	const beneficiary = "DEMO-BENEFICIARY-73"
	const payment = "DEMO-PAYMENT-88"
	makeEntry("a1", records+"/"+reliefID, reliefEntry, "relief-referral", map[string]any{
		"case_reference": "DEMO-RELIEF-91", "beneficiary_reference": beneficiary, "payment_reference": payment,
		"public_summary": "The fictional relief office received the referral.",
	}, false, 201)
	call("a1", "POST", "/submissions/"+reliefEntry+"/approval", map[string]string{"decision": "approved"}, 200)
	call("a2", "GET", "/submissions/"+reliefEntry, nil, 404)
	excludes(call("a2", "GET", records+"/"+reliefID, nil, 200), beneficiary, payment, reliefEntry)
	call("a1", "DELETE", "/referral-grants", grant, 204)
	readSummary("a2")
	call("a2", "GET", "/submissions/"+entry, nil, 404)
	var audited bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
    SELECT FROM audit_log WHERE workspace_id = $1 AND actor_id = $2 AND action = 'referral.read'
      AND target_id = $3 AND details->>'submissionId' = $4
) AND NOT EXISTS (SELECT FROM audit_log WHERE workspace_id = $1 AND action = 'referral.read' AND actor_id = $5)`,
		workspace, prefix+"a2", referral, entry, prefix+"a3").Scan(&audited); err != nil || !audited {
		t.Fatalf("restricted reads need an audit entry and summary reads should leave none: %v", err)
	}

	excludes(call("a1", "GET", "/alerts/"+alert+"/submissions", nil, 200), ref, beneficiary, payment, public, entry)
	excludes(call("a1", "GET", "/submissions?alert="+alert, nil, 200), ref, beneficiary, payment, public, entry)
	after := readReport(t, []byte(call("a2", "GET", "/situation-report?hours=24", nil, 200)))
	if string(before.FieldReports) != string(after.FieldReports) {
		t.Fatalf("referrals changed field reports: %s → %s", before.FieldReports, after.FieldReports)
	}
	for _, form := range []string{"", "offence-referral", "relief-referral"} {
		var allowed, known bool
		var total int
		var exportID pgtype.UUID
		var filters json.RawMessage
		var frozen int64
		err := tx.QueryRow(ctx, exportsSQL, prefix+"a1", slug, "submissions", "geojson", 24,
			[]string{"inside", "near_edge", "fixed_heat_source"}, "", nil, "", maxExportRows,
			pgtype.Interval{Microseconds: time.Hour.Microseconds(), Valid: true}, form, "", "").
			Scan(&allowed, &known, &total, &exportID, &filters, &frozen)
		if err != nil || !allowed || !known || !exportID.Valid || total != 0 || frozen != 0 {
			t.Fatalf("referral became an export row for filter %q: %v %v %d %d (%v)", form, allowed, known, total, frozen, err)
		}
	}

	burn, authID, finishID := prefix+"e6", prefix+"e7", prefix+"e8"
	burnPath := records + "/" + burn
	call("a2", "POST", records, operationsRequest{ID: burn, Kind: "burn", Unit: division}, 201)
	burnLink := burnPath + "/alerts/" + burnAlert
	call("a2", "PUT", burnLink, nil, 409)
	makeEntry("a2", burnPath, authID, "prescribed-burn-authorisation", map[string]any{
		"authorisation_reference": "DEMO-BURN-9", "site": "Fictional plot", "window_start": now.Add(-72 * time.Hour).Format(time.DateOnly),
		"window_end": now.Format(time.DateOnly),
	}, false, 201)
	call("a2", "POST", "/submissions/"+authID+"/approval", map[string]string{"decision": "approved"}, 200)
	call("a3", "PUT", burnLink, nil, 404)
	call("a2", "PUT", burnLink, nil, 204)
	call("a2", "PUT", burnLink, nil, 204)
	includes(call("a2", "GET", burnPath, nil, 200), fmt.Sprintf(`"alerts":["%s"]`, burnAlert))
	call("a2", "POST", "/alerts/"+burnAlert+"/steps", stepRequest{Step: "controlled_burn"}, 200)
	makeEntry("a2", burnPath, finishID, "prescribed-burn-completion", map[string]any{
		"finished_on": now.Format(time.DateOnly), "result": "Fictional plot was checked."}, true, 201)
	call("a2", "POST", "/submissions/"+finishID+"/approval", map[string]string{"decision": "approved"}, 200)
	includes(call("a2", "GET", burnPath, nil, 200), `"completed":true`)

	action, completion := prefix+"e9", prefix+"ea"
	actionPath := records + "/" + action
	call("a2", "POST", records, operationsRequest{ID: action, Kind: "after-action", Unit: beatUnit, Owner: userID("a3")}, 201)
	includes(call("a2", "GET", actionPath, nil, 200), "Demo Beat Guard")
	makeEntry("a3", actionPath, completion, "after-action-finding", map[string]any{
		"finding": "The drill identified a missing marker.", "action": "Mark the cabinet.",
		"target_date": now.Format(time.DateOnly), "result": "The fictional cabinet was marked.",
	}, true, 201)
	call("a3", "POST", "/submissions/"+completion+"/approval", map[string]string{"decision": "approved"}, 403)
	includes(call("a3", "GET", actionPath, nil, 200), `"completionPending":true`)
	call("a2", "POST", "/submissions/"+completion+"/approval", map[string]string{"decision": "approved"}, 200)
	includes(call("a3", "GET", actionPath, nil, 200), `"completed":true`)

	// A record file shares the same store and UUID namespace as alert files.
	file := prefix + "eb"
	upload := func(record, subject string, want int) {
		t.Helper()
		r := httptest.NewRequest("PUT", "/?name=fictional-notes.txt", strings.NewReader("Fictional drill notes."))
		r.Header.Set("Content-Type", "text/plain")
		route := chi.NewRouteContext()
		route.URLParams.Add("slug", slug)
		route.URLParams.Add("record", record)
		route.URLParams.Add("attachment", file)
		r = r.WithContext(context.WithValue(context.WithValue(r.Context(), chi.RouteCtxKey, route),
			identityKey{}, identity{Subject: prefix + subject}))
		w := httptest.NewRecorder()
		a.uploadAttachment(w, r)
		if w.Code != want {
			t.Fatalf("upload for %s: %d want %d: %s", record, w.Code, want, w.Body)
		}
	}
	upload(reliefID, "a1", 403)
	upload(action, "a3", 201)
	upload(action, "a3", 200)
	call("a3", "GET", actionPath+"/attachments/"+file, nil, 200)
	call("a3", "GET", "/alerts/"+alert+"/attachments/"+file, nil, 404)
	includes(call("a2", "GET", "/attachments/"+file, nil, 200), "Fictional drill notes.")
}
