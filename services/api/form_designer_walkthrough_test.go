package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// TestFormDesignerWalkthroughServer serves the real API for a browser
// walkthrough of the form designer. Before it serves, it loads the demo
// workspace, adds a synthetic national template and files one report on it
// as the demo beat guard, so the browser can check that the report keeps
// its version after the state extends the template. Run it with
// make form-designer-walkthrough, on a database of its own.
func TestFormDesignerWalkthroughServer(t *testing.T) {
	if os.Getenv("FIMRA_FORMS_WALKTHROUGH") != "1" {
		t.Skip("Run make form-designer-walkthrough.")
	}
	seconds, _ := strconv.Atoi(envOr("FIMRA_WALKTHROUGH_SECONDS", "600"))
	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(seconds)*time.Second)
	defer cancel()
	cfg := loadConfig()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := ensureDemoSeed(ctx, pool); err != nil {
		t.Fatalf("load the demo seed: %v", err)
	}
	if counts, err := runIngest(ctx, pool, ingestRun{Source: "forms_walkthrough", Product: demoProduct}, func(context.Context) ([]firms.Observation, error) {
		return readFixture(filepath.Join(fixtureDir, "demo.csv"), demoProduct, true, time.Now())
	}); err != nil || counts.Inserted == 0 {
		t.Fatalf("load synthetic detections: %+v %v", counts, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE role_assignments SET unit_path = 'ds.c1.d1.r2.b05'
WHERE workspace_id = 'd0000000-0000-4000-8000-000000000001' AND role_slug = 'field_staff'`); err != nil {
		t.Fatal(err)
	}
	provider, err := discoverProvider(ctx, cfg.issuer)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newLocalAttachments(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandler(pool, newAuthenticator(provider, cfg.issuer, cfg.audience), logger, cfg.corsOrigins, cfg.uploadDir, nil,
		withAttachments(store, 1<<20))

	// The national template and one report filled in on it, through the API.
	client := &http.Client{Timeout: 10 * time.Second}
	tokens := map[string]string{}
	for _, user := range []string{"state.admin", "beat.guard"} {
		tokens[user] = demoToken(t, client, provider.Endpoint().TokenURL, user)
	}
	call := func(method, path, user string, body any, want int) {
		t.Helper()
		data, _ := json.Marshal(body)
		r := httptest.NewRequestWithContext(ctx, method, "/api/v1/workspaces/demo"+path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+tokens[user])
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: HTTP %d: %s", method, path, w.Code, w.Body.Bytes())
		}
	}
	cases := readExtensionCases(t)
	var national map[string]any
	if err := json.Unmarshal(cases.Parent, &national); err != nil {
		t.Fatal(err)
	}
	national["id"] = "national-fire-report"
	items := national["sections"].([]any)[1].(map[string]any)["items"].([]any)
	crew := items[1].(map[string]any)["items"].([]any)[0].(map[string]any)
	crew["showWhen"] = map[string]any{"op": "equals", "field": "cause", "value": "lightning"}
	call("POST", "/forms", "state.admin", map[string]any{"level": "national", "definition": national,
		"template": templateRules{AllowExtension: true, Locked: []string{"basics", "crew_size"}}}, 201)
	call("PUT", "/forms/national-fire-report/assignments/first_report", "state.admin", map[string]any{"version": 1}, 200)
	var alertID string
	var lon, lat float64
	if err := pool.QueryRow(ctx, `SELECT a.id::text, ST_X(d.geom), ST_Y(d.geom) FROM alerts a
JOIN detections d ON (d.id, d.observed_at) = (a.detection_id, a.observed_at)
WHERE d.source = 'forms_walkthrough' AND a.queue = 'unit' AND a.unit_path = 'ds.c1.d1.r2.b05' ORDER BY a.id LIMIT 1`).Scan(&alertID, &lon, &lat); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"acknowledged", "team_sent", "reached_site", "controlled", "extinguished", "first_report"} {
		call("POST", "/alerts/"+alertID+"/steps", "beat.guard", map[string]any{"step": step,
			"location": map[string]float64{"longitude": lon, "latitude": lat}}, 200)
	}
	call("POST", "/alerts/"+alertID+"/submissions", "beat.guard", map[string]any{"id": walkthroughSubmission,
		"form": "national-fire-report", "version": 1, "step": "first_report",
		"values": map[string]any{"reached_at": time.Now().Add(-time.Hour).Format(time.RFC3339), "cause": "lightning", "crew_size": 4}}, 201)

	listener, err := net.Listen("tcp", cfg.httpAddr)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); _ = server.Close() }()
	logger.Info("Form designer walkthrough API listening", "address", listener.Addr().String(), "submission", walkthroughSubmission)
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}

// walkthroughSubmission is the report the walkthrough files before the state
// extends the template. scripts/form-designer-walkthrough.mjs reads it back.
const walkthroughSubmission = "7e3a1a7e-0000-4000-8000-00000000f0a1"
