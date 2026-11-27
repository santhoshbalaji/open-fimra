package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/lsasaf"
)

func TestLSASAFIntegration(t *testing.T) {
	if os.Getenv("FIMRA_API_INTEGRATION") != "1" {
		t.Skip("Run make lsasaf-test-integration with a migrated database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	pool, err := openPool(ctx, loadConfig().databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	_, slug, prefix := loadSeedCopy(t, ctx, tx)
	workspace := prefix + "01"

	const product = "HDF5_LSASAF_MSG-IODC_FRP-PIXEL-ListProduct_IODC-Disk_"
	const day = "20261126"
	at, _ := time.Parse("200601021504", day+"0000")
	var mu sync.Mutex
	files := map[string]bool{"0000": true}
	downloads := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/2026/11/26/" {
			for minute := range files {
				fmt.Fprintf(w, "<a href=\"%s%s%s\">file</a>", product, day, minute)
			}
			return
		}
		if minute, found := strings.CutPrefix(r.URL.Path, "/2026/11/26/"+product+day); found && files[minute] {
			downloads[minute]++
			http.ServeFile(w, r, "internal/lsasaf/testdata/"+day+minute+".h5")
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	client := lsasaf.NewClient(server.URL, "test", "test", server.Client())
	poll := func(minutes int, more ...string) {
		t.Helper()
		mu.Lock()
		for _, min := range more {
			files[min] = true
		}
		mu.Unlock()
		if err := pollLSASAF(ctx, tx, client, lsasafRule{2, 3}, 5*time.Minute, at.Add(time.Duration(minutes)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	counts := func(scans, pixels, cases, alerts int) {
		t.Helper()
		var s, p, c, a int
		err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM lsasaf_scans WHERE workspace_id=$1),
			(SELECT count(*) FROM lsasaf_pixels WHERE workspace_id=$1),
			(SELECT count(*) FROM lsasaf_cases WHERE workspace_id=$1),
			(SELECT count(*) FROM alerts WHERE workspace_id=$1)`, workspace).Scan(&s, &p, &c, &a)
		if err != nil || s != scans || p != pixels || c != cases || a != alerts {
			t.Fatalf("scans/pixels/cases/alerts = %d/%d/%d/%d, want %d/%d/%d/%d: %v", s, p, c, a, scans, pixels, cases, alerts, err)
		}
	}
	poll(5)
	counts(1, 1, 1, 0)
	for _, role := range []struct {
		suffix     string
		watchCount int
	}{{"a1", 1}, {"a3", 0}, {"ff", 0}} {
		var hasAccess bool
		var result struct {
			Features []struct{ Geometry struct{ Type string } }
		}
		err := tx.QueryRow(ctx, lsasafWatchSQL, prefix+role.suffix, slug, 24, at.Add(time.Hour)).
			Scan(&hasAccess, &result)
		if err != nil || hasAccess != (role.suffix != "ff") || len(result.Features) != role.watchCount ||
			(role.watchCount > 0 && result.Features[0].Geometry.Type != "Polygon") {
			t.Fatalf("watch scope %s: %v, %+v, %v", role.suffix, hasAccess, result, err)
		}
	}
	var beats int
	err = tx.QueryRow(ctx, `SELECT jsonb_array_length(boundary_snapshot->'candidateBeats')
		FROM detection_matches WHERE workspace_id=$1`, workspace).Scan(&beats)
	if err != nil || beats < 2 {
		t.Fatalf("expected multiple candidate beats, got %d: %v", beats, err)
	}
	poll(35, "0030")
	counts(2, 2, 1, 0)
	poll(40, "0015") // A late empty scan makes the two hits consecutive evidence.
	counts(3, 2, 1, 1)
	poll(50, "0045")
	counts(4, 3, 1, 1)
	poll(55)
	counts(4, 3, 1, 1)
	mu.Lock()
	for minute, n := range downloads {
		if n != 1 {
			t.Errorf("downloaded %s %d times", minute, n)
		}
	}
	mu.Unlock()

	var alertID int64
	var queue string
	err = tx.QueryRow(ctx, "SELECT id,queue FROM alerts WHERE workspace_id=$1", workspace).Scan(&alertID, &queue)
	if err != nil || queue != "triage" {
		t.Fatalf("expected one duty alert in triage, got %v: %v", queue, err)
	}
	for _, include := range []bool{false, true} {
		var allowed, review, known bool
		var counts map[string]int
		var rows []json.RawMessage
		err := tx.QueryRow(ctx, alertsSQL, prefix+"a1", slug, alertID, 168, "", "", "", nil, nil,
			int64(0), 2, nil, nil, "", "", include).Scan(&allowed, &review, &known, &counts, &rows)
		if err != nil || !allowed || !review || !known || (len(rows) == 1) != include {
			t.Fatalf("point-only alert access with include=%t: %+v, %v", include, rows, err)
		}
	}
	var push, inbox int
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE channel='push'),
		count(*) FILTER (WHERE channel='in_app') FROM notifications WHERE alert_id=$1`, alertID).Scan(&push, &inbox)
	if err != nil || push != 0 || inbox == 0 {
		t.Fatalf("coarse pushes and duty inbox: %d, %d, %v", push, inbox, err)
	}

	// The imported point is inside the footprint and several kilometres from
	// some corners. It must take over the existing alert and preserve its ID.
	observe := firms.Observation{NativeID: prefix, Satellite: "N21", Instrument: "VIIRS",
		Latitude: 16.418669, Longitude: 86.297480, ObservedAt: at.Add(27 * time.Minute)}
	ingestPoint := func(workspace string) {
		t.Helper()
		_, err := runIngest(ctx, tx, ingestRun{Source: firms.Source, Product: "VIIRS_NOAA21_NRT",
			WorkspaceID: &workspace, WorkspaceOnly: true, Kind: runImport}, func(context.Context) ([]firms.Observation, error) {
			return []firms.Observation{observe}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	ingestPoint(workspace)
	counts(4, 3, 1, 1)
	var confirmed bool
	var source string
	err = tx.QueryRow(ctx, `SELECT c.confirmed_at IS NOT NULL,d.source FROM lsasaf_cases c
		JOIN alerts a ON a.id=c.alert_id JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
		WHERE a.id=$1`, alertID).Scan(&confirmed, &source)
	if err != nil || !confirmed || source != "firms" {
		t.Fatalf("expected precise evidence on original alert: %v, %s, %v", confirmed, source, err)
	}
	// A precise alert can also arrive before the geo listing. A replay must
	// retain that point and avoid ever promoting a second alert.
	_, _, other := loadSeedCopy(t, ctx, tx)
	workspace = other + "01"
	ingestPoint(workspace)
	poll(65, "0100")
	counts(5, 3, 1, 1)
	err = tx.QueryRow(ctx, `SELECT c.confirmed_at IS NOT NULL, d.source FROM lsasaf_cases c
		JOIN alerts a ON a.id=c.alert_id JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
		WHERE c.workspace_id=$1`, workspace).Scan(&confirmed, &source)
	if err != nil || !confirmed || source != "firms" {
		t.Fatalf("expected link to the earlier precise alert: %v, %s, %v", confirmed, source, err)
	}

	checkAt := time.Now().Add(time.Minute)
	if checkAt.Before(at.Add(time.Hour)) {
		checkAt = at.Add(time.Hour)
	}
	facts, err := loadFeedFacts(ctx, tx, &workspace, checkAt, nil)
	if err != nil || len(facts) != 1 || facts[0].Source != lsasaf.Source ||
		facts[0].LatestScanAt == nil || !facts[0].LatestScanAt.Equal(at.Add(time.Hour)) || facts[0].LatestObservedAt == nil ||
		!facts[0].LatestObservedAt.Equal(at.Add(45*time.Minute)) {
		t.Fatalf("expected scan health independent of the newest local fire: %+v, %v", facts, err)
	}

}
