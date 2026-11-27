package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

//go:embed feeds.sql
var feedsSQL string

// Feed statuses, from best to worst.
const (
	feedHealthy = "healthy"
	feedDelayed = "delayed"
	feedQuiet   = "quiet"
	feedFailing = "failing"
)

// feedThresholds decide a feed's status. They are workspace settings under
// "feedHealth", with these defaults:
//
//   - failing: no successful fetch for 60 minutes.
//   - quiet: fetches work but the newest observation is over 36 hours old. In
//     fire season every polar-orbiting satellite passes over India twice a
//     day, so this is two missed daytime passes.
//   - delayed: the newest observation is over 18 hours old, one missed
//     daytime pass plus the usual few hours before FIRMS publishes it.
type feedThresholds struct {
	FailingAfter time.Duration
	DelayedAfter time.Duration
	QuietAfter   time.Duration
}

var defaultThresholds = feedThresholds{
	FailingAfter: 60 * time.Minute,
	DelayedAfter: 18 * time.Hour,
	QuietAfter:   36 * time.Hour,
}

// monthDay is a calendar day of the year, such as 11-01.
type monthDay struct{ Month, Day int }

func (d monthDay) String() string { return fmt.Sprintf("%02d-%02d", d.Month, d.Day) }

func parseMonthDay(s string) (monthDay, error) {
	// 2000 is a leap year, so 02-29 is accepted.
	t, err := time.Parse("2006-01-02", "2000-"+s)
	if err != nil || len(s) != 5 {
		return monthDay{}, fmt.Errorf("%q is not a day in MM-DD form", s)
	}
	return monthDay{int(t.Month()), t.Day()}, nil
}

// fireSeason is the part of the year when a quiet or failing feed raises an
// alert, both ends included, in India Standard Time. The default is
// 1 November to 30 June: the national forest fire season, which covers the
// early fires of the dry deciduous forests in central and southern India and
// the late fires of the Himalayan pine forests in May and June. Forest fires
// are rare in the monsoon months from July to October, when quiet feeds are
// expected.
type fireSeason struct{ Start, End monthDay }

var defaultFireSeason = fireSeason{Start: monthDay{11, 1}, End: monthDay{6, 30}}

// indiaTime is India Standard Time, which has no daylight saving.
var indiaTime = time.FixedZone("IST", 5*60*60+30*60)

func (s fireSeason) contains(t time.Time) bool {
	local := t.In(indiaTime)
	day := int(local.Month())*100 + local.Day()
	start, end := s.Start.Month*100+s.Start.Day, s.End.Month*100+s.End.Day
	if start <= end {
		return day >= start && day <= end
	}
	// The season runs across the new year.
	return day >= start || day <= end
}

type feedSettings struct {
	Thresholds feedThresholds
	Season     fireSeason
}

// readFeedSettings reads the "feedHealth" part of a workspace's settings. It
// returns the defaults with an error when a value is invalid, so that a typing
// mistake cannot switch the alerts off.
func readFeedSettings(settings []byte) (feedSettings, error) {
	out := feedSettings{Thresholds: defaultThresholds, Season: defaultFireSeason}
	var raw struct {
		FeedHealth *struct {
			FailingAfterMinutes *float64 `json:"failingAfterMinutes"`
			DelayedAfterHours   *float64 `json:"delayedAfterHours"`
			QuietAfterHours     *float64 `json:"quietAfterHours"`
			FireSeason          *struct {
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"fireSeason"`
		} `json:"feedHealth"`
	}
	if len(settings) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(settings, &raw); err != nil {
		return out, fmt.Errorf("read feed health settings: %w", err)
	}
	if raw.FeedHealth == nil {
		return out, nil
	}
	set, want := out, raw.FeedHealth
	for _, v := range []struct {
		value *float64
		unit  time.Duration
		dst   *time.Duration
	}{
		{want.FailingAfterMinutes, time.Minute, &set.Thresholds.FailingAfter},
		{want.DelayedAfterHours, time.Hour, &set.Thresholds.DelayedAfter},
		{want.QuietAfterHours, time.Hour, &set.Thresholds.QuietAfter},
	} {
		if v.value == nil {
			continue
		}
		if !(*v.value > 0 && *v.value < 24*365) {
			return out, errors.New("feed health thresholds must be positive and under a year")
		}
		*v.dst = time.Duration(*v.value * float64(v.unit))
	}
	if set.Thresholds.DelayedAfter >= set.Thresholds.QuietAfter {
		return out, errors.New("delayedAfterHours must be less than quietAfterHours")
	}
	if want.FireSeason != nil {
		start, errStart := parseMonthDay(want.FireSeason.Start)
		end, errEnd := parseMonthDay(want.FireSeason.End)
		if err := errors.Join(errStart, errEnd); err != nil {
			return out, fmt.Errorf("read the fire season: %w", err)
		}
		set.Season = fireSeason{Start: start, End: end}
	}
	return set, nil
}

// feedFacts are what the database knows about one live feed.
type feedFacts struct {
	WorkspaceID, WorkspaceSlug, Source, Product string
	Settings                                    []byte
	FirstRunAt, LastRunAt                       time.Time
	LastRunStatus                               string
	LastError                                   *string
	LastSuccessAt, LatestObservedAt             *time.Time
	FailuresLast24h                             int
	ProcessedRecords                            int
	ProcessingP95Seconds, WithinGate            *float64
	FirstSeenRecords                            int
	FirstSeenP50Seconds, FirstSeenP95Seconds    *float64
	LatestScanAt                                *time.Time
	ListedScans, MissingScans                   int
	ScanP50Seconds, ScanP95Seconds              *float64
}

// feedStatus applies the thresholds at now. Until a feed has had a success or
// an observation, its age counts from its first run, so a new feed starts
// healthy.
func feedStatus(f feedFacts, t feedThresholds, now time.Time) string {
	since := f.FirstRunAt
	if f.LastSuccessAt != nil {
		since = *f.LastSuccessAt
	}
	if now.Sub(since) > t.FailingAfter {
		return feedFailing
	}
	if f.Source == "lsasaf" {
		latest := f.FirstRunAt
		if f.LatestScanAt != nil {
			latest = *f.LatestScanAt
		}
		switch age := now.Sub(latest); {
		case age > 90*time.Minute:
			return feedQuiet
		case age > 45*time.Minute:
			return feedDelayed
		default:
			return feedHealthy
		}
	}
	observed := f.FirstRunAt
	if f.LatestObservedAt != nil {
		observed = *f.LatestObservedAt
	}
	switch age := now.Sub(observed); {
	case age > t.QuietAfter:
		return feedQuiet
	case age > t.DelayedAfter:
		return feedDelayed
	}
	return feedHealthy
}

type alertAction int

const (
	// keepAlert updates an open alert's status, if there is one.
	keepAlert alertAction = iota
	openAlert
	resolveAlert
)

// feedAlertAction says what the check does with a feed's alert. A quiet or
// failing feed opens an alert only in fire season. Outside it, an alert that
// is already open stays open until the feed recovers.
func feedAlertAction(status string, inSeason bool) alertAction {
	switch {
	case status == feedHealthy || status == feedDelayed:
		return resolveAlert
	case inSeason:
		return openAlert
	}
	return keepAlert
}

type feedDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadFeedFacts(ctx context.Context, db feedDB, workspaceID *string, now time.Time, liveProducts []string) ([]feedFacts, error) {
	rows, err := db.Query(ctx, feedsSQL, workspaceID, now, liveProducts)
	if err != nil {
		return nil, fmt.Errorf("read feed health: %w", err)
	}
	facts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (feedFacts, error) {
		var f feedFacts
		err := row.Scan(&f.WorkspaceID, &f.WorkspaceSlug, &f.Settings, &f.Source, &f.Product, &f.FirstRunAt,
			&f.LastRunAt, &f.LastRunStatus, &f.LastError, &f.LastSuccessAt, &f.LatestObservedAt,
			&f.FailuresLast24h, &f.ProcessedRecords, &f.ProcessingP95Seconds, &f.WithinGate,
			&f.FirstSeenRecords, &f.FirstSeenP50Seconds, &f.FirstSeenP95Seconds,
			&f.LatestScanAt, &f.ListedScans, &f.ScanP50Seconds, &f.ScanP95Seconds, &f.MissingScans)
		return f, err
	})
	if err != nil {
		return nil, fmt.Errorf("collect feed health: %w", err)
	}
	return facts, nil
}

type feedCheckResult struct {
	Feeds, Opened, Resolved int
}

type feedCheckDB interface {
	feedDB
	Begin(context.Context) (pgx.Tx, error)
}

// checkFeedHealth works out every feed's status at now, opens an alert for a
// feed that is quiet or failing in fire season, and resolves the open alert
// of a feed that has recovered.
func checkFeedHealth(ctx context.Context, db feedCheckDB, logger *slog.Logger, now time.Time, liveProducts []string) (feedCheckResult, error) {
	var result feedCheckResult
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		// Overlapping checks would decide from the same open alerts.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('open-fimra:feed-health'))"); err != nil {
			return fmt.Errorf("lock the feed health check: %w", err)
		}
		retired, err := tx.Exec(ctx, `
			UPDATE feed_health_alerts
			SET resolved_at = GREATEST(opened_at, $1), last_checked_at = GREATEST(last_checked_at, $1)
			WHERE source = 'firms' AND NOT (product = ANY($2::text[])) AND resolved_at IS NULL`, now, liveProducts)
		if err != nil {
			return fmt.Errorf("resolve alerts for disabled FIRMS feeds: %w", err)
		}
		result.Resolved = int(retired.RowsAffected())
		facts, err := loadFeedFacts(ctx, tx, nil, now, liveProducts)
		if err != nil {
			return err
		}
		result.Feeds = len(facts)
		for _, f := range facts {
			settings, err := readFeedSettings(f.Settings)
			if err != nil && logger != nil {
				logger.Warn("Using the default feed health settings", "workspace", f.WorkspaceSlug, "error", err)
			}
			status := feedStatus(f, settings.Thresholds, now)
			switch feedAlertAction(status, settings.Season.contains(now)) {
			case openAlert:
				details, err := json.Marshal(map[string]any{
					"lastSuccessAt": f.LastSuccessAt, "latestObservedAt": f.LatestObservedAt,
					"failuresLast24h": f.FailuresLast24h, "lastRunStatus": f.LastRunStatus,
				})
				if err != nil {
					return fmt.Errorf("encode feed alert details: %w", err)
				}
				var opened bool
				err = tx.QueryRow(ctx, `
					INSERT INTO feed_health_alerts (workspace_id, source, product, opened_status, status,
					                                opened_at, last_checked_at, details)
					VALUES ($1, $2, $3, $4, $4, $5, $5, $6)
					ON CONFLICT (workspace_id, source, product) WHERE resolved_at IS NULL DO UPDATE
					SET status = EXCLUDED.status,
					    last_checked_at = GREATEST(feed_health_alerts.last_checked_at, EXCLUDED.last_checked_at)
					RETURNING xmax = 0`, f.WorkspaceID, f.Source, f.Product, status, now, details).Scan(&opened)
				if err != nil {
					return fmt.Errorf("record the feed alert for %s %s: %w", f.Source, f.Product, err)
				}
				if opened {
					result.Opened++
					if logger != nil {
						logger.Warn("Feed health alert opened", "workspace", f.WorkspaceSlug,
							"source", f.Source, "product", f.Product, "status", status)
					}
				}
			case keepAlert:
				if _, err := tx.Exec(ctx, `
					UPDATE feed_health_alerts SET status = $4, last_checked_at = GREATEST(last_checked_at, $5)
					WHERE (workspace_id, source, product) = ($1, $2, $3) AND resolved_at IS NULL`,
					f.WorkspaceID, f.Source, f.Product, status, now); err != nil {
					return fmt.Errorf("update the feed alert for %s %s: %w", f.Source, f.Product, err)
				}
			case resolveAlert:
				tag, err := tx.Exec(ctx, `
					UPDATE feed_health_alerts
					SET status = $4, last_checked_at = GREATEST(last_checked_at, $5), resolved_at = GREATEST(opened_at, $5)
					WHERE (workspace_id, source, product) = ($1, $2, $3) AND resolved_at IS NULL`,
					f.WorkspaceID, f.Source, f.Product, status, now)
				if err != nil {
					return fmt.Errorf("resolve the feed alert for %s %s: %w", f.Source, f.Product, err)
				}
				if tag.RowsAffected() > 0 {
					result.Resolved++
					if logger != nil {
						logger.Info("Feed health alert resolved", "workspace", f.WorkspaceSlug,
							"source", f.Source, "product", f.Product, "status", status)
					}
				}
			}
		}
		return nil
	})
	return result, err
}

type feedHealthArgs struct{}

func (feedHealthArgs) Kind() string { return "feed_health_check" }

type feedHealthWorker struct {
	river.WorkerDefaults[feedHealthArgs]
	db           feedCheckDB
	logger       *slog.Logger
	liveProducts []string
}

func (w *feedHealthWorker) Work(ctx context.Context, _ *river.Job[feedHealthArgs]) error {
	_, err := checkFeedHealth(ctx, w.db, w.logger, time.Now(), w.liveProducts)
	return err
}

//go:embed feed_latency.sql
var feedLatencySQL string

type latencyStats struct {
	Count      int      `json:"count"`
	P50Seconds *float64 `json:"p50Seconds"`
	P95Seconds *float64 `json:"p95Seconds"`
	MaxSeconds *float64 `json:"maxSeconds"`
}

type feedLatency struct {
	Satellite   string       `json:"satellite"`
	WindowHours int          `json:"windowHours"`
	FirstSeen   latencyStats `json:"firstSeen"`
	Matched     latencyStats `json:"matched"`
}

type feedKey struct{ source, product string }

func loadFeedLatency(ctx context.Context, db feedDB, workspaceID string, now time.Time) (map[feedKey][]feedLatency, error) {
	rows, err := db.Query(ctx, feedLatencySQL, workspaceID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[feedKey][]feedLatency)
	for rows.Next() {
		var key feedKey
		var sample feedLatency
		seen, matched := &sample.FirstSeen, &sample.Matched
		if err := rows.Scan(&key.source, &key.product, &sample.Satellite, &sample.WindowHours,
			&seen.Count, &seen.P50Seconds, &seen.P95Seconds, &seen.MaxSeconds,
			&matched.Count, &matched.P50Seconds, &matched.P95Seconds, &matched.MaxSeconds); err != nil {
			return nil, err
		}
		result[key] = append(result[key], sample)
	}
	return result, rows.Err()
}
