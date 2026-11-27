package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

//go:embed ingest.sql
var ingestSQL string

type ingestDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Kinds of ingest run. Feed health reads only polls.
const (
	runPoll     = "poll"
	runBackfill = "backfill"
	runImport   = "import"
)

type ingestRun struct {
	WorkspaceID *string
	// Kind defaults to an import without a workspace and a poll with one.
	Kind        string
	Source      string
	Product     string
	WindowStart time.Time
	WindowEnd   time.Time
	// Synthetic observations belong only to the workspace that replays them.
	WorkspaceOnly bool
	// Successful objects from a multipart poll can be ingested despite a fetch error.
	AllowPartial bool
}

type ingestCounts struct {
	Fetched, Inserted, Duplicates, Matched, Linked int
}

type readObservations func(context.Context) ([]firms.Observation, error)

// fetchTiming preserves the original response time when several runs reuse a
// fetch. A comparable previous response is optional for live polls.
type fetchTiming struct {
	fetchedAt         time.Time
	previousFetchedAt *time.Time
}

func runIngest(ctx context.Context, db ingestDB, run ingestRun, read readObservations) (ingestCounts, error) {
	return runIngestTimed(ctx, db, run, func(ctx context.Context) ([]firms.Observation, fetchTiming, error) {
		observations, err := read(ctx)
		return observations, fetchTiming{}, err
	})
}

func runIngestTimed(ctx context.Context, db ingestDB, run ingestRun, read func(context.Context) ([]firms.Observation, fetchTiming, error)) (ingestCounts, error) {
	var id int64
	var counts ingestCounts
	inferWindow := run.WindowEnd.IsZero()
	if inferWindow {
		run.WindowEnd = time.Now()
		run.WindowStart = run.WindowEnd
	}
	if run.Kind == "" {
		run.Kind = runPoll
		if run.WorkspaceID == nil {
			run.Kind = runImport
		}
	}
	err := db.QueryRow(ctx, `
		INSERT INTO ingest_runs (workspace_id, kind, source, product, window_start, window_end)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		run.WorkspaceID, run.Kind, run.Source, run.Product, run.WindowStart, run.WindowEnd).Scan(&id)
	if err != nil {
		return counts, fmt.Errorf("start ingest run: %w", err)
	}

	observations, timing, readErr := read(ctx)
	err = readErr
	counts.Fetched = len(observations)
	var latest *time.Time
	for i := range observations {
		if latest == nil || observations[i].ObservedAt.After(*latest) {
			latest = &observations[i].ObservedAt
		}
	}
	if inferWindow && len(observations) > 0 {
		run.WindowStart, run.WindowEnd = observations[0].ObservedAt, observations[0].ObservedAt
		for _, o := range observations[1:] {
			if o.ObservedAt.Before(run.WindowStart) {
				run.WindowStart = o.ObservedAt
			}
			if o.ObservedAt.After(run.WindowEnd) {
				run.WindowEnd = o.ObservedAt
			}
		}
	}
	if err == nil || (run.AllowPartial && len(observations) > 0 && ctx.Err() == nil) {
		status := "succeeded"
		var message *string
		if readErr != nil {
			status = "failed"
			detail := readErr.Error()
			message = &detail
		}
		err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			// The response has been read: processing time starts here.
			if timing.fetchedAt.IsZero() {
				if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&timing.fetchedAt); err != nil {
					return fmt.Errorf("read the fetch time: %w", err)
				}
			}
			if err := insertAndMatch(ctx, tx, id, run, observations, &counts); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
				UPDATE ingest_runs SET status = $10, error_message = $11, finished_at = clock_timestamp(),
				    fetched = $2, inserted = $3, duplicates = $4, window_start = $5, window_end = $6,
				    fetched_at = $7, latest_observed_at = $8, linked = $9, previous_fetched_at = $12
				WHERE id = $1`, id, counts.Fetched, counts.Inserted, counts.Duplicates, run.WindowStart, run.WindowEnd,
				timing.fetchedAt, latest, counts.Linked, status, message, timing.previousFetchedAt)
			if err != nil {
				return fmt.Errorf("finish ingest run: %w", err)
			}
			return nil
		})
	}
	if err == nil {
		if readErr != nil {
			return counts, fmt.Errorf("partial ingest run %d: %w", id, readErr)
		}
		return counts, nil
	}

	// The caller may have cancelled the fetch or database work.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, recordErr := db.Exec(finish, `
		UPDATE ingest_runs SET status = 'failed', finished_at = clock_timestamp(),
		    fetched = $2, error_message = $3, window_start = $4, window_end = $5
		WHERE id = $1 AND status = 'running'`, id, counts.Fetched, err.Error(), run.WindowStart, run.WindowEnd); recordErr != nil {
		err = errors.Join(err, fmt.Errorf("record ingest failure: %w", recordErr))
	}
	return ingestCounts{Fetched: counts.Fetched}, fmt.Errorf("ingest run %d: %w", id, err)
}

func insertAndMatch(ctx context.Context, tx pgx.Tx, runID int64, run ingestRun, observations []firms.Observation, counts *ingestCounts) error {
	if len(observations) == 0 {
		return nil
	}
	type record struct {
		firms.Observation
		SourceKey string `json:"source_key"`
	}
	records := make([]record, 0, len(observations))
	months := make(map[time.Time]struct{})
	var scope *string
	if run.WorkspaceOnly {
		if run.WorkspaceID == nil {
			return errors.New("a scoped import needs a workspace")
		}
		scope = run.WorkspaceID
	}
	for _, o := range observations {
		key := o.SourceKey(run.Source, run.Product)
		if scope != nil {
			key = *scope + ":" + key
		}
		records = append(records, record{o, key})
		year, month, _ := o.ObservedAt.UTC().Date()
		months[time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)] = struct{}{}
	}
	batch, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("encode detections: %w", err)
	}

	// Partition creation locks the parent table. Take the same lock order in
	// every ingest, including concurrent fixture imports and River jobs.
	if _, err := tx.Exec(ctx, detectionWriteLock); err != nil {
		return fmt.Errorf("lock detection ingest: %w", err)
	}
	for month := range months {
		if _, err := tx.Exec(ctx, "SELECT ensure_detection_partition($1::date)", month); err != nil {
			return fmt.Errorf("ensure detection partition for %s: %w", month.Format("2006-01"), err)
		}
	}
	// Keep small spatial batches from spending more time compiling than
	// matching, including on callers' connections outside openPool.
	if _, err := tx.Exec(ctx, "SET LOCAL jit = off"); err != nil {
		return fmt.Errorf("disable JIT for detection ingest: %w", err)
	}
	crossSource, crossProducts := noddCounterparts(run.Source, run.Product)
	err = tx.QueryRow(ctx, ingestSQL, batch, run.Source, run.Product, firms.Counterpart(run.Product), runID, scope, crossProducts, crossSource).
		Scan(&counts.Inserted, &counts.Matched, &counts.Linked)
	if err != nil {
		return fmt.Errorf("insert and match detections: %w", err)
	}
	counts.Duplicates = counts.Fetched - counts.Inserted
	return nil
}
