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
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// TestFormTemplatesIntegration takes a form through the template levels: a
// national template arrives, a state extends it and publishes the result,
// and a division adds to the state's form. On the way it checks who may do
// what, that locked parts cannot change, and that a submission made before
// the state's form still shows with the version it used. Everything runs in
// one transaction on the demo workspace and is rolled back at the end.
func TestFormTemplatesIntegration(t *testing.T) {
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
	source := fmt.Sprintf("templates_%08x", rand.Uint32())
	if counts, err := runIngest(ctx, tx, ingestRun{Source: source, Product: demoProduct}, func(context.Context) ([]firms.Observation, error) {
		return readFixture(filepath.Join(fixtureDir, "demo.csv"), demoProduct, true, time.Now())
	}); err != nil || counts.Inserted == 0 {
		t.Fatalf("load synthetic detections: %+v %v", counts, err)
	}
	store, err := newLocalAttachments(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := api{reader: tx, feedDB: tx, logger: slog.New(slog.NewTextHandler(testLog{t}, nil)),
		attachments: attachmentSettings{store: store, maxBytes: 1 << 20}}.handler(newAuthenticator(provider, cfg.issuer, cfg.audience), devOrigins)

	const base = "/api/v1/workspaces/demo"
	call := func(method, path, user string, body any, want int) []byte {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		r := httptest.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+tokens[user])
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s as %s: HTTP %d, want %d: %s", method, path, user, w.Code, want, w.Body.Bytes())
		}
		return w.Body.Bytes()
	}
	type version struct {
		Form    string
		Version int
		Level   string
		Status  string
		Unit    *struct{ Hierarchy, Path string }
		Extends *struct {
			Form, Level string
			Version     int
		}
		Template templateRules
		Problems []string
		// Only for the people who manage the form.
		SubmissionCount *int
		Definition      json.RawMessage
	}
	read := func(body []byte) version {
		t.Helper()
		var v version
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		return v
	}

	// The national template is the shared synthetic parent of the
	// extension test cases: two sections, a shared list and a group.
	cases := readExtensionCases(t)
	form := func(id string, version int, edit func(f map[string]any)) map[string]any {
		var f map[string]any
		if err := json.Unmarshal(cases.Parent, &f); err != nil {
			t.Fatal(err)
		}
		f["id"], f["version"] = id, version
		if edit != nil {
			edit(f)
		}
		return f
	}
	sections := func(f map[string]any) []any { return f["sections"].([]any) }
	items := func(section any) []any { return section.(map[string]any)["items"].([]any) }
	national := map[string]any{"level": "national", "definition": form("national-fire-report", 1, nil),
		"template": templateRules{AllowExtension: true, Locked: []string{"basics", "crew_size"}}}

	// Only a workspace administrator at the root adds national templates.
	for _, user := range []string{"division.officer", "beat.guard", "no.role"} {
		call("POST", "/forms", user, national, 403)
	}
	call("POST", "/forms", "state.admin", map[string]any{"level": "national", "definition": national["definition"],
		"template": map[string]any{"allowExtension": true, "locked": []string{"fire"}}}, 422)
	added := read(call("POST", "/forms", "state.admin", national, 201))
	if added.Level != "national" || added.Status != "published" || added.SubmissionCount == nil || *added.SubmissionCount != 0 {
		t.Fatalf("a national template is published on arrival: %+v", added)
	}
	// It never changes in the workspace, and does not extend anything.
	call("PUT", "/forms/national-fire-report/versions/1", "state.admin", national, 409)
	call("POST", "/forms", "state.admin", map[string]any{"level": "national", "definition": form("national-fire-report", 2, nil),
		"extends": formRef{"national-fire-report", 1}}, 422)
	call("POST", "/forms", "state.admin", map[string]any{"level": "national", "definition": form("closed-template", 1, nil)}, 201)
	// A national template can be used as it is. Beat guards read it.
	if got := read(call("GET", "/forms/national-fire-report/versions/1", "beat.guard", nil, 200)); got.SubmissionCount != nil || got.Status != "published" {
		t.Fatalf("beat guard reads the national template: %+v", got)
	}

	// An alert in beat 05 of Hill Division reaches the first report step
	// with the national template assigned, and the beat guard reports on it.
	if _, err := tx.Exec(ctx, `UPDATE role_assignments SET unit_path = 'ds.c1.d1.r2.b05'
WHERE workspace_id = 'd0000000-0000-4000-8000-000000000001' AND role_slug = 'field_staff'`); err != nil {
		t.Fatal(err)
	}
	var alertID string
	var lon, lat float64
	if err := tx.QueryRow(ctx, `SELECT a.id::text, ST_X(d.geom), ST_Y(d.geom) FROM alerts a
JOIN detections d ON (d.id, d.observed_at) = (a.detection_id, a.observed_at)
WHERE d.source = $1 AND a.workspace_id = 'd0000000-0000-4000-8000-000000000001' AND a.queue = 'unit' AND a.unit_path = 'ds.c1.d1.r2.b05' ORDER BY a.id LIMIT 1`, source).Scan(&alertID, &lon, &lat); err != nil {
		t.Fatal(err)
	}
	call("PUT", "/forms/national-fire-report/assignments/first_report", "state.admin", map[string]any{"version": 1}, 200)
	for _, step := range []string{"acknowledged", "team_sent", "reached_site", "controlled", "extinguished", "first_report"} {
		call("POST", "/alerts/"+alertID+"/steps", "beat.guard", map[string]any{"step": step,
			"location": map[string]float64{"longitude": lon, "latitude": lat}}, 200)
	}
	submit := func(id, formKey string, version int, values map[string]any) {
		t.Helper()
		call("POST", "/alerts/"+alertID+"/submissions", "beat.guard", map[string]any{"id": id, "form": formKey, "version": version,
			"step": "first_report", "values": values}, 201)
	}
	reached := time.Now().Add(-time.Hour).Format(time.RFC3339)
	const earlier = "7e3a1a7e-0000-4000-8000-0000000000b1"
	submit(earlier, "national-fire-report", 1, map[string]any{"reached_at": reached, "cause": "lightning", "crew_size": 5})

	// The state extends the national template as its own form. Locked
	// parts must stay as they are; everything else can change.
	stateForm := func(version int, edit func(f map[string]any)) map[string]any {
		return form("demo-fire-report", version, func(f map[string]any) {
			f["title"] = map[string]any{"en": "Demo State fire report"}
			f["sections"] = append(sections(f), map[string]any{"key": "demo_state", "title": map[string]any{"en": "Demo State details"},
				"items": []any{map[string]any{"key": "range_office", "type": "text", "label": map[string]any{"en": "Range office"}}}})
			if edit != nil {
				edit(f)
			}
		})
	}
	extend := func(def map[string]any, rules any) map[string]any {
		return map[string]any{"level": "state", "extends": formRef{"national-fire-report", 1}, "definition": def, "template": rules}
	}
	stateRules := templateRules{AllowExtension: true, Locked: []string{"demo_state"}}
	call("POST", "/forms", "division.officer", extend(stateForm(1, nil), stateRules), 403)
	changed := read(call("POST", "/forms", "state.admin", extend(stateForm(1, func(f map[string]any) {
		items(sections(f)[0])[0].(map[string]any)["label"] = map[string]any{"en": "Arrived at"}
	}), stateRules), 422))
	if !slices.Equal(changed.Problems, []string{`Section "basics" is locked by the template it extends and cannot change.`}) {
		t.Fatalf("locked parts: %q", changed.Problems)
	}
	removed := read(call("POST", "/forms", "state.admin", extend(stateForm(1, func(f map[string]any) {
		f["sections"] = append([]any{sections(f)[0]}, sections(f)[2])
	}), stateRules), 422))
	if !slices.Equal(removed.Problems, []string{`Field "crew_size" is locked by the template it extends and cannot be removed.`}) {
		t.Fatalf("removed locked field: %q", removed.Problems)
	}
	call("POST", "/forms", "state.admin", map[string]any{"level": "state", "extends": formRef{"closed-template", 1},
		"definition": form("closed-copy", 1, nil)}, 422)
	call("POST", "/forms", "state.admin", map[string]any{"level": "state", "extends": formRef{"national-fire-report", 9},
		"definition": form("missing-parent", 1, nil)}, 422)
	draft := read(call("POST", "/forms", "state.admin", extend(stateForm(1, func(f map[string]any) {
		// Unlocked parts can change: the notes field goes, a title changes.
		extra := sections(f)[1].(map[string]any)
		extra["title"] = map[string]any{"en": "Other details"}
		extra["items"] = items(extra)[1:]
	}), stateRules), 201))
	if draft.Status != "draft" || draft.Extends == nil || draft.Extends.Form != "national-fire-report" || draft.Extends.Version != 1 ||
		!slices.Equal(draft.Template.Locked, []string{"basics", "crew_size", "demo_state"}) {
		t.Fatalf("state draft: %+v", draft)
	}
	// Drafts stay hidden from people who do not manage the form.
	call("GET", "/forms/demo-fire-report/versions/1", "beat.guard", nil, 404)
	call("GET", "/forms/demo-fire-report/versions/1", "division.officer", nil, 404)
	call("POST", "/forms/demo-fire-report/versions/1/publish", "division.officer", nil, 403)
	published := read(call("POST", "/forms/demo-fire-report/versions/1/publish", "state.admin", nil, 200))
	if published.Status != "published" || published.Extends == nil {
		t.Fatalf("published state form: %+v", published)
	}

	// The state's form replaces the national one on the step.
	call("DELETE", "/forms/national-fire-report/assignments/first_report", "state.admin", nil, 204)
	call("PUT", "/forms/demo-fire-report/assignments/first_report", "state.admin", map[string]any{"version": 1}, 200)
	const later = "7e3a1a7e-0000-4000-8000-0000000000b2"
	submit(later, "demo-fire-report", 1, map[string]any{"reached_at": reached, "cause": "people", "crew_size": 3, "range_office": "Demo Range"})

	// A new state version keeps what the national template locked.
	call("POST", "/forms", "state.admin", extend(stateForm(2, func(f map[string]any) {
		f["sections"] = sections(f)[:1]
	}), stateRules), 422)
	call("POST", "/forms", "state.admin", extend(stateForm(2, func(f map[string]any) {
		// The state's own locks bind the forms that extend it, not the state.
		own := sections(f)[2].(map[string]any)
		own["items"] = append(items(own), map[string]any{"key": "beat_name", "type": "text", "label": map[string]any{"en": "Beat"}})
	}), stateRules), 201)
	call("POST", "/forms/demo-fire-report/versions/2/publish", "state.admin", nil, 200)

	// The earlier submissions still show with the versions they used, and
	// those versions have not changed.
	for id, want := range map[string]formRef{earlier: {"national-fire-report", 1}, later: {"demo-fire-report", 1}} {
		var got formRef
		if err := json.Unmarshal(call("GET", "/submissions/"+id, "division.officer", nil, 200), &got); err != nil || got != want {
			t.Fatalf("submission %s shows %+v, want %+v", id, got, want)
		}
	}
	if got := read(call("GET", "/forms/national-fire-report/versions/1", "state.admin", nil, 200)); !jsonEqual(got.Definition, national["definition"]) ||
		got.SubmissionCount == nil || *got.SubmissionCount != 1 {
		t.Fatalf("the national template changed or lost its submission: %+v", got)
	}
	if got := read(call("GET", "/forms/demo-fire-report/versions/1", "state.admin", nil, 200)); got.SubmissionCount == nil || *got.SubmissionCount != 1 {
		t.Fatalf("state version 1 lost its submission: %+v", got)
	}

	// A division adds to the state's form for its own unit.
	division := func(path string, extends formRef, edit func(f map[string]any)) map[string]any {
		def := stateForm(1, func(f map[string]any) {
			f["id"] = "hill-fire-report"
			f["sections"] = append(sections(f), map[string]any{"key": "hill", "title": map[string]any{"en": "Hill Division details"},
				"items": []any{map[string]any{"key": "watch_tower", "type": "text", "label": map[string]any{"en": "Nearest watch tower"}}}})
			if edit != nil {
				edit(f)
			}
		})
		return map[string]any{"level": "division", "hierarchy": "territorial", "unitPath": path, "extends": extends, "definition": def}
	}
	call("POST", "/forms", "division.officer", division("ds.c1.d2", formRef{"demo-fire-report", 1}, nil), 403)
	call("POST", "/forms", "beat.guard", division("ds.c1.d1", formRef{"demo-fire-report", 1}, nil), 403)
	call("POST", "/forms", "division.officer", division("ds.c1.d1", formRef{"national-fire-report", 1}, nil), 422)
	call("POST", "/forms", "division.officer", map[string]any{"level": "division", "hierarchy": "territorial", "unitPath": "ds.c1.d1",
		"definition": division("ds.c1.d1", formRef{}, nil)["definition"]}, 422)
	locked := read(call("POST", "/forms", "division.officer", division("ds.c1.d1", formRef{"demo-fire-report", 1}, func(f map[string]any) {
		items(sections(f)[2])[0].(map[string]any)["hint"] = map[string]any{"en": "The office's name"}
	}), 422))
	if !slices.Equal(locked.Problems, []string{`Section "demo_state" is locked by the template it extends and cannot change.`}) {
		t.Fatalf("division changes a state lock: %q", locked.Problems)
	}
	list := read(call("POST", "/forms", "division.officer", division("ds.c1.d1", formRef{"demo-fire-report", 1}, func(f map[string]any) {
		causes := f["lists"].([]any)[0].(map[string]any)
		causes["choices"] = append(causes["choices"].([]any), map[string]any{"key": "unknown", "label": map[string]any{"en": "Not known"}})
	}), 422))
	if !slices.Equal(list.Problems, []string{`The list "causes" is used by a locked part of the template and cannot change.`}) {
		t.Fatalf("division changes a locked list: %q", list.Problems)
	}
	withRules := division("ds.c1.d1", formRef{"demo-fire-report", 1}, nil)
	withRules["template"] = templateRules{AllowExtension: true}
	call("POST", "/forms", "division.officer", withRules, 422)
	hill := read(call("POST", "/forms", "division.officer", division("ds.c1.d1", formRef{"demo-fire-report", 1}, nil), 201))
	if hill.Level != "division" || hill.Unit == nil || hill.Unit.Path != "ds.c1.d1" ||
		!slices.Equal(hill.Template.Locked, []string{"basics", "crew_size", "demo_state"}) {
		t.Fatalf("division draft: %+v", hill)
	}
	call("GET", "/forms/hill-fire-report/versions/1", "beat.guard", nil, 404)
	call("GET", "/forms/hill-fire-report/versions/1", "state.admin", nil, 200)
	// A plain form file keeps the draft's template settings.
	plain := division("ds.c1.d1", formRef{}, func(f map[string]any) { f["title"] = map[string]any{"en": "Hill Division fire report"} })["definition"]
	if got := read(call("PUT", "/forms/hill-fire-report/versions/1", "division.officer", plain, 200)); got.Extends == nil || got.Extends.Form != "demo-fire-report" {
		t.Fatalf("a plain update dropped the template settings: %+v", got)
	}
	call("POST", "/forms/hill-fire-report/versions/1/publish", "division.officer", nil, 200)
	call("GET", "/forms/hill-fire-report/versions/1", "beat.guard", nil, 200)
	// Assignment stays with the root administrator, under the division only.
	call("PUT", "/forms/hill-fire-report/assignments/first_report", "division.officer", map[string]any{"version": 1, "hierarchy": "territorial", "unitPaths": []string{"ds.c1.d1"}}, 403)
	call("PUT", "/forms/hill-fire-report/assignments/first_report", "state.admin", map[string]any{"version": 1}, 422)
	call("PUT", "/forms/hill-fire-report/assignments/first_report", "state.admin", map[string]any{"version": 1, "hierarchy": "territorial", "unitPaths": []string{"ds.c1.d2"}}, 422)
	call("PUT", "/forms/hill-fire-report/assignments/first_report", "state.admin", map[string]any{"version": 1, "hierarchy": "territorial", "unitPaths": []string{"ds.c1.d1.r2"}}, 200)

	// The list shows each level in order, with what each version extends.
	var forms []struct {
		Form      string
		Level     string
		CanManage bool
		Versions  []version
	}
	if err := json.Unmarshal(call("GET", "/forms", "division.officer", nil, 200), &forms); err != nil {
		t.Fatal(err)
	}
	var levels []string
	for _, f := range forms {
		if f.Form == "national-fire-report" || f.Form == "demo-fire-report" || f.Form == "hill-fire-report" {
			levels = append(levels, f.Level)
			if f.CanManage != (f.Form == "hill-fire-report") {
				t.Fatalf("division officer manages %s: %v", f.Form, f.CanManage)
			}
		}
	}
	if !slices.Equal(levels, []string{"national", "state", "division"}) {
		t.Fatalf("levels in the list: %v", levels)
	}

	// The database refuses what the API refuses.
	for _, sql := range []string{
		`UPDATE forms SET level = 'state' WHERE form_key = 'national-fire-report'`,
		`INSERT INTO form_versions (form_id, version, source, definition, columns, extends_version_id)
SELECT f.id, 9, '{"version":9}', '{"version":9}', '[]', (SELECT v.id FROM form_versions v JOIN forms c ON c.id = v.form_id
    WHERE c.form_key = 'closed-template') FROM forms f WHERE f.form_key = 'demo-fire-report'`,
	} {
		err := pgx.BeginFunc(ctx, tx, func(save pgx.Tx) error { _, err := save.Exec(ctx, sql); return err })
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("%s: %v", sql, err)
		}
	}
}

func jsonEqual(a json.RawMessage, b any) bool {
	var x, y any
	data, err := json.Marshal(b)
	if err != nil || json.Unmarshal(a, &x) != nil || json.Unmarshal(data, &y) != nil {
		return false
	}
	ax, _ := json.Marshal(x)
	ay, _ := json.Marshal(y)
	return bytes.Equal(ax, ay)
}
