package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/santhoshbalaji/open-fimra/services/api/internal/cdse"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

func TestCDSECredentials(t *testing.T) {
	const key = "FIMRA_CDSE_S3_ACCESS_KEY"
	t.Setenv(key, "")
	t.Setenv(key+"_FILE", filepath.Join(t.TempDir(), "fixture-key"))
	if err := os.WriteFile(os.Getenv(key+"_FILE"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := cdseCredential(key); value != "fixture" || err != nil {
		t.Fatalf("could not read fixture credential: %v", err)
	}
	t.Setenv(key, "fixture-inline")
	if _, err := cdseCredential(key); err == nil || strings.Contains(err.Error(), "fixture-inline") {
		t.Fatalf("expected redacted credential conflict, got %v", err)
	}
}

func TestCDSEIntegration(t *testing.T) {
	if os.Getenv("FIMRA_API_INTEGRATION") != "1" {
		t.Skip("Run make cdse-test-integration with a migrated database")
	}
	ctx, pool := notificationTestDB(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	_, _, idPrefix := loadSeedCopy(t, ctx, tx)
	workspace := idPrefix + "01"

	// Start with an old successful checkpoint so this poll must recover a
	// publication from beyond the connector's initial lookback.
	now := time.Now().UTC().Truncate(time.Second)
	observed := now.Add(-29 * time.Hour)
	var cursor = now.Add(-2 * 24 * time.Hour)
	if _, err := runIngest(ctx, tx, ingestRun{
		Source: cdse.Source, Product: cdse.Product("S3A"), WorkspaceID: &workspace,
		WindowStart: cursor.Add(-time.Hour), WindowEnd: cursor,
	}, func(context.Context) ([]firms.Observation, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}

	granule := cdse.Granule{
		ID: "catalogue-fixture",
		Name: fmt.Sprintf("S3A_SL_2_FRP____%s_%s_%s_0179_146_333______MAR_O_NR_003.SEN3",
			observed.Add(-time.Minute).Format("20060102T150405"),
			observed.Add(2*time.Minute).Format("20060102T150405"),
			observed.Add(100*time.Minute).Format("20060102T150405")),
		PublicationDate: observed.Add(105 * time.Minute),
		GeoFootprint:    json.RawMessage(`{"type":"Polygon","coordinates":[[[86.27,16.41],[86.31,16.41],[86.31,16.43],[86.27,16.43],[86.27,16.41]]]}`),
	}
	granule.S3Path = "/eodata/Sentinel-3/SLSTR/SL_2_FRP___/" + observed.Format("2006/01/02") + "/" + granule.Name
	granule.ContentDate.Start, granule.ContentDate.End = observed.Add(-time.Minute), observed.Add(2*time.Minute)
	nt := granule
	nt.Name, nt.S3Path = strings.Replace(nt.Name, "_NR_", "_NT_", 1), strings.Replace(nt.S3Path, "_NR_", "_NT_", 1)
	data, err := os.ReadFile("internal/cdse/testdata/fires.csv")
	if err != nil {
		t.Fatal(err)
	}
	csv := strings.NewReplacer("2000-01-02", observed.Format("2006-01-02"), "14:01:11", observed.Format("15:04:05")).Replace(string(data))
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Products":
			if strings.Contains(r.URL.Query().Get("$filter"), "S3A_SL_2_FRP") {
				json.NewEncoder(w).Encode(map[string]any{"value": []cdse.Granule{granule, nt}})
				return
			}
			fmt.Fprint(w, `{"value":[]}`)
		case granule.S3Path + "/" + cdse.File:
			if !strings.Contains(r.Header.Get("Authorization"), "Credential=fixture-access/") {
				t.Error("object request was not signed with the fixture key")
			}
			if attempts.Add(1) == 1 {
				http.Error(w, "fixture-secret", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, csv)
		default:
			t.Errorf("unexpected CDSE request %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := cdse.NewClient(server.Client(), server.URL+"/Products", server.URL, func(req *http.Request) {
		signS3(req, emptyPayloadHash, "fixture-access", "fixture-secret", "default", now)
	})
	firmsObservation := firms.Observation{
		Latitude: 16.418669, Longitude: 86.297480, ObservedAt: observed.Truncate(time.Minute),
		Satellite: "N21", Instrument: "VIIRS",
	}
	counts, err := runIngest(ctx, tx, ingestRun{Source: firms.Source, Product: "VIIRS_NOAA21_NRT"},
		func(context.Context) ([]firms.Observation, error) { return []firms.Observation{firmsObservation}, nil })
	if err != nil || counts.Inserted != 1 {
		t.Fatalf("seed first alert: %+v, %v", counts, err)
	}
	if err := pollCDSE(ctx, tx, client, now); err == nil || !strings.Contains(err.Error(), "HTTP 503") || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("expected redacted failed poll, got %v", err)
	}
	if err := pollCDSE(ctx, tx, client, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Replay the successful window with the fixed clock.
	if err := pollCDSE(ctx, tx, client, now); err != nil {
		t.Fatal(err)
	}
	var detections, members, incidents, before, after, links int
	err = tx.QueryRow(ctx, `
		WITH alerts_for_fire AS (
			SELECT a.id, m.incident_id, d.source FROM alerts a
			JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
			JOIN incident_members m ON m.alert_id=a.id
			WHERE a.workspace_id=$1 AND d.observed_at IN ($2, $3)
		)
		SELECT (SELECT count(*) FROM detections WHERE source='cdse' AND observed_at=$2),
		       count(*), count(DISTINCT a.incident_id),
		       (SELECT count(*) FROM notifications n JOIN alerts_for_fire a ON a.id=n.alert_id WHERE a.source='firms' AND event='alert_new'),
		       (SELECT count(*) FROM notifications n JOIN alerts_for_fire a ON a.id=n.alert_id WHERE a.source='cdse' AND event='alert_new'),
		       (SELECT count(*) FROM detection_links l JOIN detections d ON (d.id,d.observed_at)=(l.detection_id,l.observed_at)
		        WHERE d.source='cdse' AND d.observed_at=$2)
		FROM alerts_for_fire a`, workspace, observed, firmsObservation.ObservedAt).Scan(&detections, &members, &incidents, &before, &after, &links)
	if err != nil || detections != 1 || members != 2 || incidents != 1 || before == 0 || after != 0 || links != 0 {
		t.Fatalf("detections=%d members=%d incidents=%d first notifications=%d later=%d links=%d, %v",
			detections, members, incidents, before, after, links, err)
	}
	facts, err := loadFeedFacts(ctx, tx, &workspace, now.Add(2*time.Minute), defaultFIRMSProducts())
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 || facts[0].LastRunStatus != "succeeded" || facts[0].FailuresLast24h != 1 ||
		facts[0].FirstSeenRecords != 1 || facts[0].ProcessedRecords != 1 || *facts[0].WithinGate != 0 || facts[1].LatestObservedAt != nil || facts[1].LastRunStatus != "succeeded" {
		t.Fatalf("unexpected CDSE feed health: %+v", facts)
	}
}
