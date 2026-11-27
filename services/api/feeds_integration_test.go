package main

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

func TestFeedHealthIntegration(t *testing.T) {
	if os.Getenv("FIMRA_API_INTEGRATION") != "1" {
		t.Skip("Run make api-test-integration with the development stack running and migrated.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
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
	suffix, slug, prefix := loadSeedCopy(t, ctx, tx)
	workspaceID := prefix + "01"

	t.Run("processing time", func(t *testing.T) {
		checkProcessingTime(t, ctx, tx, workspaceID)
	})
	t.Run("shared FIRMS fetch and recovery", func(t *testing.T) {
		_, pollSlug, pollPrefix := loadSeedCopy(t, ctx, tx)
		checkSharedFIRMSFetch(t, ctx, tx, pollPrefix+"01", pollSlug)
	})
	t.Run("stale and overlapping polls", func(t *testing.T) {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('open-fimra:firms-poll'))"); err != nil {
			t.Fatal(err)
		}
		poller := firmsPollWorker{
			pool: pool, keys: newFIRMSKeys(pool, nil, "", nil),
			previous: make(map[pollFeed]time.Time),
		}
		job := &river.Job[firmsPollArgs]{JobRow: &rivertype.JobRow{ScheduledAt: time.Now().Add(-time.Hour)}}
		if err := poller.Work(ctx, job); err != nil || poller.previous == nil {
			t.Fatalf("a stale tick should skip before attempting to take the database lock: %v", err)
		}
		job.ScheduledAt = time.Now()
		if err := poller.Work(ctx, job); err != nil || poller.previous != nil {
			t.Fatalf("another worker holds the database lock, so this tick should skip and discard its baseline: %v", err)
		}
	})
	t.Run("silent source", func(t *testing.T) {
		checkSilentSource(t, ctx, tx, slug, prefix, "silent_"+suffix)
	})
}

func checkProcessingTime(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Minute)
	run := ingestRun{WorkspaceID: &workspaceID, Source: firms.Source, Product: "MODIS_NRT",
		WindowStart: now.Add(-48 * time.Hour), WindowEnd: now}
	rows, err := readFixture(filepath.Join(fixtureDir, "modis.csv"), run.Product, false, now)
	if err != nil {
		t.Fatal(err)
	}
	terra, aquaEarly, aquaLate, backlog := rows[0], rows[0], rows[0], rows[0]
	terra.ObservedAt, aquaEarly.ObservedAt, aquaLate.ObservedAt = now.Add(-time.Hour), now.Add(-time.Hour), now.Add(-3*time.Hour)
	aquaEarly.Satellite, aquaLate.Satellite = "A", "Aqua"
	backlog.ObservedAt = now.Add(-24 * time.Hour)
	oldFetch := now.Add(-time.Second)
	ingest := func(rows []firms.Observation, timing fetchTiming) {
		t.Helper()
		counts, err := runIngestTimed(ctx, tx, run, func(context.Context) ([]firms.Observation, fetchTiming, error) {
			return rows, timing, nil
		})
		if err != nil || counts.Inserted != len(rows) {
			t.Fatalf("ingest: %+v, %v", counts, err)
		}
	}
	ingest([]firms.Observation{backlog}, fetchTiming{fetchedAt: oldFetch})
	ingest([]firms.Observation{terra, aquaEarly, aquaLate}, fetchTiming{fetchedAt: now, previousFetchedAt: &oldFetch})

	checked := time.Now()
	facts, err := loadFeedFacts(ctx, tx, &workspaceID, checked, defaultFIRMSProducts())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range facts {
		if f.Source != run.Source || f.Product != run.Product {
			continue
		}
		// Synthetic rows use independently controlled first-seen times.
		if f.ProcessedRecords != 3 || f.ProcessingP95Seconds == nil || *f.ProcessingP95Seconds < 1 || f.WithinGate == nil || *f.WithinGate != 1 {
			t.Fatalf("processing facts: %+v", f)
		}
	}
	latencies, err := loadFeedLatency(ctx, tx, workspaceID, checked)
	if err != nil {
		t.Fatal(err)
	}
	latency := latencies[feedKey{run.Source, run.Product}]
	if len(latency) != 4 {
		t.Fatalf("expected Aqua and Terra in both windows, got %+v", latency)
	}
	for _, sample := range latency {
		count, p50, p95, maxSeconds := 1, 3600.0, 3600.0, 3600.0
		if sample.Satellite == "Aqua" {
			count, p50, p95, maxSeconds = 2, 7200, 10440, 10800
		} else if sample.Satellite != "Terra" {
			t.Fatalf("unexpected satellite: %s", sample.Satellite)
		}
		if sample.FirstSeen.Count != count || sample.FirstSeen.P50Seconds == nil || *sample.FirstSeen.P50Seconds != p50 ||
			sample.FirstSeen.P95Seconds == nil || math.Abs(*sample.FirstSeen.P95Seconds-p95) > 0.001 ||
			sample.FirstSeen.MaxSeconds == nil || *sample.FirstSeen.MaxSeconds != maxSeconds ||
			sample.Matched.Count != count || sample.Matched.P50Seconds == nil || *sample.Matched.P50Seconds < p50 {
			t.Fatalf("unexpected latency sample: %+v", sample)
		}
	}
}

func checkSharedFIRMSFetch(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, slug string) {
	t.Helper()
	var twin string
	err := tx.QueryRow(ctx, `
		INSERT INTO workspaces (slug, name, boundary)
		SELECT $2, 'Synthetic second workspace', boundary FROM workspaces WHERE id = $1
		RETURNING id::text`, workspaceID, slug+"-twin").Scan(&twin)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := newSettingsCipher(randomSettingsKey(t), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, workspace := range []string{workspaceID, twin} {
		if err := saveSecret(ctx, tx, cipher, workspace, firmsSecret, testMapKey, "test"); err != nil {
			t.Fatal(err)
		}
	}
	fake := newFakeFIRMS(t, testMapKey)
	offline := false
	transport := roundTripper(func(r *http.Request) (*http.Response, error) {
		if offline {
			return nil, errors.New("synthetic outage")
		}
		return fake.transport().RoundTrip(r)
	})
	poller := firmsPollWorker{keys: newFIRMSKeys(tx, cipher, "", transport)}
	for i := range 4 {
		offline = i == 2
		if err := poller.poll(ctx, tx, time.Now()); (err != nil) != offline {
			t.Fatalf("poll %d, offline=%t: %v", i+1, offline, err)
		}
	}
	if got := len(fake.seen()); got != 9 {
		t.Fatalf("three successful polls of three products used %d requests, want 9", got)
	}

	rows, err := tx.Query(ctx, `
		SELECT product, count(DISTINCT fetched_at), array_agg(previous_fetched_at IS NOT NULL ORDER BY id)
		FROM ingest_runs WHERE workspace_id IN ($1, $2) AND source = 'firms'
		GROUP BY product ORDER BY product`, workspaceID, twin)
	if err != nil {
		t.Fatal(err)
	}
	type counts struct {
		product         string
		distinctFetches int
		comparable      []bool
	}
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (counts, error) {
		var c counts
		err := row.Scan(&c.product, &c.distinctFetches, &c.comparable)
		return c, err
	})
	products := []string{"MODIS_NRT", "VIIRS_NOAA20_NRT", "VIIRS_NOAA21_NRT"}
	if err != nil || len(got) != len(products) {
		t.Fatalf("polls = %+v: %v", got, err)
	}
	wantComparable := []bool{false, false, true, true, false, false, false, false}
	for i, c := range got {
		if c.product != products[i] || c.distinctFetches != 3 || !slices.Equal(c.comparable, wantComparable) {
			t.Fatalf("unexpected product runs: %+v", c)
		}
	}
}

// checkSilentSource builds a feed's history on a fake clock: normal polls,
// then polls that succeed with no new observations, then failed polls, then
// recovery. It checks that exactly one alert opens and then resolves.
func checkSilentSource(t *testing.T, ctx context.Context, tx pgx.Tx, slug, prefix, source string) {
	workspaceID := prefix + "01"
	const product = "VIIRS_SNPP_NRT"
	// 10 March 2031 is in the default fire season.
	base := time.Date(2031, 3, 10, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return base.Add(d) }
	polls := func(from, to time.Duration, status string, observed *time.Time) {
		t.Helper()
		_, err := tx.Exec(ctx, `
		    INSERT INTO ingest_runs (workspace_id, kind, source, product, window_start, window_end, started_at,
		                             finished_at, status, fetched_at, latest_observed_at, error_message)
		    SELECT $1, 'poll', $2, $3, s - interval '1 day', s, s, s + interval '20 seconds', $6,
		           CASE WHEN $6 = 'succeeded' THEN s + interval '10 seconds' END,
		           CASE WHEN $6 = 'succeeded' THEN $7::timestamptz END,
		           CASE WHEN $6 = 'failed' THEN 'synthetic outage' END
		    FROM generate_series($4::timestamptz, $5::timestamptz, interval '15 minutes') AS s`,
			workspaceID, source, product, at(from), at(to), status, observed)
		if err != nil {
			t.Fatal(err)
		}
	}
	type alertRow struct {
		open, total    int
		status, opened string
	}
	check := func(d time.Duration, wantStatus string, want alertRow) {
		t.Helper()
		if _, err := checkFeedHealth(ctx, tx, slog.Default(), at(d), defaultFIRMSProducts()); err != nil {
			t.Fatal(err)
		}
		facts, err := loadFeedFacts(ctx, tx, &workspaceID, at(d), defaultFIRMSProducts())
		if err != nil {
			t.Fatal(err)
		}
		var status string
		for _, f := range facts {
			if f.Source == source {
				status = feedStatus(f, defaultThresholds, at(d))
			}
		}
		var got alertRow
		err = tx.QueryRow(ctx, `
		    SELECT count(*) FILTER (WHERE resolved_at IS NULL), count(*),
		           COALESCE(max(status) FILTER (WHERE resolved_at IS NULL), ''),
		           COALESCE(max(opened_status), '')
		    FROM feed_health_alerts WHERE workspace_id = $1 AND source = $2`, workspaceID, source).
			Scan(&got.open, &got.total, &got.status, &got.opened)
		if err != nil {
			t.Fatal(err)
		}
		if status != wantStatus || got != want {
			t.Fatalf("at +%s the feed is %s with alerts %+v, want %s with %+v", d, status, got, wantStatus, want)
		}
		var recipients []string
		err = tx.QueryRow(ctx, `
SELECT ARRAY(SELECT u.username FROM notifications n JOIN users u ON u.id=n.recipient_id
    JOIN feed_health_alerts a ON a.id=n.feed_alert_id
    WHERE a.workspace_id=$1 AND a.source=$2 AND n.event='feed_open' AND n.channel='in_app'
    ORDER BY u.username)`, workspaceID, source).Scan(&recipients)
		if err != nil || len(recipients) != want.total || (want.total == 1 && recipients[0] != "state.admin") {
			t.Fatalf("feed-health notification recipients = %v: %v", recipients, err)
		}
		t.Logf("+%s: %s, %d open of %d alerts", d, status, got.open, got.total)
	}

	seen := at(-3 * time.Hour)
	polls(-2*time.Hour, 0, "succeeded", &seen)
	check(0, feedHealthy, alertRow{})

	// The source goes silent. Polls still succeed but bring nothing new.
	polls(15*time.Minute, 36*time.Hour, "succeeded", nil)
	check(20*time.Hour, feedDelayed, alertRow{})
	check(32*time.Hour, feedDelayed, alertRow{})
	check(34*time.Hour, feedQuiet, alertRow{1, 1, feedQuiet, feedQuiet})
	// The same problem continues: no second alert.
	check(34*time.Hour+15*time.Minute, feedQuiet, alertRow{1, 1, feedQuiet, feedQuiet})
	check(36*time.Hour, feedQuiet, alertRow{1, 1, feedQuiet, feedQuiet})

	// Then fetching fails too. It is the same alert, now failing.
	polls(36*time.Hour+15*time.Minute, 37*time.Hour+15*time.Minute, "failed", nil)
	check(37*time.Hour+30*time.Minute, feedFailing, alertRow{1, 1, feedFailing, feedQuiet})
	checkFeedsEndpoint(t, tx, slug, prefix, source, at(37*time.Hour+30*time.Minute))

	// The feed recovers and the alert resolves.
	fresh := at(37 * time.Hour)
	polls(38*time.Hour, 38*time.Hour, "succeeded", &fresh)
	check(38*time.Hour, feedHealthy, alertRow{0, 1, "", feedQuiet})
	check(38*time.Hour+15*time.Minute, feedHealthy, alertRow{0, 1, "", feedQuiet})
	var resolved time.Time
	var finalStatus string
	if err := tx.QueryRow(ctx, `SELECT resolved_at, status FROM feed_health_alerts WHERE workspace_id = $1 AND source = $2`,
		workspaceID, source).Scan(&resolved, &finalStatus); err != nil || !resolved.Equal(at(38*time.Hour)) || finalStatus != feedHealthy {
		t.Fatalf("the alert resolved at %s as %s: %v", resolved, finalStatus, err)
	}

	// Outside the fire season a quiet feed raises no alert.
	if _, err := tx.Exec(ctx, `UPDATE workspaces SET settings = settings || '{"feedHealth": {"fireSeason": {"start": "11-01", "end": "02-28"}}}' WHERE id = $1`,
		workspaceID); err != nil {
		t.Fatal(err)
	}
	polls(38*time.Hour+15*time.Minute, 80*time.Hour, "succeeded", nil)
	check(80*time.Hour, feedQuiet, alertRow{0, 1, "", feedQuiet})
}

func checkFeedsEndpoint(t *testing.T, tx pgx.Tx, slug, prefix, source string, now time.Time) {
	t.Helper()
	key, signer := testSigner(t)
	const issuer, audience = "https://issuer.invalid", "open-fimra-api"
	auth := authenticator{issuers: map[string]bool{issuer: true}, verifier: oidc.NewVerifier(issuer,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: audience})}
	handler := api{reader: tx, feedDB: tx, logger: slog.Default(), now: func() time.Time { return now }}.handler(auth, devOrigins)
	request := func(path, subject string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if subject != "" {
			// Tokens are checked against the real clock.
			token := signedToken(t, signer, jwt.Claims{Issuer: issuer, Subject: prefix + subject,
				Audience: jwt.Audience{audience}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Minute))})
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/workspaces/" + slug + "/feeds"
	assertUnauthorized(t, request(path, ""), "Bearer")
	for _, tc := range []struct{ subject, path string }{
		{"a2", path}, {"a3", path}, {"a4", path}, {"a1", "/api/v1/workspaces/no-such-workspace/feeds"},
	} {
		if w := request(tc.path, tc.subject); w.Code != http.StatusForbidden {
			t.Fatalf("%s got HTTP %d for %s, want 403", tc.subject, w.Code, tc.path)
		}
	}

	w := request(path, "a1")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("state admin got HTTP %d: %s", w.Code, w.Body)
	}
	var body feedsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.FireSeason.InSeason || body.FireSeason.Start != "11-01" || body.Thresholds.QuietAfterHours != 36 {
		t.Fatalf("unexpected settings in %s", w.Body)
	}
	var feed *feedJSON
	for i := range body.Feeds {
		if body.Feeds[i].Source == source {
			feed = &body.Feeds[i]
		}
	}
	lastSuccess := time.Date(2031, 3, 11, 12, 0, 10, 0, time.UTC)
	if feed == nil || feed.Status != feedFailing || feed.LastRunStatus != "failed" || feed.FailuresLast24h != 5 ||
		feed.LastSuccessAt == nil || !feed.LastSuccessAt.Equal(lastSuccess) || feed.DelaySeconds == nil ||
		*feed.DelaySeconds != int64((40*time.Hour+30*time.Minute).Seconds()) || feed.LastError == nil {
		t.Fatalf("unexpected feed in %s", w.Body)
	}
	var alert *feedAlertJSON
	for i := range body.OpenAlerts {
		if body.OpenAlerts[i].Source == source {
			alert = &body.OpenAlerts[i]
		}
	}
	if alert == nil || alert.Status != feedFailing || alert.OpenedStatus != feedQuiet || alert.Product != "VIIRS_SNPP_NRT" {
		t.Fatalf("unexpected open alerts in %s", w.Body)
	}
	t.Logf("state admin sees the failing feed and its open alert; other demo users get 403")
}
