package main

import (
	"context"
	"crypto"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

func TestFeedHealthIntegration(t *testing.T) {
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
	suffix, slug, prefix := loadSeedCopy(t, ctx, tx)
	workspaceID := prefix + "01"

	t.Run("processing time", func(t *testing.T) {
		checkProcessingTime(t, ctx, tx, workspaceID, "live_"+suffix)
	})
	t.Run("silent source", func(t *testing.T) {
		checkSilentSource(t, ctx, tx, slug, prefix, "silent_"+suffix)
	})
}

// checkProcessingTime polls twice for real, and checks that the new records
// of the second poll get a processing time.
func checkProcessingTime(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, source string) {
	now := time.Now()
	run := ingestRun{WorkspaceID: &workspaceID, Source: source, Product: demoProduct, WindowStart: now.Add(-48 * time.Hour), WindowEnd: now}
	for i, file := range []string{"", "demo.csv"} {
		counts, err := runIngest(ctx, tx, run, func(context.Context) ([]firms.Observation, error) {
			if file == "" {
				return nil, nil
			}
			return readFixture(filepath.Join(fixtureDir, file), demoProduct, true, now)
		})
		if err != nil || (i == 1 && counts.Inserted != 11) {
			t.Fatalf("poll %d: %+v, %v", i+1, counts, err)
		}
	}
	facts, err := loadFeedFacts(ctx, tx, &workspaceID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range facts {
		if f.Source != source {
			continue
		}
		if f.ProcessedRecords != 11 || f.ProcessingP95Seconds == nil || *f.ProcessingP95Seconds < 0 ||
			*f.ProcessingP95Seconds > 60 || f.WithinGate == nil || *f.WithinGate != 1 || f.LatestObservedAt == nil {
			t.Fatalf("processing facts: %+v", f)
		}
		t.Logf("11 new records, 95th percentile %.2f seconds from the previous fetch to the match", *f.ProcessingP95Seconds)
		return
	}
	t.Fatal("the live feed is missing from feed health")
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
		if _, err := checkFeedHealth(ctx, tx, slog.Default(), at(d)); err != nil {
			t.Fatal(err)
		}
		facts, err := loadFeedFacts(ctx, tx, &workspaceID, at(d))
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
	auth := authenticator{issuer: issuer, verifier: oidc.NewVerifier(issuer,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: audience})}
	handler := api{feedDB: tx, logger: slog.Default(), now: func() time.Time { return now }}.handler(auth, devOrigins)
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
