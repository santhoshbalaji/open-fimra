package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// reportResponse reads the parts of a report the checks compare.
type reportResponse struct {
	situationReport
	IncidentCounts struct {
		Total, Opened, Active, Closed, Overdue int
		Items                                  []struct {
			ID         string
			AlertCount int
		}
	} `json:"-"`
	Reports struct {
		Submitted, Approved, Returned, AwaitingReview, Missing, AlertsMissing int
	} `json:"-"`
	Overdue []struct{ ID string } `json:"-"`
	Rows    []struct {
		Path                                           string
		Alerts, Active, Overdue, PendingReports, Forms int
	} `json:"-"`
	MapParts struct {
		Alerts      [][]any
		AlertsShown int
		Outline     json.RawMessage
		Bounds      []float64
	} `json:"-"`
}

func readReport(t *testing.T, body []byte) reportResponse {
	t.Helper()
	var r reportResponse
	if err := json.Unmarshal(body, &r.situationReport); err != nil {
		t.Fatalf("report JSON: %v: %s", err, body)
	}
	for _, part := range []struct {
		raw  json.RawMessage
		into any
	}{{r.Incidents, &r.IncidentCounts}, {r.FieldReports, &r.Reports}, {r.OverdueAlerts, &r.Overdue},
		{r.Units, &r.Rows}, {r.Map, &r.MapParts}} {
		if err := json.Unmarshal(part.raw, part.into); err != nil {
			t.Fatalf("report part %s: %v", part.raw, err)
		}
	}
	return r
}

// checkSituationReport compares situation reports with the dashboard and with
// direct database counts for each demo user, and checks who may report on what.
func checkSituationReport(t *testing.T, ctx context.Context, tx pgx.Tx, slug, prefix string, request func(string, string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	save, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer save.Rollback(ctx)
	base := "/api/v1/workspaces/" + slug
	workspace := prefix + "01"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Synthetic fires in Beat 05's forest at several ages: two close together
	// make one incident. The beat guard's grant moves to that beat.
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	var fires []firms.Observation
	for i, fire := range []struct {
		lon, lat float64
		age      time.Duration
	}{{86.297475, 16.418669, 2 * time.Hour}, {86.300457, 16.422232, 3 * time.Hour},
		{86.297475, 16.418669, 50 * time.Hour}, {86.300457, 16.422232, 121 * time.Hour}} {
		fires = append(fires, firms.Observation{Satellite: "N", Instrument: "VIIRS", Longitude: fire.lon, Latitude: fire.lat,
			ObservedAt: now.Add(-fire.age).Truncate(time.Minute), Raw: map[string]string{"report_case": fmt.Sprint(i)}})
	}
	if counts, err := runIngest(ctx, tx, ingestRun{Source: "report_" + strings.ReplaceAll(slug, "-", "_"), Product: demoProduct},
		func(context.Context) ([]firms.Observation, error) { return fires, nil }); err != nil || counts.Inserted != 4 {
		t.Fatalf("report fires: %+v %v", counts, err)
	}
	exec(`UPDATE role_assignments SET unit_path='ds.c1.d1.r2.b05' WHERE workspace_id=$1 AND role_slug='field_staff'`, workspace)

	// Two alerts in the field officer's division reach their first report. One
	// has an approved submission, the other only a returned one, so its form is
	// still missing. The first is left overdue.
	rows, err := tx.Query(ctx, `SELECT a.id::text FROM alerts a JOIN workflow_instances i ON i.alert_id=a.id
 WHERE a.workspace_id=$1 AND i.current_step='new' AND a.queue='unit' AND a.unit_path <@ 'ds.c1.d1'
   AND a.observed_at BETWEEN now()-interval '6 days' AND now() ORDER BY a.id LIMIT 2`, workspace)
	if err != nil {
		t.Fatal(err)
	}
	picked, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(picked) != 2 {
		t.Fatalf("pick report alerts: %v %v", picked, err)
	}
	for _, id := range picked {
		for _, step := range []string{"acknowledged", "team_sent", "reached_site", "controlled", "extinguished", "first_report"} {
			body := fmt.Sprintf(`{"step":%q}`, step)
			if step == "reached_site" || step == "controlled" || step == "extinguished" {
				body = fmt.Sprintf(`{"step":%q,"reason":"Synthetic radio report"}`, step)
			}
			if w := request("POST", base+"/alerts/"+id+"/steps", "a1", body); w.Code != 200 {
				t.Fatalf("move %s to %s: %d %s", id, step, w.Code, w.Body)
			}
		}
	}
	exec(`UPDATE workflow_instances SET entered_at=now()-interval '4 days' WHERE alert_id=$1`, picked[0])
	exec(`INSERT INTO forms (id, workspace_id, form_key) OVERRIDING SYSTEM VALUE VALUES (900001, $1, 'report-test')`, workspace)
	exec(`INSERT INTO form_versions (id, form_id, version, source, definition, columns, published_by, published_at)
 OVERRIDING SYSTEM VALUE SELECT 900001, 900001, 1, '{}', '{"version":1,"title":{"en":"Synthetic first report"}}', '[]', u.id, now()
 FROM users u WHERE u.oidc_subject=$1`, prefix+"a1")
	exec(`INSERT INTO form_assignments (form_id, form_version_id, template_id, step)
 SELECT 900001, 900001, template_id, 'first_report' FROM workflow_instances WHERE alert_id=$1`, picked[0])
	exec(`INSERT INTO submissions (id, workspace_id, form_version_id, alert_id, template_id, step, user_id, answers, request_hash)
 SELECT s.id, $1, 900001, s.alert, i.template_id, 'first_report', u.id, '{}', repeat('a', 64)
 FROM (VALUES ('5ca1ab1e-0000-4000-8000-00000000f271'::uuid, $2::bigint), ('5ca1ab1e-0000-4000-8000-00000000f272'::uuid, $3::bigint)) s(id, alert)
 JOIN workflow_instances i ON i.alert_id=s.alert JOIN users u ON u.oidc_subject=$4`, workspace, picked[0], picked[1], prefix+"a3")
	exec(`INSERT INTO approvals (submission_id, decision, note, user_id)
 SELECT s.id, s.decision, s.note, u.id FROM (VALUES ('5ca1ab1e-0000-4000-8000-00000000f271'::uuid, 'approved', ''),
   ('5ca1ab1e-0000-4000-8000-00000000f272'::uuid, 'returned', 'Count the teams again.')) s(id, decision, note)
 JOIN users u ON u.oidc_subject=$1`, prefix+"a2")

	get := func(subject string, params url.Values, status int) []byte {
		t.Helper()
		w := request("GET", base+"/situation-report?"+params.Encode(), subject, "")
		if w.Code != status {
			t.Fatalf("report %s as %s: %d want %d: %s", params.Encode(), subject, w.Code, status, w.Body)
		}
		return w.Body.Bytes()
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var today time.Time
	if err := tx.QueryRow(ctx, `SELECT (now() AT TIME ZONE 'Asia/Kolkata')::date::timestamp`).Scan(&today); err != nil {
		t.Fatal(err)
	}
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format(time.DateOnly) }

	for _, tc := range []struct {
		name, subject, path, unit string
		review                    bool
		children                  int
	}{
		{"state.admin", "a1", "", "ds", true, 2}, {"division.officer", "a2", "ds.c1.d1", "ds.c1.d1", true, 2},
		{"beat.guard", "a3", "ds.c1.d1.r2.b05", "ds.c1.d1.r2.b05", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The dashboard and the report for the same window and unit give the
			// same counts.
			for _, filter := range []struct{ summary, report string }{
				{"hours=168", "hours=168"}, {"hours=24", "hours=24"},
				{"hours=168&unitPath=ds.c1.d1.r1", "hours=168&unitPath=ds.c1.d1.r1"},
			} {
				if tc.subject == "a3" && strings.Contains(filter.report, "unitPath") {
					continue
				}
				w := request("GET", base+"/summary?"+filter.summary, tc.subject, "")
				if w.Code != 200 {
					t.Fatalf("summary: %d %s", w.Code, w.Body)
				}
				var dashboard workspaceSummary
				if err := json.Unmarshal(w.Body.Bytes(), &dashboard); err != nil {
					t.Fatal(err)
				}
				params, _ := url.ParseQuery(filter.report)
				rep := readReport(t, get(tc.subject, params, 200))
				if !reflect.DeepEqual(rep.Counts, dashboard.summaryCounts) {
					t.Fatalf("%s: report counts %+v\ndashboard %+v", filter.report, rep.Counts, dashboard.summaryCounts)
				}
				if rep.Alerts.Total != rep.Counts.Queues["unit"]+rep.Counts.Queues["edge_review"]+rep.Counts.Queues["triage"] ||
					rep.Alerts.Active+rep.Alerts.Closed != rep.Alerts.Total || rep.MapParts.AlertsShown != rep.Alerts.Total ||
					len(rep.MapParts.Alerts) != rep.Alerts.Total || len(rep.Overdue) != rep.Counts.Overdue {
					t.Fatalf("%s: report totals do not add up: %+v %+v", filter.report, rep.Alerts, rep.Counts)
				}
			}

			// A week and a single past day, against direct database counts.
			for _, period := range []struct{ from, to string }{{day(-6), day(0)}, {day(-2), day(-2)}, {day(0), ""}} {
				params := url.Values{"from": {period.from}}
				if period.to != "" {
					params.Set("to", period.to)
				}
				rep := readReport(t, get(tc.subject, params, 200))
				label := tc.name + " " + params.Encode()
				if rep.Unit == nil || rep.Unit.Path != tc.unit || rep.Unit.LevelLabel == "" {
					t.Fatalf("%s: unit %+v", label, rep.Unit)
				}
				// The expected population comes from the seed's grants, without
				// the API's queries or grant views.
				exec("DROP TABLE IF EXISTS report_alerts")
				exec(`CREATE TEMP TABLE report_alerts ON COMMIT DROP AS
 SELECT a.id, a.queue, a.observed_at, a.forest_class, a.hierarchy_unit_paths -> $6::text AS paths, i.template_id, i.current_step, i.entered_at, m.incident_id
 FROM alerts a JOIN workflow_instances i ON i.alert_id=a.id LEFT JOIN incident_members m ON m.alert_id=a.id
 WHERE a.workspace_id=$1 AND a.observed_at >= $2::timestamptz AND a.observed_at <= least($3::timestamptz, now())
   AND ($4 OR a.queue='unit') AND ($5='' OR a.hierarchy_unit_paths -> $6::text ? $5)`,
					pgx.QueryExecModeSimpleProtocol, workspace, rep.Period.Start, rep.Period.End, tc.review, tc.path, prefix+"10")
				check := func(what string, got, want int) {
					t.Helper()
					if got != want {
						t.Fatalf("%s %s: got %d want %d", label, what, got, want)
					}
				}
				start := today.AddDate(0, 0, -6)
				if period.from == day(-2) {
					start = today.AddDate(0, 0, -2)
				} else if period.to == "" {
					start = today
				}
				wantStart := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, indiaTime)
				if !rep.Period.Start.Equal(wantStart) {
					t.Fatalf("%s: period starts %s want %s", label, rep.Period.Start, wantStart)
				}
				for _, class := range []string{"inside", "near_edge", "outside", "fixed_heat_source"} {
					check("detections "+class, rep.Counts.Detections[class], count(`SELECT count(*) FROM detection_matches
 WHERE workspace_id=$1 AND observed_at >= $2::timestamptz AND observed_at <= least($3::timestamptz, now()) AND forest_class=$4
   AND ($5='' OR hierarchy_unit_paths -> $6::text ? $5)`, pgx.QueryExecModeSimpleProtocol, workspace, rep.Period.Start, rep.Period.End, class, tc.path, prefix+"10"))
				}
				const terminal = `('closed','false_alarm','duplicate','controlled_burn','not_reachable')`
				check("alerts", rep.Alerts.Total, count(`SELECT count(*) FROM report_alerts`))
				check("active", rep.Alerts.Active, count(`SELECT count(*) FROM report_alerts WHERE current_step NOT IN `+terminal))
				check("closed", rep.Alerts.Closed, count(`SELECT count(*) FROM report_alerts WHERE current_step IN `+terminal))
				check("overdue", rep.Counts.Overdue, count(`SELECT count(*) FROM report_alerts a WHERE now()-entered_at >
 (SELECT min(time_limit) FROM workflow_transitions WHERE template_id=a.template_id AND from_step=a.current_step)`))
				check("overdue list", len(rep.Overdue), rep.Counts.Overdue)
				check("pending reports", rep.Counts.PendingReports, count(`SELECT count(*) FROM report_alerts WHERE current_step IN ('first_report','final_report')`))
				stepTotal := 0
				for _, step := range rep.Steps {
					check("step "+step.Step, step.Count, count(`SELECT count(*) FROM report_alerts WHERE current_step=$1`, step.Step))
					stepTotal += step.Count
				}
				check("steps", stepTotal, rep.Alerts.Total)
				check("map alerts", len(rep.MapParts.Alerts), rep.Alerts.Total)
				check("incidents", rep.IncidentCounts.Total, count(`SELECT count(DISTINCT incident_id) FROM report_alerts`))
				check("active incidents", rep.IncidentCounts.Active, count(`SELECT count(DISTINCT r.incident_id) FROM report_alerts r
 JOIN workflow_instances i ON i.incident_id=r.incident_id WHERE i.current_step NOT IN `+terminal))
				for _, item := range rep.IncidentCounts.Items {
					// An incident shows only the members the user may see.
					check("incident "+item.ID+" members", item.AlertCount, count(`SELECT count(*) FROM incident_members m
 JOIN alerts a ON a.id=m.alert_id WHERE m.incident_id=$1::bigint AND ($2 OR a.queue='unit') AND ($3='' OR a.hierarchy_unit_paths -> $4::text ? $3)`,
						pgx.QueryExecModeSimpleProtocol, item.ID, tc.review, tc.path, prefix+"10"))
				}
				check("submitted", rep.Reports.Submitted, count(`SELECT count(*) FROM submissions s JOIN report_alerts r ON r.id=s.alert_id`))
				check("approved", rep.Reports.Approved, count(`SELECT count(*) FROM submissions s JOIN report_alerts r ON r.id=s.alert_id
 JOIN approvals p ON p.submission_id=s.id WHERE p.decision='approved'`))
				check("returned", rep.Reports.Returned, count(`SELECT count(*) FROM submissions s JOIN report_alerts r ON r.id=s.alert_id
 JOIN approvals p ON p.submission_id=s.id WHERE p.decision='returned'`))
				check("missing", rep.Reports.Missing, count(`SELECT count(*) FROM report_alerts r WHERE r.current_step='first_report'
 AND NOT EXISTS (SELECT FROM submissions s WHERE s.alert_id=r.id AND s.form_version_id=900001
   AND NOT EXISTS (SELECT FROM approvals p WHERE p.submission_id=s.id AND p.decision='returned'))`))
				check("unit rows", len(rep.Rows), tc.children)
				rowTotal := 0
				for _, row := range rep.Rows {
					check("unit "+row.Path, row.Alerts, count(`SELECT count(*) FROM report_alerts WHERE paths ? $1`, row.Path))
					rowTotal += row.Alerts
				}
				if tc.children > 0 && tc.subject == "a2" {
					check("division unit rows", rowTotal, rep.Alerts.Total)
				}
				if len(rep.MapParts.Bounds) != 4 || len(rep.MapParts.Outline) < 10 || len(rep.ResponseTimes) != len(responseMilestones) ||
					len(rep.Notes) == 0 || len(rep.PreparedFor) != 1 || rep.Workspace.Slug != slug {
					t.Fatalf("%s: incomplete report %s / %v / %+v", label, rep.MapParts.Outline, rep.MapParts.Bounds, rep.PreparedFor)
				}
				exec("DROP TABLE report_alerts")
			}
		})
	}
	// The set-up week has the moved alerts: the division officer's report shows
	// one approved submission, one returned and one form still missing.
	week := url.Values{"from": {day(-6)}, "to": {day(0)}}
	if rep := readReport(t, get("a2", week, 200)); rep.Reports.Submitted != 2 || rep.Reports.Approved != 1 ||
		rep.Reports.Returned != 1 || rep.Reports.Missing != 1 || rep.Reports.AlertsMissing != 1 || rep.Counts.Overdue < 1 {
		t.Fatalf("division field reports %+v, overdue %d", rep.Reports, rep.Counts.Overdue)
	}

	// Units the user may report on, and refusals outside them.
	for subject, want := range map[string]int{"a1": count(`SELECT count(*) FROM units WHERE hierarchy_id IN (SELECT id FROM hierarchies WHERE workspace_id=$1) AND valid_to IS NULL`, workspace),
		"a2": count(`SELECT count(*) FROM units WHERE hierarchy_id=$1 AND valid_to IS NULL AND path <@ 'ds.c1.d1'`, prefix+"10"), "a3": 1} {
		w := request("GET", base+"/situation-report/units", subject, "")
		var list struct{ Units []reportableUnit }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Units) != want {
			t.Fatalf("units for %s: %d %d want %d", subject, w.Code, len(list.Units), want)
		}
	}
	if w := request("GET", base+"/situation-report/units", "a4", ""); w.Code != 403 {
		t.Fatalf("no.role units: %d", w.Code)
	}
	for _, tc := range []struct {
		subject string
		params  url.Values
		status  int
	}{
		{"a3", url.Values{"from": {day(0)}, "unitPath": {"ds.c1.d1"}}, 403},
		{"a2", url.Values{"from": {day(0)}, "unitPath": {"ds.c2"}}, 403},
		{"a2", url.Values{"from": {day(0)}, "unitPath": {"ds"}}, 403},
		{"a2", url.Values{"from": {day(0)}, "unitPath": {"ds.c1.d1.r1"}}, 200},
		{"a1", url.Values{"from": {day(0)}, "unitPath": {"ds.c9"}}, 404},
		{"a1", url.Values{"from": {day(0)}, "unitPath": {"ds..c1"}}, 400},
		{"a4", url.Values{"from": {day(0)}}, 403},
		{"a1", url.Values{"from": {day(1)}}, 400},
		{"a1", url.Values{"from": {day(0)}, "to": {day(-1)}}, 400},
		{"a1", url.Values{"from": {day(-200)}, "to": {day(0)}}, 400},
		{"a1", url.Values{"from": {"15/03/2026"}}, 400},
		{"a1", url.Values{"from": {day(0)}, "hours": {"24"}}, 400},
		{"a1", url.Values{"hours": {"721"}}, 400},
	} {
		get(tc.subject, tc.params, tc.status)
	}
	// A beat guard's report holds only the beat guard's alerts, even for the
	// whole range they belong to on the map.
	guard := readReport(t, get("a3", url.Values{"from": {day(-6)}, "to": {day(0)}}, 200))
	officer := readReport(t, get("a2", url.Values{"from": {day(-6)}, "to": {day(0)}, "unitPath": {"ds.c1.d1.r2.b05"}}, 200))
	if guard.Alerts.Total == 0 || guard.Alerts.Total >= officer.Alerts.Total || guard.Counts.Queues["edge_review"] != 0 ||
		guard.Counts.Queues["triage"] != 0 {
		t.Fatalf("beat guard sees more than the beat: %+v vs %+v", guard.Counts.Queues, officer.Counts.Queues)
	}
	if n := count(`SELECT count(*) FROM audit_log WHERE workspace_id=$1 AND action='report.view' AND actor_id=$2
 AND details->>'unitPath'='ds.c1.d1.r2.b05'`, workspace, prefix+"a3"); n == 0 {
		t.Fatal("report views are not in the audit log")
	}

	// A PDF request saves the same report for the worker.
	w := request("POST", base+"/situation-reports", "a2", fmt.Sprintf(`{"from":%q,"to":%q}`, day(-6), day(0)))
	var status reportStatus
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &status) != nil || status.Status != "pending" ||
		w.Header().Get("Location") != base+"/situation-reports/"+status.ID || status.Scope == "" {
		t.Fatalf("create report: %d %s", w.Code, w.Body)
	}
	var saved situationReport
	var content []byte
	if err := tx.QueryRow(ctx, `SELECT content FROM situation_reports WHERE id=$1`, status.ID).Scan(&content); err != nil ||
		json.Unmarshal(content, &saved) != nil {
		t.Fatal(err)
	}
	live := readReport(t, get("a2", week, 200))
	sameJSON := func(a, b json.RawMessage) bool {
		var x, y any
		return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
	}
	if !reflect.DeepEqual(saved.Counts, live.Counts) || saved.Alerts != live.Alerts || !sameJSON(saved.FieldReports, live.FieldReports) ||
		!sameJSON(saved.Incidents, live.Incidents) || !sameJSON(saved.Map, live.Map) {
		t.Fatalf("saved report differs: %+v %s\nvs %+v %s", saved.Alerts, saved.FieldReports, live.Alerts, live.FieldReports)
	}
	for _, bad := range []string{`{"from":"x"}`, `{"from":"` + day(0) + `","extra":1}`, `[]`} {
		if w := request("POST", base+"/situation-reports", "a2", bad); w.Code != 400 {
			t.Fatalf("bad request %s: %d", bad, w.Code)
		}
	}
	if w := request("POST", base+"/situation-reports", "a3", `{"from":"`+day(0)+`","unitPath":"ds.c1.d1"}`); w.Code != 403 {
		t.Fatalf("beat guard division PDF: %d", w.Code)
	}
	reportURL := base + "/situation-reports/" + status.ID
	for subject, code := range map[string]int{"a1": 404, "a3": 404, "a4": 403, "a2": 200} {
		if w := request("GET", reportURL, subject, ""); w.Code != code {
			t.Fatalf("report status as %s: %d want %d", subject, w.Code, code)
		}
	}
	if w := request("GET", reportURL+"/file", "a2", ""); w.Code != 409 {
		t.Fatalf("pending file: %d", w.Code)
	}
	exec(`UPDATE situation_reports SET status='ready', pdf='%PDF-1.4 synthetic', page_count=1, finished_at=now() WHERE id=$1`, status.ID)
	if w := request("GET", reportURL+"/file", "a1", ""); w.Code != 404 {
		t.Fatalf("other user's file: %d", w.Code)
	}
	w = request("GET", reportURL+"/file", "a2", "")
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/pdf" || !strings.HasPrefix(w.Body.String(), "%PDF") ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "situation-report") {
		t.Fatalf("download: %d %v", w.Code, w.Header())
	}
	if n := count(`SELECT count(*) FROM audit_log WHERE target_id=$1 AND action IN ('report.create','report.download')`, status.ID); n != 2 {
		t.Fatalf("report audit entries: %d", n)
	}
	guarded, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guarded.Exec(ctx, `UPDATE situation_reports SET content='{}' WHERE id=$1`, status.ID); err == nil {
		t.Fatal("a ready report's content changed")
	}
	if err := guarded.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE situation_reports SET status='expired', content='{}', pdf=NULL, page_count=NULL, expires_at=expires_at WHERE id=$1`, status.ID)
	if w := request("GET", reportURL+"/file", "a2", ""); w.Code != 410 {
		t.Fatalf("expired file: %d", w.Code)
	}
}
