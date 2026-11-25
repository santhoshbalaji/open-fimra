package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// backfillRequest asks for standard-processing records for one workspace and
// a range of UTC days, both ends included.
type backfillRequest struct {
	WorkspaceID string
	Source      string
	Products    []string
	First, Last time.Time
}

// fetchRange fetches the area for days UTC days from the date of start.
type fetchRange func(ctx context.Context, product string, box firms.Bounds, start time.Time, days int) ([]firms.Observation, error)

// availableDays returns the first and last day that a product has data for.
// A nil availableDays leaves the range as asked.
type availableDays func(ctx context.Context, product string) (first, last time.Time, err error)

type backfillResult struct {
	Chunks, Failed, SkippedDays, UnavailableDays int
	Counts                                       ingestCounts
}

type backfillDB interface {
	ingestDB
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// backfillFIRMS fetches every day in the request that no successful backfill
// run covers yet, in chunks of at most firms.MaxDayRange days. Each chunk is
// one ingest run. A failed chunk is recorded and the next chunk still runs;
// running the backfill again retries it.
func backfillFIRMS(ctx context.Context, db backfillDB, req backfillRequest, fetch fetchRange, available availableDays,
	progress func(product string, chunk firms.Chunk, counts ingestCounts, err error)) (backfillResult, error) {
	var result backfillResult
	if req.Source == "" {
		req.Source = firms.Source
	}
	first, last := firms.Day(req.First), firms.Day(req.Last)
	if last.Before(first) {
		return result, errors.New("the backfill range ends before it starts")
	}
	box, err := workspaceBounds(ctx, db, req.WorkspaceID)
	if err != nil {
		return result, err
	}

	var failures []error
	for _, product := range req.Products {
		if !firms.IsStandard(product) {
			return result, fmt.Errorf("%s is not a FIRMS standard-processing product", product)
		}
		from, to := first, last
		if available != nil {
			// FIRMS answers a day it has not processed yet with an empty file.
			// Never record such a day as done.
			start, end, err := available(ctx, product)
			if err != nil {
				failures = append(failures, fmt.Errorf("check which days %s has: %w", product, err))
				continue
			}
			from, to = maxTime(from, firms.Day(start)), minTime(to, firms.Day(end))
			if from.After(to) {
				result.UnavailableDays += days(first, last)
				continue
			}
			result.UnavailableDays += days(first, last) - days(from, to)
		}

		done, err := finishedDays(ctx, db, req, product, from, to)
		if err != nil {
			return result, err
		}
		result.SkippedDays += len(done)
		for _, chunk := range firms.PlanChunks(from, to, firms.MaxDayRange, func(day time.Time) bool { return done[day] }) {
			if ctx.Err() != nil {
				return result, errors.Join(append(failures, ctx.Err())...)
			}
			result.Chunks++
			counts, err := runIngest(ctx, db, ingestRun{
				WorkspaceID: &req.WorkspaceID, Kind: runBackfill, Source: req.Source, Product: product,
				WindowStart: chunk.Start, WindowEnd: chunk.End(),
			}, func(ctx context.Context) ([]firms.Observation, error) {
				return fetch(ctx, product, box, chunk.Start, chunk.Days)
			})
			if progress != nil {
				progress(product, chunk, counts, err)
			}
			if err != nil {
				result.Failed++
				failures = append(failures, fmt.Errorf("backfill %s from %s for %d days: %w",
					product, chunk.Start.Format(time.DateOnly), chunk.Days, err))
				continue
			}
			result.Counts.Fetched += counts.Fetched
			result.Counts.Inserted += counts.Inserted
			result.Counts.Duplicates += counts.Duplicates
			result.Counts.Matched += counts.Matched
			result.Counts.Linked += counts.Linked
		}
	}
	return result, errors.Join(failures...)
}

func workspaceBounds(ctx context.Context, db ingestDB, workspaceID string) (firms.Bounds, error) {
	var box firms.Bounds
	err := db.QueryRow(ctx, `
		SELECT ST_XMin(boundary), ST_YMin(boundary), ST_XMax(boundary), ST_YMax(boundary)
		FROM workspaces WHERE id = $1 AND boundary IS NOT NULL AND NOT ST_IsEmpty(boundary)`,
		workspaceID).Scan(&box.West, &box.South, &box.East, &box.North)
	if errors.Is(err, pgx.ErrNoRows) {
		return box, fmt.Errorf("workspace %s has no boundary to backfill", workspaceID)
	}
	if err != nil {
		return box, fmt.Errorf("read the workspace boundary: %w", err)
	}
	return box, nil
}

// finishedDays returns the days from first to last that a successful backfill
// run of the same workspace, source and product already covers.
func finishedDays(ctx context.Context, db backfillDB, req backfillRequest, product string, first, last time.Time) (map[time.Time]bool, error) {
	rows, err := db.Query(ctx, `
		SELECT window_start, window_end FROM ingest_runs
		WHERE workspace_id = $1 AND kind = 'backfill' AND source = $2 AND product = $3
		  AND status = 'succeeded' AND window_start < $5 AND window_end > $4`,
		req.WorkspaceID, req.Source, product, first, last.AddDate(0, 0, 1))
	if err != nil {
		return nil, fmt.Errorf("read finished backfill runs: %w", err)
	}
	type window struct{ start, end time.Time }
	windows, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (window, error) {
		var w window
		return w, row.Scan(&w.start, &w.end)
	})
	if err != nil {
		return nil, fmt.Errorf("collect finished backfill runs: %w", err)
	}
	done := make(map[time.Time]bool)
	for _, w := range windows {
		// Count only whole UTC days inside the window.
		for day := firms.Day(w.start.Add(24*time.Hour - time.Nanosecond)); !day.AddDate(0, 0, 1).After(w.end); day = day.AddDate(0, 0, 1) {
			if !day.Before(first) && !day.After(last) {
				done[day] = true
			}
		}
	}
	return done, nil
}

func days(first, last time.Time) int {
	if last.Before(first) {
		return 0
	}
	return int(last.Sub(first).Hours()/24) + 1
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// fixtureRange serves FIRMS files from dir instead of calling FIRMS. The file
// for a product is its lower-case name with .csv, such as modis_sp.csv. It
// returns the records in the box on the requested days, as FIRMS would. The
// command stores fixture records under the demo source, so they never mark
// days as done for a real FIRMS backfill.
func fixtureRange(dir string) fetchRange {
	return func(_ context.Context, product string, box firms.Bounds, start time.Time, days int) ([]firms.Observation, error) {
		if days < 1 || days > firms.MaxDayRange {
			return nil, fmt.Errorf("FIRMS day range must be from 1 to %d, got %d", firms.MaxDayRange, days)
		}
		// A missing file is an error, not an empty day, so that it is not
		// recorded as done.
		file, err := os.Open(filepath.Join(dir, strings.ToLower(product)+".csv"))
		if err != nil {
			return nil, fmt.Errorf("open FIRMS fixture: %w", err)
		}
		defer file.Close()
		all, err := firms.Parse(file, product)
		if err != nil {
			return nil, err
		}
		from := firms.Day(start)
		to := from.AddDate(0, 0, days)
		var out []firms.Observation
		for _, o := range all {
			if !o.ObservedAt.Before(from) && o.ObservedAt.Before(to) &&
				o.Longitude >= box.West && o.Longitude <= box.East && o.Latitude >= box.South && o.Latitude <= box.North {
				out = append(out, o)
			}
		}
		return out, nil
	}
}

// River job that runs a backfill in the service. Running it again, or a
// retry after a failure, skips the chunks that already succeeded.
type firmsBackfillArgs struct {
	WorkspaceID string `json:"workspace_id" river:"unique"`
	// Source names the stored records. It is empty for FIRMS, and set by tests.
	Source   string   `json:"source,omitempty" river:"unique"`
	Products []string `json:"products" river:"unique"`
	First    string   `json:"first" river:"unique"`
	Last     string   `json:"last" river:"unique"`
}

func (firmsBackfillArgs) Kind() string { return "firms_backfill" }

const backfillQueue = "firms_backfill"

func (a firmsBackfillArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: backfillQueue, MaxAttempts: 10, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

func (a firmsBackfillArgs) request() (backfillRequest, error) {
	first, errFirst := time.Parse(time.DateOnly, a.First)
	last, errLast := time.Parse(time.DateOnly, a.Last)
	if err := errors.Join(errFirst, errLast); err != nil {
		return backfillRequest{}, fmt.Errorf("read the backfill dates: %w", err)
	}
	return backfillRequest{WorkspaceID: a.WorkspaceID, Source: a.Source, Products: a.Products, First: first, Last: last}, nil
}

type firmsBackfillWorker struct {
	river.WorkerDefaults[firmsBackfillArgs]
	db        backfillDB
	fetch     fetchRange
	available availableDays
	// keys, when set, chooses the fetchers by the job's workspace instead.
	keys *firmsKeys
}

func (*firmsBackfillWorker) Timeout(*river.Job[firmsBackfillArgs]) time.Duration {
	// A long backfill continues in the next attempt from where it stopped.
	return time.Hour
}

func (w *firmsBackfillWorker) Work(ctx context.Context, job *river.Job[firmsBackfillArgs]) error {
	req, err := job.Args.request()
	if err != nil {
		return river.JobCancel(err)
	}
	fetch, available := w.fetch, w.available
	if w.keys != nil {
		fetch, available, err = w.keys.backfillClient(ctx, req.WorkspaceID)
		if err != nil {
			return fmt.Errorf("choose the FIRMS key: %w", err)
		}
	}
	_, err = backfillFIRMS(ctx, w.db, req, fetch, available, nil)
	return err
}
