package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// TestFormsIntegration signs in as each demo user and takes a synthetic alert
// through the first report with a published form: publishing, assignment,
// attachments, submission, approval and the flat view. Everything runs in
// one transaction on the demo workspace and is rolled back at the end.
func TestFormsIntegration(t *testing.T) {
	if os.Getenv("FIMRA_API_INTEGRATION") != "1" {
		t.Skip("Run make api-test-integration with the development stack running and migrated.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cfg := loadConfig()
	pool, err := openPool(ctx, cfg.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := ensureDemoSeed(ctx, pool); err != nil {
		t.Fatalf("load the demo seed: %v", err)
	}
	provider, err := discoverProvider(ctx, cfg.issuer)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	tokens := map[string]string{}
	for _, user := range []string{"state.admin", "division.officer", "beat.guard", "no.role"} {
		tokens[user] = demoToken(t, client, provider.Endpoint().TokenURL, user)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	source := fmt.Sprintf("forms_%08x", rand.Uint32())
	if counts, err := runIngest(ctx, tx, ingestRun{Source: source, Product: demoProduct}, func(context.Context) ([]firms.Observation, error) {
		return readFixture(filepath.Join(fixtureDir, "demo.csv"), demoProduct, true, time.Now())
	}); err != nil || counts.Inserted == 0 {
		t.Fatalf("load synthetic detections: %+v %v", counts, err)
	}
	store, err := newLocalAttachments(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const maxUpload = 1 << 20
	handler := api{reader: tx, feedDB: tx, logger: slog.New(slog.NewTextHandler(testLog{t}, nil)),
		attachments: attachmentSettings{store: store, maxBytes: maxUpload}}.handler(newAuthenticator(provider, cfg.issuer, cfg.audience), devOrigins)

	const base = "/api/v1/workspaces/demo"
	type answer struct {
		code   int
		body   []byte
		header http.Header
	}
	send := func(method, path, user, contentType string, body []byte) answer {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		if user != "" {
			r.Header.Set("Authorization", "Bearer "+tokens[user])
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return answer{w.Code, w.Body.Bytes(), w.Header()}
	}
	call := func(method, path, user string, body any, want int) answer {
		t.Helper()
		var data []byte
		switch b := body.(type) {
		case nil:
		case []byte:
			data = b
		case string:
			data = []byte(b)
		default:
			data, _ = json.Marshal(b)
		}
		got := send(method, path, user, "application/json", data)
		if got.code != want {
			t.Fatalf("%s %s as %s: HTTP %d, want %d: %s", method, path, user, got.code, want, got.body)
		}
		return got
	}
	has := func(body []byte, fragment string) bool {
		var compact bytes.Buffer
		return json.Compact(&compact, body) == nil && bytes.Contains(compact.Bytes(), []byte(fragment))
	}
	example := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(formExamples, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	// Only a workspace administrator at the root manages forms.
	firstReport := example("first-report")
	for user, status := range map[string]int{"division.officer": 403, "beat.guard": 403, "no.role": 403} {
		call("POST", "/forms", user, firstReport, status)
	}
	bad := call("POST", "/forms", "state.admin", `{"formatVersion":1,"id":"Bad","version":1,"title":{"en":"x"},"sections":[]}`, 422)
	var invalid struct{ Problems []string }
	if json.Unmarshal(bad.body, &invalid) != nil || len(invalid.Problems) == 0 {
		t.Fatalf("invalid form problems: %s", bad.body)
	}
	created := call("POST", "/forms", "state.admin", firstReport, 201)
	if created.header.Get("Location") != base+"/forms/first-report/versions/1" {
		t.Fatalf("location %q", created.header.Get("Location"))
	}
	call("POST", "/forms", "state.admin", firstReport, 409)
	// A draft is hidden from field staff and can be changed.
	if got := call("GET", "/forms", "beat.guard", nil, 200); string(bytes.TrimSpace(got.body)) != "[]" {
		t.Fatalf("beat guard sees drafts: %s", got.body)
	}
	call("GET", "/forms/first-report/versions/1", "beat.guard", nil, 404)
	edited := bytes.Replace(firstReport, []byte(`"First report"`), []byte(`"First report from the site"`), 1)
	call("PUT", "/forms/first-report/versions/1", "state.admin", edited, 200)
	call("PUT", "/forms/first-report/versions/2", "state.admin", edited, 422)
	call("POST", "/forms/first-report/versions/1/publish", "division.officer", nil, 403)
	published := call("POST", "/forms/first-report/versions/1/publish", "state.admin", nil, 200)
	again := call("POST", "/forms/first-report/versions/1/publish", "state.admin", nil, 200)
	var version struct {
		Status      string
		PublishedAt *time.Time
		Views       map[string]string
		Definition  json.RawMessage
	}
	if json.Unmarshal(published.body, &version) != nil || version.Status != "published" || version.PublishedAt == nil ||
		version.Views["submissions"] == "" || version.Views["teams"] == "" || !bytes.Equal(published.body, again.body) {
		t.Fatalf("published version: %s", published.body)
	}
	call("PUT", "/forms/first-report/versions/1", "state.admin", edited, 409)
	got := call("GET", "/forms/first-report/versions/1", "beat.guard", nil, 200)
	if json.Unmarshal(got.body, &version) != nil || !bytes.Contains(version.Definition, []byte("First report from the site")) {
		t.Fatalf("beat guard reads the published version: %s", got.body)
	}
	// The database refuses any change to a published version.
	for _, sql := range []string{`UPDATE form_versions SET source = source || ' ' WHERE published_at IS NOT NULL`,
		`DELETE FROM form_versions WHERE published_at IS NOT NULL`} {
		err := pgx.BeginFunc(ctx, tx, func(save pgx.Tx) error { _, err := save.Exec(ctx, sql); return err })
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("published versions are frozen: %v", err)
		}
	}
	// Nested repeating groups get one view each.
	publishExample := func(name string) answer {
		t.Helper()
		location := call("POST", "/forms", "state.admin", example(name), 201).header.Get("Location")
		return call("POST", strings.TrimPrefix(location, base)+"/publish", "state.admin", nil, 200)
	}
	tour := publishExample("field-type-tour")
	if !has(tour.body, `"shifts":"form_views.f`) {
		t.Fatalf("field type tour views: %s", tour.body)
	}
	publishExample("control-room-dispatch")

	// Assignments: a draft cannot be assigned, a staged form only at its stages.
	call("PUT", "/forms/first-report/assignments/first_report", "division.officer", map[string]any{"version": 1}, 403)
	call("PUT", "/forms/first-report/assignments/first_report", "state.admin", map[string]any{"version": 2}, 422)
	call("PUT", "/forms/first-report/assignments/no_such_step", "state.admin", map[string]any{"version": 1}, 404)
	call("PUT", "/forms/control-room-dispatch/assignments/first_report", "state.admin", map[string]any{"version": 1}, 422)
	call("PUT", "/forms/control-room-dispatch/assignments/first_report", "state.admin", map[string]any{"version": 2}, 422)
	call("PUT", "/forms/first-report/assignments/first_report", "state.admin", map[string]any{"version": 1, "hierarchy": "territorial", "unitPaths": []string{"ds.c9"}}, 422)
	call("PUT", "/forms/first-report/assignments/first_report", "state.admin", map[string]any{"version": 1, "hierarchy": "territorial", "unitPaths": []string{"ds.c1.d2", "ds.c1.d1"}}, 200)
	list := call("GET", "/forms", "beat.guard", nil, 200)
	if !has(list.body, `"unitPaths":["ds.c1.d1","ds.c1.d2"]`) {
		t.Fatalf("forms list: %s", list.body)
	}

	// The beat guard's alert is in beat 05 of Hill Division. Alert B is in
	// the other assigned division and alert C outside both.
	type alertRow struct {
		id       string
		lon, lat float64
	}
	pick := func(where string) alertRow {
		t.Helper()
		var a alertRow
		err := tx.QueryRow(ctx, `SELECT a.id::text, ST_X(d.geom), ST_Y(d.geom) FROM alerts a
JOIN detections d ON (d.id, d.observed_at) = (a.detection_id, a.observed_at)
WHERE d.source = $1 AND a.workspace_id = 'd0000000-0000-4000-8000-000000000001' AND a.queue = 'unit' AND `+where+`
ORDER BY a.id LIMIT 1`, source).Scan(&a.id, &a.lon, &a.lat)
		if err != nil {
			t.Fatalf("pick alert %s: %v", where, err)
		}
		return a
	}
	alertA := pick(`a.unit_path = 'ds.c1.d1.r2.b05'`)
	alertB := pick(`a.unit_path <@ 'ds.c1.d2'`)
	alertC := pick(`a.unit_path <@ 'ds.c2'`)
	if _, err := tx.Exec(ctx, `UPDATE role_assignments SET unit_path = 'ds.c1.d1.r2.b05'
WHERE workspace_id = 'd0000000-0000-4000-8000-000000000001' AND role_slug = 'field_staff'`); err != nil {
		t.Fatal(err)
	}
	advance := func(a alertRow, user string, steps ...string) {
		t.Helper()
		for _, step := range steps {
			call("POST", "/alerts/"+a.id+"/steps", user, map[string]any{"step": step,
				"location": map[string]float64{"longitude": a.lon, "latitude": a.lat}}, 200)
		}
	}
	toExtinguished := []string{"acknowledged", "team_sent", "reached_site", "controlled", "extinguished"}

	// Without an assigned form, report steps behave as before.
	advance(alertC, "state.admin", append(toExtinguished, "first_report", "final_report")...)
	if got := call("GET", "/alerts/"+alertC.id+"/forms", "state.admin", nil, 200); !has(got.body, `"forms":[]`) {
		t.Fatalf("alert outside the assigned units has forms: %s", got.body)
	}

	advance(alertA, "beat.guard", toExtinguished...)

	// Attachments follow the alert's permissions.
	path := "/alerts/" + alertA.id + "/attachments/"
	photoID, signatureID := "5ca1ab1e-0000-4000-8000-000000000001", "5ca1ab1e-0000-4000-8000-000000000002"
	photo := append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte("synthetic"), 50)...)
	signature := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("synthetic"), 20)...)
	upload := func(id, user, name, contentType string, body []byte, want int) answer {
		t.Helper()
		got := send("PUT", path+id+"?name="+name, user, contentType, body)
		if got.code != want {
			t.Fatalf("upload %s as %s: HTTP %d, want %d: %s", name, user, got.code, want, got.body)
		}
		return got
	}
	upload(photoID, "no.role", "site.jpg", "image/jpeg", photo, 404)
	first := upload(photoID, "beat.guard", "site.jpg", "image/jpeg", photo, 201)
	if retry := upload(photoID, "beat.guard", "site.jpg", "image/jpeg", photo, 200); !bytes.Equal(first.body, retry.body) {
		t.Fatal("a retried upload changed its receipt")
	}
	// The phone lost its connection after the first photo was stored. It
	// checks its receipt first and sends only the remaining photos.
	if got := call("GET", path+photoID, "beat.guard", nil, 200); !bytes.Equal(first.body, got.body) {
		t.Fatalf("receipt does not match the saved photo: %s", got.body)
	}
	call("GET", path+photoID, "no.role", nil, 404)
	call("GET", path+photoID, "division.officer", nil, 404)
	remaining := []string{"5ca1ab1e-0000-4000-8000-000000000007", "5ca1ab1e-0000-4000-8000-000000000008"}
	for _, id := range remaining {
		call("GET", path+id, "beat.guard", nil, 404)
	}
	upload(photoID, "beat.guard", "site.jpg", "image/jpeg", append(photo, 'x'), 409)
	upload(signatureID, "beat.guard", "sign.png", "image/png", signature, 201)
	upload("5ca1ab1e-0000-4000-8000-000000000003", "beat.guard", "fake.jpg", "image/jpeg", []byte("plain text, not a photo"), 415)
	upload("5ca1ab1e-0000-4000-8000-000000000004", "beat.guard", "page.html", "text/html", []byte("<html>"), 415)
	upload("5ca1ab1e-0000-4000-8000-000000000005", "beat.guard", "big.jpg", "image/jpeg", append([]byte("\xff\xd8\xff"), make([]byte, maxUpload)...), 413)
	upload("5CA1AB1E-0000-4000-8000-000000000006", "beat.guard", "site.jpg", "image/jpeg", photo, 400)

	download := func(user string, want int) {
		t.Helper()
		got := send("GET", "/attachments/"+photoID, user, "", nil)
		if got.code != want {
			t.Fatalf("download as %s: HTTP %d, want %d", user, got.code, want)
		}
		if want == 200 && (!bytes.Equal(got.body, photo) || got.header.Get("X-Content-Type-Options") != "nosniff" ||
			!strings.HasPrefix(got.header.Get("Content-Disposition"), "attachment;") || got.header.Get("Content-Type") != "image/jpeg") {
			t.Fatalf("download as %s: %v", user, got.header)
		}
	}
	for user, status := range map[string]int{"state.admin": 200, "division.officer": 200, "beat.guard": 200, "no.role": 404} {
		download(user, status)
	}

	// A submission needs the alert to have reached the form's step.
	values := map[string]any{
		"reached_at": time.Now().Add(-time.Hour).Format(time.RFC3339), "fire_status": "controlled",
		"cause_group": "natural", "cause": "lightning", "estimated_area": 1.25, "vegetation": []string{"grass"},
		"site_photos": append([]string{photoID}, remaining...), "reporter_signature": signatureID,
		"teams": []map[string]any{{"team_name": "Demo Team A", "members": 6}, {"team_name": "Demo Team B", "members": 4}},
	}
	submission := map[string]any{"id": "5ca1ab1e-0000-4000-8000-0000000000a1", "form": "first-report", "version": 1,
		"values": values, "deviceTime": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		"location": map[string]float64{"longitude": alertA.lon, "latitude": alertA.lat}}
	submissions := "/alerts/" + alertA.id + "/submissions"
	call("POST", submissions, "beat.guard", submission, 409)
	advance(alertA, "beat.guard", "first_report")
	call("POST", "/alerts/"+alertA.id+"/steps", "beat.guard", map[string]any{"step": "final_report"}, 422)
	if got := call("GET", "/alerts/"+alertA.id+"/forms", "beat.guard", nil, 200); !has(got.body, `"requiredNow":true`) {
		t.Fatalf("required form not listed: %s", got.body)
	}
	call("POST", submissions, "no.role", submission, 404)
	// A report referring to photos still in the outbox is never stored in part.
	call("POST", submissions, "beat.guard", submission, 422)
	for i, id := range remaining {
		upload(id, "beat.guard", fmt.Sprintf("site-%d.jpg", i+2), "image/jpeg", append(bytes.Clone(photo), byte(i)), 201)
	}
	wrong := map[string]any{}
	for k, v := range submission {
		wrong[k] = v
	}
	wrong["id"] = "5ca1ab1e-0000-4000-8000-0000000000a2"
	wrong["values"] = map[string]any{"fire_status": "smouldering"}
	rejected := call("POST", submissions, "beat.guard", wrong, 422)
	if json.Unmarshal(rejected.body, &invalid) != nil || len(invalid.Problems) == 0 {
		t.Fatalf("rejected submission: %s", rejected.body)
	}
	receipt := call("POST", submissions, "beat.guard", submission, 201)
	if replay := call("POST", submissions, "beat.guard", submission, 200); !bytes.Equal(receipt.body, replay.body) {
		t.Fatal("a retried submission changed its receipt")
	}
	changed := map[string]any{}
	for k, v := range submission {
		changed[k] = v
	}
	changed["deviceTime"] = time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)
	call("POST", submissions, "beat.guard", changed, 409)
	var stored struct {
		ID     string
		Step   string
		Values map[string]any
	}
	if json.Unmarshal(receipt.body, &stored) != nil || stored.Step != "first_report" || stored.Values["total_staff"] != 10.0 ||
		stored.Values["alert_location"] == nil {
		t.Fatalf("submission receipt: %s", receipt.body)
	}
	resumed := call("GET", submissions, "beat.guard", nil, 200)
	var reports []struct {
		ID     string
		Values struct {
			SitePhotos []string `json:"site_photos"`
		}
	}
	if err := json.Unmarshal(resumed.body, &reports); err != nil || len(reports) != 1 ||
		reports[0].ID != stored.ID || !slices.Equal(reports[0].Values.SitePhotos, append([]string{photoID}, remaining...)) {
		t.Fatalf("expected one resumed report with three photos: %s", resumed.body)
	}
	var linked int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM submission_attachments
WHERE submission_id = $1 AND field = 'site_photos'`, stored.ID).Scan(&linked); err != nil || linked != 3 {
		t.Fatalf("expected three linked photos: %d, %v", linked, err)
	}
	for user, status := range map[string]int{"state.admin": 200, "division.officer": 200, "beat.guard": 200, "no.role": 404} {
		call("GET", "/submissions/"+stored.ID, user, nil, status)
		call("GET", submissions, user, nil, map[int]int{200: 200, 404: 404}[status])
	}

	// The submissions list shares the alert queue's rules and filters. Only a
	// supervising officer on the alert may approve.
	type listedSubmission struct {
		ID         string
		AlertID    string `json:"alert_id"`
		Form       string
		Decision   string
		CanApprove bool `json:"can_approve"`
	}
	listSubmissions := func(user, query string, want int) (map[string]int, []listedSubmission) {
		t.Helper()
		got := call("GET", "/submissions"+query, user, nil, want)
		var page struct {
			Counts map[string]int
			Items  []listedSubmission
		}
		if want == 200 && json.Unmarshal(got.body, &page) != nil {
			t.Fatalf("submissions list as %s: %s", user, got.body)
		}
		return page.Counts, page.Items
	}
	for user, approve := range map[string]bool{"state.admin": true, "division.officer": true, "beat.guard": false} {
		counts, items := listSubmissions(user, "?form=first-report&decision=pending", 200)
		if len(items) != 1 || items[0].ID != stored.ID || items[0].AlertID != alertA.id || items[0].CanApprove != approve || counts["pending"] != 1 {
			t.Fatalf("submissions list as %s: %v %+v", user, counts, items)
		}
		got := call("GET", "/submissions/"+stored.ID, user, nil, 200)
		if !has(got.body, fmt.Sprintf(`"canApprove":%v`, approve)) {
			t.Fatalf("submission as %s: %s", user, got.body)
		}
		if _, items := listSubmissions(user, "?unitPath=ds.c2", 200); len(items) != 0 {
			t.Fatalf("unit path filter as %s: %+v", user, items)
		}
	}
	listSubmissions("no.role", "", 403)
	listSubmissions("beat.guard", "?decision=maybe", 400)
	listSubmissions("beat.guard", "?cursor=2026-01-01T00:00:00Z,12", 400)
	if _, items := listSubmissions("division.officer", "?decision=approved", 200); len(items) != 0 {
		t.Fatalf("approved before any decision: %+v", items)
	}

	// The first report moves the alert to the next step.
	advance(alertA, "beat.guard", "final_report")

	// The flat view has the form's columns.
	var view, teamsView string
	if err := tx.QueryRow(ctx, `SELECT max(view_name) FILTER (WHERE repeat_key = ''), max(view_name) FILTER (WHERE repeat_key = 'teams')
FROM form_version_views fv JOIN form_versions v ON v.id = fv.form_version_id JOIN forms f ON f.id = v.form_id
WHERE f.form_key = 'first-report' AND f.workspace_id = 'd0000000-0000-4000-8000-000000000001'`).Scan(&view, &teamsView); err != nil {
		t.Fatal(err)
	}
	var row struct {
		alert                string
		status, cause        string
		area, staff          float64
		vegetation           []string
		photos               []string
		signature            string
		reached              time.Time
		hasLocation, hasMeta bool
	}
	err = tx.QueryRow(ctx, `SELECT _alert_id::text, fire_status, cause, estimated_area::float8, total_staff::float8, vegetation,
    site_photos::text[], reporter_signature::text, reached_at, ST_GeometryType(alert_location) = 'ST_Point',
    _submitted_by IS NOT NULL AND _received_at IS NOT NULL AND _location IS NOT NULL AND _step = 'first_report'
FROM form_views.`+pgx.Identifier{view}.Sanitize()+` WHERE _submission_id = $1`, stored.ID).Scan(
		&row.alert, &row.status, &row.cause, &row.area, &row.staff, &row.vegetation, &row.photos, &row.signature, &row.reached,
		&row.hasLocation, &row.hasMeta)
	if err != nil || row.alert != alertA.id || row.status != "controlled" || row.cause != "lightning" || row.area != 1.25 ||
		row.staff != 10 || len(row.vegetation) != 1 || row.photos[0] != photoID || row.signature != signatureID ||
		row.reached.IsZero() || !row.hasLocation || !row.hasMeta {
		t.Fatalf("flat view row %+v: %v", row, err)
	}
	var teams int
	var members float64
	if err := tx.QueryRow(ctx, `SELECT count(*), sum(members)::float8 FROM form_views.`+pgx.Identifier{teamsView}.Sanitize()+
		` WHERE _submission_id = $1 AND _entry IN ('1', '2') AND _parent_entry IS NULL`, stored.ID).Scan(&teams, &members); err != nil || teams != 2 || members != 10 {
		t.Fatalf("teams view: %d %v %v", teams, members, err)
	}

	// Approvals: a supervising officer on the alert decides once.
	approval := "/submissions/" + stored.ID + "/approval"
	call("POST", approval, "beat.guard", map[string]string{"decision": "approved"}, 403)
	call("POST", approval, "no.role", map[string]string{"decision": "approved"}, 404)
	call("POST", approval, "division.officer", map[string]string{"decision": "returned"}, 422)
	decided := call("POST", approval, "division.officer", map[string]string{"decision": "approved", "note": "Checked by radio."}, 200)
	if !has(decided.body, `"decision":"approved"`) {
		t.Fatalf("approval: %s", decided.body)
	}
	call("POST", approval, "division.officer", map[string]string{"decision": "approved", "note": "Checked by radio."}, 200)
	call("POST", approval, "state.admin", map[string]string{"decision": "returned", "note": "Recount the teams."}, 409)
	if counts, items := listSubmissions("division.officer", "?decision=approved", 200); len(items) != 1 || items[0].Decision != "approved" ||
		counts["approved"] != 1 || counts["pending"] != 0 {
		t.Fatalf("approved list: %v %+v", counts, items)
	}

	// A returned report no longer counts: the step waits for a new one.
	advance(alertB, "state.admin", append(toExtinguished, "first_report")...)
	upload2 := func(id, name, contentType string, body []byte) {
		t.Helper()
		got := send("PUT", "/alerts/"+alertB.id+"/attachments/"+id+"?name="+name, "state.admin", contentType, body)
		if got.code != 201 {
			t.Fatalf("upload to alert B: %d %s", got.code, got.body)
		}
	}
	upload2("5ca1ab1e-0000-4000-8000-0000000000b1", "b.jpg", "image/jpeg", photo)
	upload2("5ca1ab1e-0000-4000-8000-0000000000b2", "b.png", "image/png", signature)
	valuesB := map[string]any{}
	for k, v := range values {
		valuesB[k] = v
	}
	valuesB["site_photos"] = []string{"5ca1ab1e-0000-4000-8000-0000000000b1"}
	valuesB["reporter_signature"] = "5ca1ab1e-0000-4000-8000-0000000000b2"
	// Photos uploaded for one alert cannot be used on another.
	call("POST", "/alerts/"+alertB.id+"/submissions", "state.admin", map[string]any{"id": "5ca1ab1e-0000-4000-8000-0000000000b8",
		"form": "first-report", "version": 1, "values": values}, 422)
	call("POST", "/alerts/"+alertB.id+"/submissions", "state.admin", map[string]any{"id": "5ca1ab1e-0000-4000-8000-0000000000b9",
		"form": "first-report", "version": 1, "values": valuesB}, 201)
	call("POST", "/submissions/5ca1ab1e-0000-4000-8000-0000000000b9/approval", "division.officer",
		map[string]string{"decision": "returned", "note": "Add the second team."}, 404)
	call("POST", "/submissions/5ca1ab1e-0000-4000-8000-0000000000b9/approval", "state.admin",
		map[string]string{"decision": "returned", "note": "Add the second team."}, 200)
	call("POST", "/alerts/"+alertB.id+"/steps", "state.admin", map[string]any{"step": "final_report"}, 422)
	// An officer can still end the workflow with an outcome.
	if got := call("GET", "/alerts/"+alertB.id+"/history", "state.admin", nil, 200); !bytes.Contains(got.body, []byte("false_alarm")) {
		t.Fatalf("outcomes missing: %s", got.body)
	}
	call("POST", "/alerts/"+alertB.id+"/submissions", "state.admin", map[string]any{"id": "5ca1ab1e-0000-4000-8000-0000000000ba",
		"form": "first-report", "version": 1, "values": valuesB}, 201)
	advance(alertB, "state.admin", "final_report")

	// When the beat guard's grant moves away, the attachment goes with the alert.
	if _, err := tx.Exec(ctx, `UPDATE role_assignments SET unit_path = 'ds.c1.d1.r1.b01'
WHERE workspace_id = 'd0000000-0000-4000-8000-000000000001' AND role_slug = 'field_staff'`); err != nil {
		t.Fatal(err)
	}
	download("beat.guard", 404)
	call("GET", "/submissions/"+stored.ID, "beat.guard", nil, 404)

	// Everything is in the audit log, and the records are append-only.
	var audit map[string]int
	if err := tx.QueryRow(ctx, `SELECT jsonb_object_agg(action, n) FROM (SELECT action, count(*) n FROM audit_log
WHERE workspace_id = 'd0000000-0000-4000-8000-000000000001' AND action IN ('form.publish', 'form.assign', 'submission.create',
  'submission.approve', 'submission.return', 'attachment.upload', 'attachment.download')
  AND occurred_at >= now() GROUP BY action) x`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"form.publish": 3, "form.assign": 1, "submission.create": 3, "submission.approve": 1,
		"submission.return": 1, "attachment.upload": 6, "attachment.download": 3}
	for action, n := range want {
		if audit[action] != n {
			t.Errorf("audit %s: %d rows, want %d (all: %v)", action, audit[action], n, audit)
		}
	}
	for _, sql := range []string{`UPDATE submissions SET answers = '{}'`, `DELETE FROM approvals`, `UPDATE attachments SET file_name = 'x'`,
		`DELETE FROM submission_attachments`} {
		err := pgx.BeginFunc(ctx, tx, func(save pgx.Tx) error { _, err := save.Exec(ctx, sql); return err })
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Log("forms published and frozen; first report gated and accepted; flat views read; attachments and approvals checked for every demo user")
}

// testLog shows the API's log lines with the test's output.
type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}
