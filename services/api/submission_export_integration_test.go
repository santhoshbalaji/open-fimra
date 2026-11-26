package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// TestSubmissionExportIntegration takes two synthetic alerts through their
// first report, one in the division officer's division and one outside it.
// The officer lists and approves the first, and every export of field
// reports holds only the reports each user may see. It needs its own
// database and a geo worker reading it, because the worker sees only
// committed rows and reports can never be deleted. Run it with
// make submission-export-test-integration.
func TestSubmissionExportIntegration(t *testing.T) {
	if os.Getenv("FIMRA_SUBMISSION_EXPORT_INTEGRATION") != "1" {
		t.Skip("Run make submission-export-test-integration, which makes its own database and worker.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cfg := loadConfig()
	conn, err := pgx.ParseConfig(cfg.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if conn.Database != "fimra_submission_export_test" && !strings.HasSuffix(conn.Database, "__sub_export") {
		t.Fatal("This test commits reports that can never be deleted. Run make submission-export-test-integration for a fresh test database.")
	}
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := ensureDemoSeed(ctx, pool); err != nil {
		t.Fatalf("load the demo seed: %v", err)
	}
	if counts, err := runIngest(ctx, pool, ingestRun{Source: demoSource, Product: demoProduct}, func(context.Context) ([]firms.Observation, error) {
		return readFixture(filepath.Join(fixtureDir, "demo.csv"), demoProduct, true, time.Now())
	}); err != nil || counts.Inserted == 0 {
		t.Fatalf("load synthetic detections: %+v, %v", counts, err)
	}
	provider, err := discoverProvider(ctx, cfg.issuer)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newLocalAttachments(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandler(pool, newAuthenticator(provider, cfg.issuer, cfg.audience), slog.Default(), devOrigins,
		t.TempDir(), nil, withExports(cfg.exportDir, time.Hour), withAttachments(store, 1<<20)))
	t.Cleanup(server.Close)
	checkDir := os.Getenv("FIMRA_EXPORT_CHECK_DIR")
	if checkDir != "" {
		// Keep only this run's downloads for the read-back step.
		old, err := filepath.Glob(filepath.Join(checkDir, "open-fimra-*"))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range old {
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	client := &http.Client{Timeout: 20 * time.Second}
	tokens := map[string]string{}
	for _, user := range []string{"state.admin", "division.officer", "beat.guard", "no.role"} {
		tokens[user] = demoToken(t, client, provider.Endpoint().TokenURL, user)
	}
	const base = "/api/v1/workspaces/demo"

	send := func(method, path, user, contentType string, body []byte) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server.URL+base+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, data
	}
	call := func(method, path, user string, body any, want int) []byte {
		t.Helper()
		var data []byte
		switch b := body.(type) {
		case nil:
		case []byte:
			data = b
		default:
			data, _ = json.Marshal(b)
		}
		code, got := send(method, path, user, "application/json", data)
		if code != want {
			t.Fatalf("%s %s as %s: HTTP %d, want %d: %s", method, path, user, code, want, got)
		}
		return got
	}

	// The state administrator publishes the first report and puts it on the first report step.
	form, err := os.ReadFile(filepath.Join(formExamples, "first-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	call("POST", "/forms", "state.admin", form, 201)
	call("POST", "/forms/first-report/versions/1/publish", "state.admin", nil, 200)
	call("PUT", "/forms/first-report/assignments/first_report", "state.admin", map[string]any{"version": 1}, 200)

	// Alert A is in the division officer's division, alert B in another circle.
	type alertRow struct {
		id, path string
		lon, lat float64
	}
	pick := func(where string) alertRow {
		t.Helper()
		var a alertRow
		if err := pool.QueryRow(ctx, `SELECT a.id::text, a.unit_path::text, ST_X(d.geom), ST_Y(d.geom) FROM alerts a
JOIN detections d ON (d.id, d.observed_at) = (a.detection_id, a.observed_at)
WHERE a.queue = 'unit' AND `+where+` ORDER BY a.id LIMIT 1`).Scan(&a.id, &a.path, &a.lon, &a.lat); err != nil {
			t.Fatalf("pick alert %s: %v", where, err)
		}
		return a
	}
	alertA := pick(`a.unit_path <@ 'ds.c1.d1'`)
	alertB := pick(`a.unit_path <@ 'ds.c2'`)
	// The beat guard works in alert A's beat.
	if _, err := pool.Exec(ctx, `UPDATE role_assignments SET unit_path = $1::ltree
WHERE workspace_id = 'd0000000-0000-4000-8000-000000000001' AND role_slug = 'field_staff'`, alertA.path); err != nil {
		t.Fatal(err)
	}

	photo := append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte("synthetic"), 50)...)
	signature := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("synthetic"), 20)...)
	report := func(a alertRow, user, id string) string {
		t.Helper()
		for _, step := range []string{"acknowledged", "team_sent", "reached_site", "controlled", "extinguished", "first_report"} {
			call("POST", "/alerts/"+a.id+"/steps", user, map[string]any{"step": step,
				"location": map[string]float64{"longitude": a.lon, "latitude": a.lat}}, 200)
		}
		upload := func(attachment, name, contentType string, body []byte) {
			t.Helper()
			if code, got := send("PUT", "/alerts/"+a.id+"/attachments/"+attachment+"?name="+name, user, contentType, body); code != 201 {
				t.Fatalf("upload %s: HTTP %d %s", name, code, got)
			}
		}
		// Attachment IDs end in the report's last two characters, so each alert has its own.
		file := func(n string) string { return id[:len(id)-4] + id[len(id)-2:] + n }
		upload(file("f1"), "site.jpg", "image/jpeg", photo)
		upload(file("f2"), "sign.png", "image/png", signature)
		values := map[string]any{
			"reached_at": time.Now().Add(-time.Hour).Format(time.RFC3339), "fire_status": "controlled",
			"cause_group": "natural", "cause": "lightning", "estimated_area": 1.25, "vegetation": []string{"grass"},
			"site_photos": []string{file("f1")}, "reporter_signature": file("f2"),
			"teams": []map[string]any{{"team_name": "Demo Team A", "members": 6}},
		}
		call("POST", "/alerts/"+a.id+"/submissions", user, map[string]any{"id": id, "form": "first-report", "version": 1,
			"step": "first_report", "values": values}, 201)
		return id
	}
	reportA := report(alertA, "beat.guard", "5ca1ab1e-0000-4000-8000-0000000000a1")
	reportB := report(alertB, "state.admin", "5ca1ab1e-0000-4000-8000-0000000000b1")

	// The officer sees only the report in their division, waiting for a decision.
	type row struct {
		ID         string
		Decision   string
		CanApprove bool `json:"can_approve"`
	}
	list := func(user, query string) (map[string]int, []string) {
		t.Helper()
		var page struct {
			Counts map[string]int
			Items  []row
		}
		if err := json.Unmarshal(call("GET", "/submissions"+query, user, nil, 200), &page); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, item := range page.Items {
			ids = append(ids, item.ID)
			if user == "division.officer" && !item.CanApprove {
				t.Errorf("the officer cannot approve %s", item.ID)
			}
		}
		slices.Sort(ids)
		return page.Counts, ids
	}
	if counts, ids := list("division.officer", "?decision=pending"); !slices.Equal(ids, []string{reportA}) || counts["pending"] != 1 {
		t.Fatalf("officer's waiting reports: %v %v", counts, ids)
	}
	if _, ids := list("state.admin", ""); !slices.Equal(ids, []string{reportA, reportB}) {
		t.Fatalf("state administrator's reports: %v", ids)
	}
	call("GET", "/submissions/"+reportB, "division.officer", nil, 404)
	call("POST", "/submissions/"+reportB+"/approval", "division.officer", map[string]string{"decision": "approved"}, 404)
	call("POST", "/submissions/"+reportA+"/approval", "beat.guard", map[string]string{"decision": "approved"}, 403)
	call("GET", "/submissions", "no.role", nil, 403)

	// The division officer approves the first report.
	var decided struct {
		Approval *struct{ Decision, By string }
	}
	if err := json.Unmarshal(call("POST", "/submissions/"+reportA+"/approval", "division.officer",
		map[string]string{"decision": "approved", "note": "Checked with the beat guard by radio."}, 200), &decided); err != nil ||
		decided.Approval == nil || decided.Approval.Decision != "approved" || decided.Approval.By != "Demo Division Officer" {
		t.Fatalf("approval: %+v %v", decided.Approval, err)
	}
	if counts, ids := list("division.officer", "?decision=approved&form=first-report"); !slices.Equal(ids, []string{reportA}) ||
		counts["approved"] != 1 || counts["pending"] != 0 {
		t.Fatalf("officer's approved reports: %v %v", counts, ids)
	}
	// The approved report still counts, so the alert moves on.
	call("POST", "/alerts/"+alertA.id+"/steps", "beat.guard", map[string]any{"step": "final_report"}, 200)

	// Exports of field reports hold what each user may see, and nothing else.
	var created []string
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := pool.Exec(cleanup, "UPDATE exports SET expires_at = now() WHERE id = ANY($1::uuid[])", created); err != nil {
			t.Error(err)
		}
		if _, err := expireExports(cleanup, pool, cfg.exportDir); err != nil {
			t.Error(err)
		}
	})
	export := func(user string, body map[string]any) (exportStatus, []byte) {
		t.Helper()
		var status exportStatus
		if err := json.Unmarshal(call("POST", "/exports", user, body, 202), &status); err != nil {
			t.Fatal(err)
		}
		created = append(created, status.ID)
		for status.Status == "pending" {
			select {
			case <-ctx.Done():
				t.Fatalf("timed out waiting for export %s; is the geo worker running against this database?", status.ID)
			case <-time.After(100 * time.Millisecond):
			}
			if err := json.Unmarshal(call("GET", "/exports/"+status.ID, user, nil, 200), &status); err != nil {
				t.Fatal(err)
			}
		}
		if status.Status != "ready" || status.FeatureCount == nil || *status.FeatureCount != status.RowCount {
			t.Fatalf("export finished as %+v", status)
		}
		data := call("GET", "/exports/"+status.ID+"/file", user, nil, 200)
		if checkDir != "" {
			if err := os.WriteFile(filepath.Join(checkDir, status.FileName), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return status, data
	}
	features := func(data []byte) []map[string]any {
		t.Helper()
		var collection struct {
			Features []struct {
				Properties map[string]any `json:"properties"`
			} `json:"features"`
		}
		if err := json.Unmarshal(unzipMember(t, data, ".geojson"), &collection); err != nil {
			t.Fatal(err)
		}
		var out []map[string]any
		for _, f := range collection.Features {
			out = append(out, f.Properties)
		}
		return out
	}
	reportIDs := func(rows []map[string]any) []string {
		var ids []string
		for _, r := range rows {
			ids = append(ids, r["submission_id"].(string))
		}
		slices.Sort(ids)
		return ids
	}
	geojson := map[string]any{"kind": "submissions", "format": "geojson", "hours": 24, "form": "first-report"}
	status, data := export("division.officer", geojson)
	rows := features(data)
	if !slices.Equal(reportIDs(rows), []string{reportA}) || status.RowCount != 1 {
		t.Fatalf("officer's export has %v", reportIDs(rows))
	}
	got := rows[0]
	if got["decision"] != "approved" || got["fire_status"] != "controlled" || got["estimated_area"] != 1.25 ||
		got["alert_id"] != float64(mustInt(t, alertA.id)) ||
		got["answer_observed_at"] == nil || got["total_staff"] != 6.0 || got["form_version"] != 1.0 {
		t.Fatalf("officer's export row: %v", got)
	}
	for _, private := range [][]byte{[]byte("Demo Beat Guard"), []byte("Demo Division Officer"), []byte("Checked with the beat guard")} {
		if bytes.Contains(unzipMember(t, data, ".geojson"), private) || bytes.Contains(unzipMember(t, data, ".txt"), private) {
			t.Errorf("the export names a person: %s", private)
		}
	}
	if !bytes.Contains(unzipMember(t, data, ".txt"), []byte("Form: first-report")) {
		t.Error("the export note does not name the form")
	}
	if _, data := export("state.admin", geojson); !slices.Equal(reportIDs(features(data)), []string{reportA, reportB}) {
		t.Fatalf("state administrator's export has %v", reportIDs(features(data)))
	}
	if _, data := export("beat.guard", geojson); !slices.Equal(reportIDs(features(data)), []string{reportA}) {
		t.Fatalf("beat guard's export has %v", reportIDs(features(data)))
	}
	if status, _ := export("division.officer", map[string]any{"kind": "submissions", "format": "geojson", "decision": "pending"}); status.RowCount != 0 {
		t.Fatalf("officer's waiting reports export has %d rows", status.RowCount)
	}
	call("POST", "/exports", "no.role", geojson, 403)
	call("POST", "/exports", "division.officer", map[string]any{"kind": "submissions", "format": "geojson", "form": "no-such-form"}, 400)
	call("POST", "/exports", "division.officer", map[string]any{"kind": "alerts", "format": "geojson", "form": "first-report"}, 400)
	// Every format, for reading back with OGR.
	for _, format := range []string{"gpkg", "shapefile", "kml", "flatgeobuf", "csv_wkt", "csv_lonlat", "xlsx"} {
		if status, _ := export("division.officer", map[string]any{"kind": "submissions", "format": format, "form": "first-report"}); status.RowCount != 1 {
			t.Fatalf("%s export has %d rows", format, status.RowCount)
		}
	}

	var audited int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'export.create' AND details->>'kind' = 'submissions'
AND actor_id = 'd0000000-0000-4000-8000-0000000000a2'`).Scan(&audited); err != nil || audited != 9 {
		t.Fatalf("audited officer exports: %d %v", audited, err)
	}
	t.Logf("the division officer approved report %s for alert %s; exports of field reports held only what each user may see", reportA, alertA.id)
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	if err := json.Unmarshal([]byte(s), &n); err != nil {
		t.Fatal(err)
	}
	return n
}
