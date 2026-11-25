package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The starter pack part of the wizard test: turning the pack on through the
// wizard's endpoints, turning it on again, and taking a synthetic alert from
// New to Closed with each step's form filled in by the right demo role. The
// wizard test calls these in order on its fresh install.

type wizardCall func(user, method, path string, body any, want int, out any)

type starterPackRun struct {
	ctx  context.Context
	pool *pgxpool.Pool
	base string
	call wizardCall
	// send makes a request with a raw body and returns the status and answer.
	send func(user, method, path, contentType string, body []byte) (int, []byte, http.Header)
}

type packAnswer struct {
	Enabled bool `json:"enabled"`
	Forms   []struct {
		Form     string     `json:"form"`
		Status   string     `json:"status"`
		Steps    []string   `json:"steps"`
		Assigned []string   `json:"assigned"`
		Skipped  []packSkip `json:"skipped"`
	} `json:"forms"`
}

func (p starterPackRun) counts(t *testing.T) string {
	t.Helper()
	var forms, versions, assignments, audits int
	if err := p.pool.QueryRow(p.ctx, `SELECT (SELECT count(*) FROM forms), (SELECT count(*) FROM form_versions),
		(SELECT count(*) FROM form_assignments), (SELECT count(*) FROM audit_log WHERE action IN ('form.assign', 'form.publish'))`).
		Scan(&forms, &versions, &assignments, &audits); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%d forms, %d versions, %d assignments, %d audit entries", forms, versions, assignments, audits)
}

// enable turns the pack on through the wizard and checks that turning it on
// again changes nothing, including a form the department has changed.
func (p starterPackRun) enable(t *testing.T) {
	p.call("state.admin", http.MethodPost, p.base+"/setup/steps/forms", map[string]string{"status": "done"}, http.StatusConflict, nil)
	p.call("no.role", http.MethodGet, p.base+"/starter-pack", nil, http.StatusForbidden, nil)
	p.call("no.role", http.MethodPost, p.base+"/starter-pack", nil, http.StatusForbidden, nil)
	var before packAnswer
	p.call("state.admin", http.MethodGet, p.base+"/starter-pack", nil, http.StatusOK, &before)
	if _, raw, _ := p.send("state.admin", http.MethodGet, p.base+"/starter-pack", "", nil); !bytes.Contains(raw, []byte(`"steps":[]`)) ||
		bytes.Contains(raw, []byte("null")) {
		t.Fatalf("a form with no steps lists them as an empty list: %s", raw)
	}
	if before.Enabled || len(before.Forms) != 5 {
		t.Fatalf("a fresh workspace has no starter forms: %+v", before)
	}
	for _, f := range before.Forms {
		if f.Status != "missing" || len(f.Assigned) != 0 {
			t.Fatalf("a fresh workspace has no starter forms: %+v", f)
		}
	}

	var on packAnswer
	p.call("state.admin", http.MethodPost, p.base+"/starter-pack", nil, http.StatusOK, &on)
	want := map[string][]string{
		"control-room-dispatch": {"reached_site", "team_sent"}, "first-report": {"first_report"},
		"final-report": {"final_report"}, "manual-fire-report": {}, "patrol-log": {},
	}
	if !on.Enabled || len(on.Forms) != len(want) {
		t.Fatalf("the starter pack must be on: %+v", on)
	}
	for _, f := range on.Forms {
		if f.Status != "added" || !slices.Equal(f.Assigned, want[f.Form]) || len(f.Skipped) != 0 {
			t.Fatalf("%s: status %s, assigned %v, skipped %v; want added at %v", f.Form, f.Status, f.Assigned, f.Skipped, want[f.Form])
		}
		var version struct {
			Status string `json:"status"`
		}
		p.call("beat.guard", http.MethodGet, p.base+"/forms/"+f.Form+"/versions/1", nil, http.StatusForbidden, nil)
		p.call("state.admin", http.MethodGet, p.base+"/forms/"+f.Form+"/versions/1", nil, http.StatusOK, &version)
		if version.Status != "published" {
			t.Fatalf("%s is %s, want published", f.Form, version.Status)
		}
	}

	// Again: nothing changes.
	after := p.counts(t)
	var again packAnswer
	p.call("state.admin", http.MethodPost, p.base+"/starter-pack", nil, http.StatusOK, &again)
	for _, f := range again.Forms {
		if f.Status != "present" || !slices.Equal(f.Assigned, want[f.Form]) {
			t.Fatalf("turning the pack on again changed %s: %+v", f.Form, f)
		}
	}
	if got := p.counts(t); got != after {
		t.Fatalf("turning the pack on again wrote to the forms: %s, before %s", got, after)
	}

	// A department takes the first report off its step and starts a second
	// version of the final report. Turning the pack on again keeps both.
	p.call("state.admin", http.MethodDelete, p.base+"/forms/first-report/assignments/first_report", nil, http.StatusNoContent, nil)
	var final struct {
		Definition map[string]any `json:"definition"`
	}
	p.call("state.admin", http.MethodGet, p.base+"/forms/final-report/versions/1", nil, http.StatusOK, &final)
	final.Definition["version"] = 2
	final.Definition["title"] = map[string]any{"en": "Final report (Demo State edition)"}
	p.call("state.admin", http.MethodPost, p.base+"/forms", final.Definition, http.StatusCreated, nil)
	changed := p.counts(t)
	p.call("state.admin", http.MethodPost, p.base+"/starter-pack", nil, http.StatusOK, &again)
	for _, f := range again.Forms {
		if f.Form == "first-report" && len(f.Assigned) != 0 {
			t.Fatalf("turning the pack on again put the first report back on its step: %+v", f)
		}
	}
	if got := p.counts(t); got != changed {
		t.Fatalf("turning the pack on again undid a department's change: %s, before %s", got, changed)
	}
	var drafts int
	if err := p.pool.QueryRow(p.ctx, `SELECT count(*) FROM form_versions v JOIN forms f ON f.id = v.form_id
		WHERE f.form_key = 'final-report' AND v.version = 2 AND v.published_at IS NULL
		  AND v.definition #>> '{title,en}' = 'Final report (Demo State edition)'`).Scan(&drafts); err != nil || drafts != 1 {
		t.Fatalf("the department's draft must stay as it is: %d (%v)", drafts, err)
	}
	// The department puts the first report back, as the pack had it.
	p.call("state.admin", http.MethodPut, p.base+"/forms/first-report/assignments/first_report", map[string]int{"version": 1}, http.StatusOK, nil)

	var audits int
	if err := p.pool.QueryRow(p.ctx, `SELECT count(*) FROM audit_log WHERE action = 'starter_pack.enable'`).Scan(&audits); err != nil || audits != 3 {
		t.Fatalf("each request to turn the pack on is audited: %d (%v)", audits, err)
	}
	p.call("state.admin", http.MethodPost, p.base+"/setup/steps/forms", map[string]string{"status": "done"}, http.StatusOK, nil)
}

type wizardAlert struct {
	ID        string  `json:"id"`
	Status    string  `json:"status"`
	Queue     string  `json:"queue"`
	UnitPath  string  `json:"unit_path"`
	Longitude float64 `json:"longitude"`
	Latitude  float64 `json:"latitude"`
	Source    string  `json:"source"`
	Incident  *string `json:"incident_id"`
}

func (p starterPackRun) alerts(t *testing.T, user string) []wizardAlert {
	t.Helper()
	var page struct {
		Items []wizardAlert `json:"items"`
	}
	p.call(user, http.MethodGet, p.base+"/alerts", nil, http.StatusOK, &page)
	return page.Items
}

func (p starterPackRun) alert(t *testing.T, user, id string) wizardAlert {
	t.Helper()
	var detail wizardAlert
	p.call(user, http.MethodGet, p.base+"/alerts/"+id, nil, http.StatusOK, &detail)
	return detail
}

// phoneTime formats a time as the phone sends it, with its offset.
func phoneTime(at time.Time) string {
	return at.In(indiaTime).Format(time.RFC3339)
}

func point(lon, lat float64) map[string]any {
	return map[string]any{"type": "Point", "coordinates": []float64{lon, lat}}
}

// offset moves a point east by the given number of metres.
func offset(a wizardAlert, east float64) (float64, float64) {
	return a.Longitude + east/(111320*math.Cos(a.Latitude*math.Pi/180)), a.Latitude
}

// workflow takes the beat guard's alert from New to Closed with the starter
// forms: the control room sends the team, the guard reports from the site
// and the division officer approves, verifies and closes.
func (p starterPackRun) workflow(t *testing.T) {
	var mine []wizardAlert
	for _, a := range p.alerts(t, "beat.guard") {
		if a.Queue == "unit" {
			mine = append(mine, a)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("the beat guard should have one unit alert: %+v", mine)
	}
	alert := mine[0]
	path := p.base + "/alerts/" + alert.ID
	here := map[string]float64{"longitude": alert.Longitude, "latitude": alert.Latitude}
	move := func(user, step string, want int) {
		t.Helper()
		p.call(user, http.MethodPost, path+"/steps", map[string]any{"step": step, "location": here}, want, nil)
	}
	submit := func(user, id, form, step string, values map[string]any, want int) string {
		t.Helper()
		var receipt struct {
			ID string `json:"id"`
		}
		p.call(user, http.MethodPost, path+"/submissions", map[string]any{"id": id, "form": form, "version": 1, "step": step,
			"values": values, "location": here, "deviceTime": phoneTime(time.Now().Add(-time.Minute))}, want, &receipt)
		return receipt.ID
	}
	upload := func(id, name, contentType string, body []byte) string {
		t.Helper()
		if status, data, _ := p.send("beat.guard", http.MethodPut, path+"/attachments/"+id+"?name="+name, contentType, body); status != http.StatusCreated {
			t.Fatalf("upload %s: HTTP %d %s", name, status, data)
		}
		return id
	}

	var forms struct {
		Step  string `json:"step"`
		Forms []struct {
			Form string `json:"form"`
			Step string `json:"step"`
		} `json:"forms"`
	}
	p.call("beat.guard", http.MethodGet, path+"/forms", nil, http.StatusOK, &forms)
	var listed []string
	for _, f := range forms.Forms {
		listed = append(listed, f.Step+":"+f.Form)
	}
	if forms.Step != "new" || !slices.Equal(listed, []string{"final_report:final-report", "first_report:first-report",
		"reached_site:control-room-dispatch", "team_sent:control-room-dispatch"}) {
		t.Fatalf("the alert's forms are %s at %v", forms.Step, listed)
	}

	start := time.Now().Add(-3 * time.Hour)
	dispatch := map[string]any{"team_name": "Demo fire crew 2", "team_leader": "Demo Guard", "team_size": 4,
		"vehicle": "jeep", "dispatched_at": phoneTime(start), "instructions": "Check the ridge above the stream."}

	// The beat guard acknowledges and the control room sends a team.
	move("beat.guard", "acknowledged", http.StatusOK)
	move("division.officer", "team_sent", http.StatusOK)
	move("division.officer", "reached_site", http.StatusUnprocessableEntity)
	submit("division.officer", "5ca1ab1e-0000-4000-8000-00000000d001", "control-room-dispatch", "team_sent", dispatch, http.StatusCreated)

	// The team reports its arrival on the same form, then controls the fire.
	move("beat.guard", "reached_site", http.StatusOK)
	move("beat.guard", "controlled", http.StatusUnprocessableEntity)
	arrival := map[string]any{"reached_at": phoneTime(start.Add(90 * time.Minute)),
		"site_location": point(alert.Longitude, alert.Latitude), "fire_found": "yes"}
	for key, value := range dispatch {
		arrival[key] = value
	}
	submit("beat.guard", "5ca1ab1e-0000-4000-8000-00000000d002", "control-room-dispatch", "reached_site", arrival, http.StatusCreated)
	move("beat.guard", "controlled", http.StatusOK)
	move("beat.guard", "extinguished", http.StatusOK)
	move("beat.guard", "first_report", http.StatusOK)

	// First report from the site, with photos and a signature.
	photo := append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte("synthetic"), 50)...)
	signature := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("synthetic"), 20)...)
	photoID := upload("5ca1ab1e-0000-4000-8000-00000000d101", "site.jpg", "image/jpeg", photo)
	signatureID := upload("5ca1ab1e-0000-4000-8000-00000000d102", "sign.png", "image/png", signature)
	move("beat.guard", "final_report", http.StatusUnprocessableEntity)
	first := submit("beat.guard", "5ca1ab1e-0000-4000-8000-00000000d003", "first-report", "", map[string]any{
		"reached_at": phoneTime(start.Add(90 * time.Minute)), "cause_group": "human", "cause": "campfire",
		"estimated_area": 1.5, "vegetation": []string{"grass", "leaf-litter"}, "site_photos": []string{photoID},
		"teams":     []map[string]any{{"team_name": "Demo fire crew 2", "members": 4, "team_arrived": "11:30"}},
		"equipment": []string{"beaters"}, "reporter_signature": signatureID,
	}, http.StatusCreated)
	move("beat.guard", "final_report", http.StatusOK)

	// Final report with the burnt area drawn on the map.
	lon, lat := alert.Longitude, alert.Latitude
	officerSignature := upload("5ca1ab1e-0000-4000-8000-00000000d103", "officer.png", "image/png", signature)
	move("division.officer", "verified", http.StatusUnprocessableEntity)
	final := submit("beat.guard", "5ca1ab1e-0000-4000-8000-00000000d004", "final-report", "final_report", map[string]any{
		"extinguished_at": phoneTime(start.Add(150 * time.Minute)),
		"burnt_extent": map[string]any{"type": "Polygon", "coordinates": [][][]float64{{
			{lon - 0.001, lat - 0.001}, {lon + 0.001, lat - 0.001}, {lon + 0.001, lat + 0.001}, {lon - 0.001, lat + 0.001}, {lon - 0.001, lat - 0.001}}}},
		"compartments":      []map[string]any{{"compartment_name": "Demo compartment 7", "compartment_area": 2.25}},
		"damage_types":      []string{"grassland"},
		"officer_signature": officerSignature,
		"signed_on":         time.Now().In(indiaTime).Format("2006-01-02"),
	}, http.StatusCreated)

	// The division officer approves both reports, verifies and closes.
	for _, id := range []string{first, final} {
		p.call("beat.guard", http.MethodPost, p.base+"/submissions/"+id+"/approval", map[string]string{"decision": "approved"}, http.StatusForbidden, nil)
		p.call("division.officer", http.MethodPost, p.base+"/submissions/"+id+"/approval", map[string]string{"decision": "approved"}, http.StatusOK, nil)
	}
	move("beat.guard", "verified", http.StatusForbidden)
	move("division.officer", "verified", http.StatusOK)
	move("beat.guard", "closed", http.StatusForbidden)
	move("division.officer", "closed", http.StatusOK)

	var history struct {
		Entries []struct {
			Step  string `json:"step"`
			Actor string `json:"actor"`
		} `json:"entries"`
		Moves []json.RawMessage `json:"moves"`
	}
	p.call("division.officer", http.MethodGet, path+"/history", nil, http.StatusOK, &history)
	var steps []string
	for _, e := range history.Entries {
		steps = append(steps, e.Step)
	}
	if !slices.Equal(steps, []string{"new", "acknowledged", "team_sent", "reached_site", "controlled", "extinguished",
		"first_report", "final_report", "verified", "closed"}) || len(history.Moves) != 0 {
		t.Fatalf("the alert went through %v with moves %d", steps, len(history.Moves))
	}
	if got := p.alert(t, "beat.guard", alert.ID); got.Status != "closed" {
		t.Fatalf("the alert is %s, want closed", got.Status)
	}

	// Each submission was made by the right person at the right step, and
	// the flat views return the answers.
	rows, err := p.pool.Query(p.ctx, `SELECT f.form_key, s.step, u.display_name, COALESCE(ap.decision, '')
		FROM submissions s JOIN form_versions v ON v.id = s.form_version_id JOIN forms f ON f.id = v.form_id
		JOIN users u ON u.id = s.user_id LEFT JOIN approvals ap ON ap.submission_id = s.id
		WHERE s.alert_id = $1::bigint ORDER BY s.received_at`, alert.ID)
	if err != nil {
		t.Fatal(err)
	}
	var made []string
	for rows.Next() {
		var form, step, who, decision string
		if err := rows.Scan(&form, &step, &who, &decision); err != nil {
			t.Fatal(err)
		}
		made = append(made, strings.Join([]string{form, step, who, decision}, " "))
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	t.Logf("submissions: %q", made)
	if len(made) != 4 || !strings.HasPrefix(made[0], "control-room-dispatch team_sent ") ||
		!strings.HasPrefix(made[1], "control-room-dispatch reached_site ") ||
		!strings.HasPrefix(made[2], "first-report first_report ") || !strings.HasSuffix(made[2], " approved") ||
		!strings.HasPrefix(made[3], "final-report final_report ") || !strings.HasSuffix(made[3], " approved") {
		t.Fatalf("unexpected submissions: %q", made)
	}
	var people, teams int
	var hours, area float64
	var view, teamView string
	if err := p.pool.QueryRow(p.ctx, `SELECT 'form_views.' || s.view_name, 'form_views.' || t.view_name
		FROM forms f JOIN form_versions v ON v.form_id = f.id
		JOIN form_version_views s ON s.form_version_id = v.id AND s.repeat_key = ''
		JOIN form_version_views t ON t.form_version_id = v.id AND t.repeat_key = 'teams'
		WHERE f.form_key = 'first-report' AND v.version = 1`).Scan(&view, &teamView); err != nil {
		t.Fatal(err)
	}
	if err := p.pool.QueryRow(p.ctx, `SELECT total_staff, team_count, hours_to_reach,
		(SELECT count(*) FROM `+teamView+` WHERE _alert_id = $1::bigint)::int FROM `+view+` WHERE _alert_id = $1::bigint`, alert.ID).
		Scan(&people, &teams, &hours, new(int)); err != nil || people != 4 || teams != 1 || hours <= 0 {
		t.Fatalf("the first report's flat view: %d people, %d teams, %.1f hours (%v)", people, teams, hours, err)
	}
	var finalView string
	if err := p.pool.QueryRow(p.ctx, `SELECT 'form_views.' || fv.view_name FROM forms f JOIN form_versions v ON v.form_id = f.id
		JOIN form_version_views fv ON fv.form_version_id = v.id AND fv.repeat_key = ''
		WHERE f.form_key = 'final-report' AND v.version = 1`).Scan(&finalView); err != nil {
		t.Fatal(err)
	}
	if err := p.pool.QueryRow(p.ctx, `SELECT burnt_extent_area_ha FROM `+finalView+` WHERE _alert_id = $1::bigint`, alert.ID).
		Scan(&area); err != nil || area < 4 || area > 6 {
		t.Fatalf("the final report's burnt area is %.2f ha (%v), want about 5", area, err)
	}
}

// manualReport records fires reported by other sources and checks that each
// one makes an alert that follows the normal workflow.
func (p starterPackRun) manualReport(t *testing.T) {
	var guardAlert, otherAlert wizardAlert
	for _, a := range p.alerts(t, "state.admin") {
		switch {
		case a.Queue != "unit":
		case a.UnitPath == "ds.c1.d1.r2.b05":
			guardAlert = a
		case !strings.HasPrefix(a.UnitPath, "ds.c1.d1.r2.b05"):
			otherAlert = a
		}
	}
	if guardAlert.ID == "" || otherAlert.ID == "" {
		t.Fatal("the wizard's alerts should include one in the guard's beat and one elsewhere")
	}
	report := func(id string, lon, lat float64, source string) map[string]any {
		values := map[string]any{"report_source": source, "reported_at": phoneTime(time.Now().Add(-20 * time.Minute)),
			"fire_location": point(lon, lat), "landmark": "Near the demo watch tower", "visible_signs": "smoke", "estimated_area": 0.5}
		if source == "public-call" {
			values["reporter_name"] = "Demo Caller"
			values["reporter_phone"] = "9000000000"
		}
		return map[string]any{"id": id, "version": 1, "values": values}
	}
	url := p.base + "/manual-fire-reports"

	// A guard records a call about smoke 200 metres from their alert.
	lon, lat := offset(guardAlert, 200)
	body := report("5ca1ab1e-0000-4000-8000-00000000e001", lon, lat, "public-call")
	p.call("no.role", http.MethodPost, url, body, http.StatusForbidden, nil)
	status, data, header := p.send("beat.guard", http.MethodPost, url, "application/json", mustJSON(t, body))
	if status != http.StatusCreated {
		t.Fatalf("a manual fire report got HTTP %d: %s", status, data)
	}
	var receipt struct {
		ID      string         `json:"id"`
		AlertID string         `json:"alertId"`
		Step    string         `json:"step"`
		Form    string         `json:"form"`
		Values  map[string]any `json:"values"`
	}
	if err := json.Unmarshal(data, &receipt); err != nil || receipt.Step != "new" || receipt.Form != "manual-fire-report" ||
		header.Get("Location") != p.base+"/alerts/"+receipt.AlertID || receipt.Values["reporter_phone"] != "9000000000" {
		t.Fatalf("unexpected receipt %s (Location %s, %v)", data, header.Get("Location"), err)
	}
	if again, retry, _ := p.send("beat.guard", http.MethodPost, url, "application/json", mustJSON(t, body)); again != http.StatusOK || !bytes.Equal(retry, data) {
		t.Fatalf("a retried report must return the first receipt: HTTP %d %s", again, retry)
	}
	changed := report("5ca1ab1e-0000-4000-8000-00000000e001", lon, lat, "staff-sighting")
	p.call("beat.guard", http.MethodPost, url, changed, http.StatusConflict, nil)

	made := p.alert(t, "beat.guard", receipt.AlertID)
	if made.Status != "new" || made.Queue != "unit" || made.UnitPath != "ds.c1.d1.r2.b05" || made.Source != "manual" || made.Incident == nil {
		t.Fatalf("the manual report's alert: %+v", made)
	}
	p.alert(t, "division.officer", receipt.AlertID)
	var detections, matches int
	var workspace string
	if err := p.pool.QueryRow(p.ctx, `SELECT count(*), min(d.workspace_id::text),
		(SELECT count(*) FROM detection_matches m WHERE (m.detection_id, m.observed_at) = (min(d.id), min(d.observed_at)))
		FROM detections d WHERE d.source = 'manual'`).Scan(&detections, &workspace, &matches); err != nil || detections != 1 || matches != 1 || workspace == "" {
		t.Fatalf("one manual detection matched in its own workspace only: %d detections, %d matches, workspace %q (%v)", detections, matches, workspace, err)
	}
	// The fire is 200 metres from the guard's alert, so it joins that
	// alert's incident, whose officers have been told already.
	if guardAlert.Incident == nil || *made.Incident != *guardAlert.Incident {
		t.Fatalf("the manual report's alert must join the nearby incident: %v, want %v", made.Incident, guardAlert.Incident)
	}
	var notified int
	if err := p.pool.QueryRow(p.ctx, `SELECT count(*) FROM notifications WHERE alert_id = $1::bigint`, receipt.AlertID).Scan(&notified); err != nil || notified != 0 {
		t.Fatalf("a new member of a known incident notifies no one again: %d (%v)", notified, err)
	}
	var submissions []struct {
		Form string `json:"form"`
		Step string `json:"step"`
	}
	p.call("division.officer", http.MethodGet, p.base+"/alerts/"+receipt.AlertID+"/submissions", nil, http.StatusOK, &submissions)
	if len(submissions) != 1 || submissions[0].Form != "manual-fire-report" || submissions[0].Step != "new" {
		t.Fatalf("the report must be kept on the alert: %+v", submissions)
	}

	// The alert follows the normal workflow.
	p.call("beat.guard", http.MethodPost, p.base+"/alerts/"+receipt.AlertID+"/steps", map[string]any{"step": "acknowledged"}, http.StatusOK, nil)

	// Reports that cannot make an alert leave nothing behind.
	refuse := func(user string, body map[string]any, want int, message string) {
		t.Helper()
		status, data, _ := p.send(user, http.MethodPost, url, "application/json", mustJSON(t, body))
		if status != want || !strings.Contains(string(data), message) {
			t.Fatalf("got HTTP %d %s, want %d with %q", status, data, want, message)
		}
	}
	lon, lat = offset(otherAlert, 100)
	refuse("beat.guard", report("5ca1ab1e-0000-4000-8000-00000000e002", lon, lat, "staff-sighting"), http.StatusUnprocessableEntity,
		"outside the units you work in")
	refuse("state.admin", report("5ca1ab1e-0000-4000-8000-00000000e003", 72.8, 19.0, "other-agency"), http.StatusUnprocessableEntity,
		"outside the workspace")
	var outLon, outLat float64
	if err := p.pool.QueryRow(p.ctx, `SELECT ST_X(d.geom), ST_Y(d.geom) FROM detection_matches m
		JOIN detections d ON (d.id, d.observed_at) = (m.detection_id, m.observed_at) WHERE m.forest_class = 'outside' LIMIT 1`).Scan(&outLon, &outLat); err != nil {
		t.Fatalf("the synthetic feed should have a detection outside the forest: %v", err)
	}
	refuse("state.admin", report("5ca1ab1e-0000-4000-8000-00000000e004", outLon, outLat, "other-satellite-service"), http.StatusUnprocessableEntity,
		"outside the forest")
	missing := report("5ca1ab1e-0000-4000-8000-00000000e005", lon, lat, "public-call")
	delete(missing["values"].(map[string]any), "reporter_phone")
	refuse("state.admin", missing, http.StatusUnprocessableEntity, `Field \"reporter_phone\" is required.`)
	refuse("state.admin", map[string]any{"id": "5ca1ab1e-0000-4000-8000-00000000e006", "version": 2,
		"values": report("", lon, lat, "other-agency")["values"]}, http.StatusUnprocessableEntity, "not published")
	refuse("state.admin", map[string]any{"id": "5ca1ab1e-0000-4000-8000-00000000e007", "form": "first-report", "version": 1,
		"values": map[string]any{}}, http.StatusBadRequest, "manual fire report")
	if err := p.pool.QueryRow(p.ctx, `SELECT count(*) FROM detections WHERE source = 'manual'`).Scan(&detections); err != nil || detections != 1 {
		t.Fatalf("refused reports must leave no detection: %d (%v)", detections, err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
