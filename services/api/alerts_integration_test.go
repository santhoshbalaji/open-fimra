package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

func checkAlertAccess(t *testing.T, ctx context.Context, tx pgx.Tx, slug, prefix string, request func(string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	base := "/api/v1/workspaces/" + slug + "/alerts"
	list := func(subject, query string) alertPage {
		t.Helper()
		w := request(http.MethodGet, base+query, subject)
		var page alertPage
		if w.Code != 200 {
			t.Fatalf("%s %s returned %d: %s", subject, query, w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	for _, tc := range []struct {
		subject            string
		unit, edge, triage int
		review             bool
	}{
		{"a1", 3, 1, 3, true}, {"a2", 1, 0, 1, true}, {"a3", 0, 0, 0, false},
	} {
		page := list(tc.subject, "")
		if len(page.Items) != tc.unit+tc.edge+tc.triage || page.Counts["unit"] != tc.unit || page.Counts["edge_review"] != tc.edge || page.Counts["triage"] != tc.triage || page.CanReview != tc.review {
			t.Fatalf("%s got %+v", tc.subject, page)
		}
		t.Logf("%s: unit %d, edge review %d, triage %d", tc.subject, tc.unit, tc.edge, tc.triage)
	}
	if w := request(http.MethodGet, base, "a4"); w.Code != 403 {
		t.Fatalf("no.role got %d", w.Code)
	}
	assertUnauthorized(t, request(http.MethodGet, base, ""), "Bearer")
	if page := list("a1", "?queue=unit&status=new&hours=24&class=inside&unitPath=ds.c1.d1"); len(page.Items) != 1 {
		t.Fatalf("combined filters returned %+v", page)
	}
	if page := list("a1", "?hours=1"); len(page.Items) != 0 {
		t.Fatal("hour filter ignored")
	}
	page := list("a1", "?limit=2")
	seen := map[string]bool{}
	var newest []string
	for {
		for _, row := range page.Items {
			var alert struct {
				ID       string `json:"id"`
				Queue    string `json:"queue"`
				BeatCode string `json:"beat_code"`
				Division string `json:"division_name"`
				Line     string `json:"place_hierarchy"`
				Places   map[string]struct{ Code, Name string }
			}
			if err := json.Unmarshal(row, &alert); err != nil {
				t.Fatal(err)
			}
			if alert.BeatCode != "" && (alert.Line != "territorial" || alert.Places["beat"].Code != alert.BeatCode ||
				alert.Places["division"].Name != alert.Division) {
				t.Fatalf("alert places do not match the levels: %s", row)
			}
			if seen[alert.ID] {
				t.Fatal("duplicate cursor row", alert.ID)
			}
			seen[alert.ID] = true
			newest = append(newest, alert.ID)
			if w := request(http.MethodGet, base+"/"+alert.ID, "a1"); w.Code != 200 {
				t.Fatalf("detail returned %d", w.Code)
			}
			if alert.Queue != "unit" {
				if w := request(http.MethodGet, base+"/"+alert.ID, "a3"); w.Code != 404 {
					t.Fatalf("beat read review detail: %d", w.Code)
				}
			}
		}
		if page.NextCursor == "" {
			break
		}
		page = list("a1", "?limit=2&cursor="+url.QueryEscape(page.NextCursor))
	}
	if len(seen) != 7 {
		t.Fatalf("cursor read %d alerts", len(seen))
	}
	var oldest []string
	for query := "?sort=oldest&limit=2"; ; {
		page = list("a1", query)
		for _, row := range page.Items {
			var item struct {
				ID         string `json:"id"`
				NextAction string `json:"next_action"`
			}
			if err := json.Unmarshal(row, &item); err != nil || item.NextAction == "" {
				t.Fatalf("alert lacks a next action: %s (%v)", row, err)
			}
			oldest = append(oldest, item.ID)
		}
		if page.NextCursor == "" {
			break
		}
		query = "?sort=oldest&limit=2&cursor=" + url.QueryEscape(page.NextCursor)
	}
	slices.Reverse(newest)
	if !slices.Equal(newest, oldest) {
		t.Fatalf("oldest pages = %v, want %v", oldest, newest)
	}
	for _, query := range []string{"?sort=invalid", "?queue=invalid", "?status=old", "?class=bad", "?unitPath=a..b", "?cursor=x", "?hours=0", "?limit=201", "/bad"} {
		if w := request(http.MethodGet, base+query, "a1"); w.Code != 400 {
			t.Fatalf("%s returned %d", query, w.Code)
		}
	}
	// Move the beat grant onto the shared boundary. The unit alert in B05
	// should appear but its shared-boundary neighbour needs a reviewer.
	beatTx, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := beatTx.Exec(ctx, `UPDATE role_assignments SET unit_path='ds.c1.d1.r2.b05'
	    WHERE workspace_id=$1 AND role_slug='field_staff'`, prefix+"01"); err != nil {
		t.Fatal(err)
	}
	beatPage := list("a3", "")
	if len(beatPage.Items) != 1 || beatPage.Counts["unit"] != 1 || beatPage.CanReview {
		t.Fatalf("B05 guard got %+v", beatPage)
	}
	if err := beatTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// The officer also serves as a field worker at the near-edge beat.
	// The unrelated division grant cannot grant review access here.
	if _, err := tx.Exec(ctx, `INSERT INTO role_assignments(user_id,workspace_id,hierarchy_id,unit_path,role_slug)
			SELECT user_id,workspace_id,hierarchy_id,'ds.c2.d3.r5.b14','field_staff' FROM role_assignments
			WHERE workspace_id=$1 AND role_slug='division_officer'`, prefix+"01"); err != nil {
		t.Fatal(err)
	}
	if len(list("a2", "?queue=edge_review").Items) != 0 {
		t.Fatal("review leaked across grants")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM role_assignments WHERE workspace_id=$1 AND unit_path='ds.c2.d3.r5.b14'`, prefix+"01"); err != nil {
		t.Fatal(err)
	}
}

func checkAlertTriage(t *testing.T, ctx context.Context, tx pgx.Tx, slug, prefix, source string, now time.Time,
	request func(string, string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	testTx, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer testTx.Rollback(ctx)
	base := "/api/v1/workspaces/" + slug + "/alerts"
	call := func(method, path, subject, body string, status int) []byte {
		t.Helper()
		w := request(method, path, subject, body)
		if w.Code != status {
			t.Fatalf("%s %s as %s = %d, want %d: %s", method, path, subject, w.Code, status, w.Body)
		}
		return w.Body.Bytes()
	}
	var id, edge, receiver, outside string
	// The same workspace also has historical reference fixtures. Select the
	// alerts from the replay that the queue's time window can show.
	err = testTx.QueryRow(ctx, `
WITH replay AS (
    SELECT a.id, a.queue FROM alerts a
    JOIN detections d ON (d.id, d.observed_at) = (a.detection_id, a.observed_at)
    WHERE a.workspace_id = $1 AND d.source = $5
)
SELECT
  (SELECT a.id::text FROM replay a WHERE a.queue = 'triage'
       AND EXISTS (SELECT FROM alert_roles($2, $3, a.id) WHERE officer) ORDER BY a.id LIMIT 1),
  (SELECT id::text FROM replay WHERE queue = 'edge_review' ORDER BY id LIMIT 1),
  (SELECT id::text FROM units WHERE hierarchy_id = $4 AND current_path = 'ds.c1.d1.r1.b01'),
  (SELECT id::text FROM units WHERE hierarchy_id = $4 AND current_path = 'ds.c2.d3.r5.b14')`,
		prefix+"01", prefix+"a2", slug, prefix+"10", source).Scan(&id, &edge, &receiver, &outside)
	if err != nil {
		t.Fatal(err)
	}
	detail := base + "/" + id
	assignment := detail + "/assignment"
	body := func(unit, reason string) string {
		data, err := json.Marshal(alertAssignmentRequest{UnitID: unit, Reason: reason})
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	call("GET", detail, "a3", "", 404)
	call("GET", assignment, "a3", "", 404)
	call("GET", assignment, "a4", "", 404)
	call("POST", assignment, "a3", body(receiver, "Guard cannot route"), 404)
	call("POST", assignment, "a2", body(outside, "Outside this grant"), 422)
	call("POST", base+"/"+edge+"/assignment", "a2", body(receiver, "Cannot review this alert"), 404)
	call("POST", assignment, "a2", body(receiver, " "), 400)

	var choices []struct{ ID string }
	if err := json.Unmarshal(call("GET", assignment, "a2", "", 200), &choices); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(choices, func(unit struct{ ID string }) bool { return unit.ID == receiver }) ||
		slices.ContainsFunc(choices, func(unit struct{ ID string }) bool { return unit.ID == outside }) {
		t.Fatalf("choices do not match the officer's grant: %+v", choices)
	}

	const reason = "This beat has the nearest team."
	receipt := call("POST", assignment, "a2", body(receiver, reason), 200)
	retry := call("POST", assignment, "a2", body(receiver, reason), 200)
	if !bytes.Equal(receipt, retry) || !bytes.Contains(receipt, []byte(reason)) || !bytes.Contains(receipt, []byte(`"unitName"`)) {
		t.Fatalf("assignment receipt is incomplete or changed on retry: %s / %s", receipt, retry)
	}
	call("POST", assignment, "a2", body(receiver, "Second decision"), 409)
	call("POST", assignment, "a3", body(receiver, "Guard cannot route"), 403)
	var assigned struct {
		Queue      string
		Assignment json.RawMessage
	}
	if err := json.Unmarshal(call("GET", detail, "a3", "", 200), &assigned); err != nil ||
		assigned.Queue != "unit" || !bytes.Equal(bytes.TrimSpace(receipt), assigned.Assignment) {
		t.Fatalf("assigned unit sees %+v, error %v", assigned, err)
	}
	call("GET", detail+"/history", "a3", "", 200)
	var unitPage alertPage
	if err := json.Unmarshal(call("GET", base+"?queue=unit", "a3", "", 200), &unitPage); err != nil ||
		len(unitPage.Items) != 1 || unitPage.Counts["unit"] != 1 {
		t.Fatalf("assignment did not enter the receiving queue: %+v, error %v", unitPage, err)
	}
	var auditCount int
	var originalQueue, auditReason string
	err = testTx.QueryRow(ctx, `
SELECT count(*), max(details #>> '{previous,queue}'), max(details->>'reason')
FROM audit_log WHERE action='alert.assign' AND target_id=$1 AND actor_id=$2`,
		id, prefix+"a2").Scan(&auditCount, &originalQueue, &auditReason)
	if err != nil || auditCount != 1 || originalQueue != "triage" || auditReason != reason {
		t.Fatalf("assignment audit count %d, previous %q, reason %q: %v", auditCount, originalQueue, auditReason, err)
	}

	// A root officer can assign the edge alert outside its original area.
	call("POST", base+"/"+edge+"/assignment", "a1", body(receiver, "Send to the duty team."), 200)
	call("GET", base+"/"+edge, "a3", "", 200)
	if _, err := testTx.Exec(ctx, `UPDATE role_assignments SET unit_path='ds.c2.d3.r5.b14'
	    WHERE workspace_id=$1 AND role_slug='field_staff'`, prefix+"01"); err != nil {
		t.Fatal(err)
	}
	call("GET", detail, "a3", "", 404)
	call("GET", base+"/"+edge, "a3", "", 404)
	call("GET", base+"/"+edge+"/history", "a3", "", 404)

	counts, err := runIngest(ctx, testTx, ingestRun{Source: source + "_old", Product: demoProduct}, func(context.Context) ([]firms.Observation, error) {
		return readFixture(filepath.Join(fixtureDir, "demo.csv"), demoProduct, true, now.AddDate(0, 0, -40))
	})
	if err != nil || counts.Inserted != 11 {
		t.Fatalf("load older fixture: %+v, %v", counts, err)
	}
	var old string
	if err := testTx.QueryRow(ctx, `
SELECT a.id::text FROM alerts a JOIN detections d ON (d.id, d.observed_at) = (a.detection_id, a.observed_at)
WHERE a.workspace_id = $1 AND d.source = $2 AND a.observed_at < now() - interval '30 days'
ORDER BY a.observed_at, a.id LIMIT 1`, prefix+"01", source+"_old").Scan(&old); err != nil {
		t.Fatal(err)
	}
	if _, err := testTx.Exec(ctx, `
UPDATE workflow_instances SET entered_at = CASE alert_id
    WHEN $2::bigint THEN now() - interval '6 hours'
    WHEN $3::bigint THEN now() - interval '3 hours'
    WHEN $4::bigint THEN now() - interval '3 hours'
    ELSE now() END
WHERE alert_id IN (SELECT id FROM alerts WHERE workspace_id = $1)`, prefix+"01", edge, old, id); err != nil {
		t.Fatal(err)
	}
	want := []string{id, old}
	slices.SortFunc(want, func(a, b string) int {
		first, _ := strconv.ParseInt(a, 10, 64)
		second, _ := strconv.ParseInt(b, 10, 64)
		return cmp.Compare(first, second)
	})
	want = append([]string{edge}, want...)
	var got []string
	for query := "?group=overdue&sort=overdue&limit=1"; ; {
		var page alertPage
		if err := json.Unmarshal(call("GET", base+query, "a1", "", 200), &page); err != nil {
			t.Fatal(err)
		}
		for _, row := range page.Items {
			var item struct{ ID string }
			if err := json.Unmarshal(row, &item); err != nil {
				t.Fatal(err)
			}
			got = append(got, item.ID)
		}
		if page.NextCursor == "" {
			break
		}
		if len(got) > len(want) {
			t.Fatalf("too many overdue rows: %v", got)
		}
		query = "?group=overdue&sort=overdue&limit=1&cursor=" + url.QueryEscape(page.NextCursor)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("overdue order = %v, want %v", got, want)
	}
	var scoped alertPage
	if err := json.Unmarshal(call("GET", base+"?group=overdue&hours=720", "a1", "", 200), &scoped); err != nil ||
		len(scoped.Items) != 2 {
		t.Fatalf("explicit overdue observation period gave %+v, %v", scoped, err)
	}
	var summary workspaceSummary
	if err := json.Unmarshal(call("GET", strings.TrimSuffix(base, "alerts")+"summary?hours=1", "a1", "", 200), &summary); err != nil ||
		summary.Overdue != 0 || summary.BacklogOverdue == nil || *summary.BacklogOverdue != 3 {
		t.Fatalf("narrow dashboard did not show full backlog: %+v, %v", summary, err)
	}

	req, err := readExportRequest(strings.NewReader(`{"kind":"alerts","format":"xlsx","group":"overdue"}`))
	if err != nil {
		t.Fatal(err)
	}
	var allowed, knownStep bool
	var exportID pgtype.UUID
	var total, frozen int
	var filters json.RawMessage
	err = testTx.QueryRow(ctx, exportsSQL, prefix+"a1", slug, req.Kind, req.Format, *req.Hours,
		req.ForestClasses, req.Queue, req.UnitPath, req.Step, 100,
		pgtype.Interval{Microseconds: time.Hour.Microseconds(), Valid: true}, req.Form, req.Decision, req.Group,
	).Scan(&allowed, &knownStep, &total, &exportID, &filters, &frozen)
	if err != nil || !allowed || !knownStep || total != len(want) || frozen != len(want) {
		t.Fatalf("overdue export froze %d/%d rows, want %d: %v", frozen, total, len(want), err)
	}

}
