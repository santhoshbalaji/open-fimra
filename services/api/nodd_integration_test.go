package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/nodd"
)

func TestNODDIntegration(t *testing.T) {
	if os.Getenv("FIMRA_API_INTEGRATION") != "1" {
		t.Skip("Run make noaa-nodd-test-integration with a migrated database")
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
	_, _, idPrefix := loadSeedCopy(t, ctx, tx)
	workspace := idPrefix + "01"

	data, err := os.ReadFile("../../packages/connectors/noaa-nodd/fixtures/fires.nc")
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC().Truncate(time.Minute).Add(-30*time.Minute + 10*time.Second)
	now := observed.Add(20 * time.Minute)
	stamp := func(t time.Time) string { return t.Format("20060102150405") + "0" }
	const prefix = "VIIRS_EFIRE_VIIRSI_EDR/"
	sats := nodd.Satellites
	paths := make(map[string]string)
	for _, sat := range sats {
		paths[sat.Code] = fmt.Sprintf("%s%s/EFIRE-VIIRSI_v1r3_%s_s%s_e%s_c%s.nc", prefix,
			observed.Format("2006/01/02"), sat.Code, stamp(observed), stamp(observed.Add(85*time.Second)), stamp(now))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sat, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		key := paths[sat]
		if path == key && key != "" {
			w.Header().Set("ETag", `"fixture"`)
			http.ServeContent(w, r, path, now, bytes.NewReader(data))
			return
		}
		fmt.Fprint(w, "<ListBucketResult>")
		if query := r.URL.Query().Get("prefix"); query != "" && strings.HasPrefix(key, query) {
			fmt.Fprintf(w, "<Contents><Key>%s</Key><LastModified>%s</LastModified><Size>%d</Size><ETag>\"fixture\"</ETag></Contents>",
				key, now.Format(time.RFC3339), len(data))
		}
		fmt.Fprint(w, "</ListBucketResult>")
	}))
	defer server.Close()
	for i := range sats {
		sats[i].Endpoint = server.URL + "/" + sats[i].Code
	}
	client := nodd.NewClient(server.Client())
	const csv = "latitude,longitude,bright_ti4,scan,track,acq_date,acq_time,satellite,confidence,frp,daynight\n"
	firmsResult := func(satellite, kind string, at time.Time, lat, lon float64, wantLinked int) {
		t.Helper()
		product := "VIIRS_NOAA" + satellite + "_" + kind
		rows := fmt.Sprintf("%s%f,%f,300,0.4,0.4,%s,%s,N%s,n,3,D\n",
			csv, lat, lon, at.Format("2006-01-02"), at.Format("1504"), satellite)
		counts, err := runIngest(ctx, tx, ingestRun{Source: firms.Source, Product: product}, func(context.Context) ([]firms.Observation, error) {
			return firms.Parse(strings.NewReader(rows), product)
		})
		if err != nil || counts.Inserted != 1 || counts.Linked != wantLinked {
			t.Fatalf("%s ingestion: %+v, %v", product, counts, err)
		}
	}
	firmsTime := observed.Truncate(time.Minute).Add(time.Minute)
	// NOAA-21 reaches FIRMS first. NOAA-20 arrives in NOAA first.
	firmsResult("21", "NRT", firmsTime, 16.418669, 86.297480, 0)
	if err := pollNODD(ctx, tx, client, sats[:], now); err != nil {
		t.Fatal(err)
	}
	firmsResult("20", "NRT", firmsTime, 16.418669, 86.297480, 1)
	firmsResult("20", "SP", firmsTime, 16.418669, 86.297480, 1)
	// A later observation of the same spot must keep an independent alert.
	firmsResult("20", "NRT", firmsTime.Add(5*time.Minute), 16.418669, 86.297480, 0)
	if err := pollNODD(ctx, tx, client, sats[:], now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}

	var detections, links, alerts, sameRoot int
	err = tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM detections WHERE source = 'noaa_nodd' AND observed_at = $2),
		       (SELECT count(*) FROM detection_links WHERE observed_at = $2 OR primary_observed_at = $2),
		       (SELECT count(*) FROM alerts a JOIN detections d ON (a.detection_id,a.observed_at) = (d.id,d.observed_at)
		        WHERE a.workspace_id = $1 AND d.source IN ('firms', 'noaa_nodd')
		          AND ST_DWithin(d.geom::geography, ST_SetSRID(ST_Point(86.297475,16.418669),4326)::geography, 200)
		          AND a.observed_at BETWEEN $2 AND $2 + interval '10 minutes'),
		       (SELECT count(*) FROM detection_links l JOIN detections d ON (d.id,d.observed_at) = (l.primary_id,l.primary_observed_at)
		        WHERE l.primary_observed_at = $2 AND d.source = 'noaa_nodd' AND d.product = 'VIIRS_NOAA20_EFIRE')
		`, workspace, observed).Scan(&detections, &links, &alerts, &sameRoot)
	if err != nil || detections != 4 || links != 3 || alerts != 3 || sameRoot != 2 {
		t.Fatalf("got %d NOAA detections, %d links, %d alerts near the first pixel, %d links to NOAA-20: %v",
			detections, links, alerts, sameRoot, err)
	}
	// The default demo workspace sorts first and owns the inserting polls.
	insertingWorkspace := seedPrefix + "01"
	facts, err := loadFeedFacts(ctx, tx, &insertingWorkspace, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var products int
	for _, f := range facts {
		if f.Source == nodd.Source {
			products++
			if f.LastRunStatus != "succeeded" || f.LatestObservedAt == nil || !f.LatestObservedAt.Equal(observed) ||
				f.FirstSeenRecords != 2 || f.FirstSeenP50Seconds == nil || *f.FirstSeenP50Seconds < 1200 || f.FirstSeenP95Seconds == nil {
				t.Fatalf("unexpected NOAA feed facts: %+v", f)
			}
		}
	}
	if products != len(sats) {
		t.Fatalf("got %d NOAA product feeds, want %d", products, len(sats))
	}
}
