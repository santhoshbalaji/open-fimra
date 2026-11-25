package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

func TestBackfillIntegration(t *testing.T) {
	if os.Getenv("FIMRA_API_INTEGRATION") != "1" {
		t.Skip("Run make api-test-integration with the development stack running and migrated.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, loadConfig().databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	suffix, _, prefix := loadSeedCopy(t, ctx, tx)
	workspaceID := prefix + "01"
	// A source name of its own keeps this test apart from data already in a
	// shared database, such as an earlier make backfill-demo.
	source := "backfill_" + suffix

	// The near-real-time records come first, as they would in a live system.
	for _, nrt := range []struct {
		product, file string
		want          int
	}{{"VIIRS_SNPP_NRT", "viirs.csv", 2}, {"MODIS_NRT", "modis.csv", 1}} {
		counts, err := runIngest(ctx, tx, ingestRun{Source: source, Product: nrt.product}, func(context.Context) ([]firms.Observation, error) {
			return readFixture(filepath.Join(fixtureDir, nrt.file), nrt.product, false, time.Now())
		})
		if err != nil || counts.Inserted != nrt.want {
			t.Fatalf("load %s: %+v, %v", nrt.file, counts, err)
		}
	}

	// 46 days: 10 chunks for VIIRS. FIRMS has MODIS only up to February 10,
	// so MODIS gets 41 days in 9 chunks.
	req := backfillRequest{
		WorkspaceID: workspaceID, Source: source, Products: []string{"VIIRS_SNPP_SP", "MODIS_SP"},
		First: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), Last: time.Date(2000, 2, 15, 0, 0, 0, 0, time.UTC),
	}
	available := func(_ context.Context, product string) (time.Time, time.Time, error) {
		last := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
		if product == "MODIS_SP" {
			last = time.Date(2000, 2, 10, 0, 0, 0, 0, time.UTC)
		}
		return time.Date(1999, 12, 1, 0, 0, 0, 0, time.UTC), last, nil
	}
	fixtures := fixtureRange(fixtureDir)
	var calls int
	failOnce := map[string]bool{"VIIRS_SNPP_SP 2000-01-11": true}
	fetch := func(ctx context.Context, product string, box firms.Bounds, start time.Time, days int) ([]firms.Observation, error) {
		calls++
		if days > firms.MaxDayRange {
			t.Fatalf("asked for %d days", days)
		}
		if key := product + " " + start.Format(time.DateOnly); failOnce[key] {
			delete(failOnce, key)
			return nil, errors.New("synthetic network failure")
		}
		return fixtures(ctx, product, box, start, days)
	}

	type want struct {
		chunks, failed, skipped, unavailable, calls int
		counts                                      ingestCounts
	}
	for i, w := range []want{
		// The first run fails one chunk, which holds the January 14 record.
		{chunks: 19, failed: 1, unavailable: 5, calls: 19, counts: ingestCounts{Fetched: 8, Inserted: 8, Linked: 3, Matched: 5}},
		// The second run fetches only that chunk.
		{chunks: 1, skipped: 41 + 41, unavailable: 5, calls: 1, counts: ingestCounts{Fetched: 1, Inserted: 1, Matched: 1}},
		// The third run has nothing left to fetch.
		{chunks: 0, skipped: 46 + 41, unavailable: 5, calls: 0},
	} {
		calls = 0
		got, err := backfillFIRMS(ctx, tx, req, fetch, available, nil)
		if (err != nil) != (w.failed > 0) {
			t.Fatalf("run %d returned %v", i+1, err)
		}
		// Matches are counted across every workspace that covers the area,
		// which in a shared database can include the demo workspace.
		got.Counts.Matched, w.counts.Matched = 0, 0
		if got.Chunks != w.chunks || got.Failed != w.failed || got.SkippedDays != w.skipped ||
			got.UnavailableDays != w.unavailable || calls != w.calls || got.Counts != w.counts {
			t.Fatalf("run %d: got %+v with %d calls, want %+v", i+1, got, calls, w)
		}
		t.Logf("run %d: %d chunks, %d failed, %d days skipped, %d requests, inserted %d, linked %d",
			i+1, got.Chunks, got.Failed, got.SkippedDays, calls, got.Counts.Inserted, got.Counts.Linked)
	}

	// Replaying a chunk without the skip still adds nothing.
	counts, err := runIngest(ctx, tx, ingestRun{
		WorkspaceID: &workspaceID, Kind: runBackfill, Source: source, Product: "VIIRS_SNPP_SP",
		WindowStart: req.First, WindowEnd: req.Last.AddDate(0, 0, 1),
	}, func(ctx context.Context) ([]firms.Observation, error) {
		all, err := firms.Parse(mustOpen(t, filepath.Join(fixtureDir, "viirs_snpp_sp.csv")), "VIIRS_SNPP_SP")
		return all, err
	})
	if err != nil || counts.Fetched != 7 || counts.Inserted != 0 || counts.Duplicates != 7 || counts.Matched != 0 || counts.Linked != 0 {
		t.Fatalf("replay of the whole file: %+v, %v", counts, err)
	}

	// The River job runs the same backfill and finds nothing to do.
	worker := &firmsBackfillWorker{db: tx, fetch: fetch, available: available}
	calls = 0
	err = worker.Work(ctx, &river.Job[firmsBackfillArgs]{Args: firmsBackfillArgs{
		WorkspaceID: workspaceID, Source: source, Products: req.Products, First: "2000-01-01", Last: "2000-02-15",
	}})
	if err != nil || calls != 0 {
		t.Fatalf("the backfill job made %d requests: %v", calls, err)
	}
	if (firmsBackfillArgs{}).InsertOpts().Queue != backfillQueue {
		t.Fatal("the backfill job is not on its own queue")
	}

	var runs, failedRuns, partialDays, detections, links, linkedMatches, ownMatches, nrtMatches, linkedAlerts int
	err = tx.QueryRow(ctx, `
	    WITH sp AS (SELECT id, observed_at FROM detections WHERE source = $1 AND product LIKE '%\_SP')
	    SELECT
	        (SELECT count(*) FROM ingest_runs WHERE source = $1 AND kind = 'backfill' AND status = 'succeeded'
	           AND workspace_id = $2::uuid AND fetched_at IS NOT NULL),
	        (SELECT count(*) FROM ingest_runs WHERE source = $1 AND kind = 'backfill' AND status = 'failed'
	           AND error_message LIKE '%synthetic network failure%'),
	        (SELECT count(*) FROM ingest_runs WHERE source = $1 AND kind = 'backfill'
	           AND (window_start <> date_trunc('day', window_start) OR window_end <> date_trunc('day', window_end)
	                OR window_end - window_start > interval '5 days') AND window_end - window_start < interval '40 days'),
	        (SELECT count(*) FROM sp),
	        (SELECT count(*) FROM detection_links l JOIN sp ON (sp.id, sp.observed_at) = (l.detection_id, l.observed_at)
	           JOIN detections p ON (p.id, p.observed_at) = (l.primary_id, l.primary_observed_at)
	           WHERE p.source = $1 AND p.product LIKE '%\_NRT' AND l.distance_m < 5),
	        (SELECT count(*) FROM detection_matches m JOIN detection_links l
	           ON (l.detection_id, l.observed_at) = (m.detection_id, m.observed_at)),
	        (SELECT count(*) FROM detection_matches m JOIN sp ON (sp.id, sp.observed_at) = (m.detection_id, m.observed_at)
	           WHERE m.workspace_id = $2::uuid),
	        (SELECT count(*) FROM detection_matches m JOIN detections d ON (d.id, d.observed_at) = (m.detection_id, m.observed_at)
	           WHERE d.source = $1 AND d.product LIKE '%\_NRT' AND m.workspace_id = $2::uuid),
	        (SELECT count(*) FROM alerts a JOIN detection_links l
	           ON (l.detection_id, l.observed_at) = (a.detection_id, a.observed_at))
	`, source, workspaceID).Scan(&runs, &failedRuns, &partialDays, &detections, &links, &linkedMatches, &ownMatches, &nrtMatches, &linkedAlerts)
	if err != nil {
		t.Fatal(err)
	}
	// 20 chunk runs succeeded (19 planned once, the failed one again) and the
	// replay added one more.
	if runs != 20 || failedRuns != 1 || partialDays != 0 || detections != 9 || links != 3 ||
		linkedMatches != 0 || ownMatches != 6 || nrtMatches != 3 || linkedAlerts != 0 {
		t.Fatalf("got %d successful runs, %d failed, %d bad windows, %d records, %d links, "+
			"%d matches of linked records, %d matches of new records, %d near-real-time matches, %d alerts for linked records",
			runs, failedRuns, partialDays, detections, links, linkedMatches, ownMatches, nrtMatches, linkedAlerts)
	}
	t.Log("9 standard-processing records stored once; 3 linked to near-real-time records with no second match or alert")
}

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}
