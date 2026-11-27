package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/lsasaf"
)

//go:embed lsasaf_ingest.sql
var lsasafIngestSQL string

//go:embed lsasaf_promote.sql
var lsasafPromoteSQL string

const lsasafWindow = 24 * time.Hour

type lsasafRule struct {
	hits, scans int
}

type lsasafPollArgs struct{}

func (lsasafPollArgs) Kind() string { return "lsasaf_poll" }

type lsasafPollWorker struct {
	river.WorkerDefaults[lsasafPollArgs]
	pool     *pgxpool.Pool
	client   *lsasaf.Client
	rule     lsasafRule
	interval time.Duration
}

func (*lsasafPollWorker) Timeout(*river.Job[lsasafPollArgs]) time.Duration { return 8 * time.Minute }

func (w *lsasafPollWorker) Work(ctx context.Context, _ *river.Job[lsasafPollArgs]) error {
	return pollLSASAF(ctx, w.pool, w.client, w.rule, w.interval, time.Now().UTC())
}

func addLSASAFJobs(pool *pgxpool.Pool, workers *river.Workers, queues map[string]river.QueueConfig, periodic []*river.PeriodicJob) ([]*river.PeriodicJob, error) {
	enabled, err := strconv.ParseBool(envOr("FIMRA_LSASAF_ENABLED", "false"))
	if err != nil {
		return nil, errors.New("FIMRA_LSASAF_ENABLED must be a boolean")
	}
	if !enabled {
		return periodic, nil
	}
	interval, err := time.ParseDuration(envOr("FIMRA_LSASAF_POLL_INTERVAL", "5m"))
	if err != nil || interval < time.Minute || interval > 15*time.Minute {
		return nil, errors.New("FIMRA_LSASAF_POLL_INTERVAL must be a duration from 1m to 15m")
	}
	hits, hitsErr := strconv.Atoi(envOr("FIMRA_LSASAF_PERSISTENCE_HITS", "2"))
	scans, scansErr := strconv.Atoi(envOr("FIMRA_LSASAF_PERSISTENCE_SCANS", "3"))
	if hitsErr != nil || scansErr != nil || hits < 2 || hits > scans || scans > 8 {
		return nil, errors.New("LSA SAF persistence requires 2 to 8 hits within at most 8 consecutive scans")
	}
	username := os.Getenv("FIMRA_LSASAF_USERNAME")
	password := os.Getenv("FIMRA_LSASAF_PASSWORD")
	if path := os.Getenv("FIMRA_LSASAF_PASSWORD_FILE"); path != "" {
		if password != "" {
			return nil, errors.New("configure only one LSA SAF password setting")
		}
		secret, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New("could not read FIMRA_LSASAF_PASSWORD_FILE")
		}
		password = strings.TrimRight(string(secret), "\r\n")
	}
	if username == "" || strings.Contains(username, ":") || password == "" {
		return nil, errors.New("LSA SAF needs a username and password")
	}
	const queue = "lsasaf"
	queues[queue] = river.QueueConfig{MaxWorkers: 1}
	river.AddWorker(workers, &lsasafPollWorker{
		pool: pool, client: lsasaf.NewClient(lsasaf.Endpoint, username, password, &http.Client{Timeout: 15 * time.Second}),
		rule: lsasafRule{hits, scans}, interval: interval,
	})
	return append(periodic, river.NewPeriodicJob(river.PeriodicInterval(interval), func() (river.JobArgs, *river.InsertOpts) {
		return lsasafPollArgs{}, &river.InsertOpts{
			Queue: queue, MaxAttempts: 1,
			UniqueOpts: river.UniqueOpts{ByQueue: true, ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
				rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			}},
		}
	}, &river.PeriodicJobOpts{RunOnStart: true})), nil
}

type lsasafArea struct {
	workspaceArea
	known        map[time.Time]bool
	previousPoll *time.Time
}

type lsasafScan struct {
	slot   time.Time
	pixels []lsasaf.Pixel
}

func pollLSASAF(ctx context.Context, db interface {
	ingestDB
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, client *lsasaf.Client, rule lsasafRule, interval time.Duration, now time.Time) error {
	start := now.Add(-lsasafWindow)
	rows, err := db.Query(ctx, `
		SELECT w.id::text, ST_XMin(w.boundary), ST_YMin(w.boundary),
		       ST_XMax(w.boundary), ST_YMax(w.boundary),
		       ARRAY(SELECT s.slot FROM lsasaf_scans s WHERE s.workspace_id=w.id AND s.slot >= $1 ORDER BY slot),
		       ARRAY(SELECT s.validated_at IS NOT NULL FROM lsasaf_scans s
		           WHERE s.workspace_id=w.id AND s.slot >= $1 ORDER BY slot),
		       previous.checked_at
		FROM workspaces w LEFT JOIN LATERAL (
		    SELECT CASE WHEN r.status='succeeded' THEN r.window_end END AS checked_at
		    FROM ingest_runs r WHERE r.workspace_id=w.id AND (r.kind,r.source,r.product)=('poll','lsasaf','SEVIRI_IODC_FRP')
		    ORDER BY r.started_at DESC,r.id DESC LIMIT 1
		) previous ON true
		WHERE w.kind <> 'national' AND w.boundary IS NOT NULL AND NOT ST_IsEmpty(w.boundary)
		ORDER BY w.id`, start)
	if err != nil {
		return fmt.Errorf("read LSA SAF workspace bounds: %w", err)
	}
	areas, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (lsasafArea, error) {
		var a lsasafArea
		var slots []time.Time
		var complete []bool
		if err := row.Scan(&a.id, &a.box.West, &a.box.South, &a.box.East, &a.box.North, &slots, &complete, &a.previousPoll); err != nil {
			return a, err
		}
		a.known = make(map[time.Time]bool, len(slots))
		for i, slot := range slots {
			a.known[slot.UTC()] = complete[i]
		}
		return a, nil
	})
	if err != nil || len(areas) == 0 {
		return err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	slots, listErr := client.List(fetchCtx, start, now)
	listed := time.Now().UTC()
	scans := make(map[time.Time]lsasafScan)
	slotErrors := make(map[time.Time]error)
	// Get fresh slots first after a gap, then apply them in observation order.
	for _, slot := range slices.Backward(slots) {
		if !slices.ContainsFunc(areas, func(a lsasafArea) bool { return !a.known[slot] }) {
			continue
		}
		pixels, err := client.Fetch(fetchCtx, slot, func(pixel lsasaf.Pixel) bool {
			return slices.ContainsFunc(areas, func(a lsasafArea) bool {
				return !a.known[slot] && footprintInBox(pixel, a.box)
			})
		})
		switch {
		case err == nil:
			scans[slot] = lsasafScan{slot, pixels}
		case !errors.Is(err, lsasaf.ErrMissing):
			slotErrors[slot] = fmt.Errorf("LSA SAF slot %s: %w", slot.Format(time.RFC3339), err)
		}
		if fetchCtx.Err() != nil {
			break
		}
	}
	fetchedAt := time.Now().UTC()
	var failures []error
	for _, area := range areas {
		var pending []lsasafScan
		var listedSlots []time.Time
		var retried bool
		problems := []error{listErr}
		for _, slot := range slots {
			complete, listedBefore := area.known[slot]
			if complete {
				continue
			}
			retried = retried || listedBefore
			listedSlots = append(listedSlots, slot)
			if s, ok := scans[slot]; ok {
				pending = append(pending, s)
			}
			problems = append(problems, slotErrors[slot])
		}
		readErr := errors.Join(problems...)
		timing := fetchTiming{fetchedAt: fetchedAt}
		if readErr == nil && !retried && len(pending) == len(listedSlots) && area.previousPoll != nil {
			// The previous poll start is a conservative lower bound for its
			// listings, which can span multiple requests on a day boundary.
			if gap := now.Sub(*area.previousPoll); gap > 0 && gap <= 2*interval && area.previousPoll.Before(fetchedAt) {
				timing.previousFetchedAt = area.previousPoll
			}
		}
		_, err := runIngestTimed(ctx, db, ingestRun{
			WorkspaceID: &area.id, Source: lsasaf.Source, Product: lsasaf.Product,
			WindowStart: start, WindowEnd: now, AllowPartial: true,
			apply: func(ctx context.Context, tx pgx.Tx, runID int64, counts *ingestCounts) (*time.Time, error) {
				return applyLSASAF(ctx, tx, area, now, rule, listedSlots, listed, pending, runID, counts)
			},
		}, func(context.Context) ([]firms.Observation, fetchTiming, error) {
			return nil, timing, readErr
		})
		if err != nil {
			failures = append(failures, fmt.Errorf("poll LSA SAF for workspace %s: %w", area.id, err))
		}
	}
	return errors.Join(failures...)
}

func footprintInBox(pixel lsasaf.Pixel, box firms.Bounds) bool {
	west, east := pixel.Footprint[0][0], pixel.Footprint[0][0]
	south, north := pixel.Footprint[0][1], pixel.Footprint[0][1]
	for _, v := range pixel.Footprint[1:] {
		west, east = min(west, v[0]), max(east, v[0])
		south, north = min(south, v[1]), max(north, v[1])
	}
	return west <= box.East && east >= box.West && south <= box.North && north >= box.South
}

func applyLSASAF(ctx context.Context, tx pgx.Tx, area lsasafArea, now time.Time, rule lsasafRule,
	listedSlots []time.Time, listedAt time.Time, scans []lsasafScan, runID int64, counts *ingestCounts) (*time.Time, error) {
	if len(listedSlots) == 0 {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, detectionWriteLock); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL jit = off"); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO lsasaf_scans (workspace_id,slot,first_listed_at)
		SELECT $1, slot, $3 FROM unnest($2::timestamptz[]) AS listed(slot) ON CONFLICT DO NOTHING`,
		area.id, listedSlots, listedAt); err != nil {
		return nil, err
	}
	if len(scans) == 0 {
		return nil, nil
	}
	var latest *time.Time
	for _, s := range scans {
		tag, err := tx.Exec(ctx, `UPDATE lsasaf_scans SET validated_at = clock_timestamp()
			WHERE workspace_id=$1 AND slot=$2 AND validated_at IS NULL`, area.id, s.slot)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		type record struct {
			firms.Observation
			Key       string          `json:"source_key"`
			Line      int             `json:"line"`
			Pixel     int             `json:"pixel"`
			Footprint json.RawMessage `json:"footprint"`
		}
		pixels := make([]record, 0, len(s.pixels))
		for _, p := range s.pixels {
			if footprintInBox(p, area.box) {
				pixels = append(pixels, record{p.Observation,
					area.id + ":" + p.Observation.SourceKey(lsasaf.Source, lsasaf.Product),
					p.Line, p.Column, p.Geometry()})
			}
		}
		if len(pixels) == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, "SELECT ensure_detection_partition($1::date)", s.slot); err != nil {
			return nil, err
		}
		batch, err := json.Marshal(pixels)
		if err != nil {
			return nil, err
		}
		var fetched, inserted int
		if err := tx.QueryRow(ctx, lsasafIngestSQL, batch, area.id, s.slot, runID).
			Scan(&fetched, &inserted); err != nil {
			return nil, fmt.Errorf("match LSA SAF footprints: %w", err)
		}
		counts.Fetched += fetched
		counts.Inserted += inserted
		counts.Matched += inserted
		counts.Duplicates += fetched - inserted
		if inserted > 0 {
			latest = &s.slot
		}
	}
	if _, err := tx.Exec(ctx, `SELECT lsasaf_link_existing($1,$2,$3)`, area.id, scans[0].slot, scans[len(scans)-1].slot); err != nil {
		return nil, fmt.Errorf("link LSA SAF watches: %w", err)
	}
	if _, err := tx.Exec(ctx, lsasafPromoteSQL, area.id, now, rule.hits, rule.scans); err != nil {
		return nil, fmt.Errorf("promote LSA SAF watches: %w", err)
	}
	return latest, nil
}
