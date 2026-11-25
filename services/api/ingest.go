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

type ingestRun struct {
	WorkspaceID *string
	Source      string
	Product     string
	WindowStart time.Time
	WindowEnd   time.Time
}

type ingestCounts struct {
	Fetched, Inserted, Duplicates, Matched int
}

type readObservations func(context.Context) ([]firms.Observation, error)

func runIngest(ctx context.Context, db ingestDB, run ingestRun, read readObservations) (ingestCounts, error) {
	var id int64
	var counts ingestCounts
	inferWindow := run.WindowEnd.IsZero()
	if inferWindow {
		run.WindowEnd = time.Now()
		run.WindowStart = run.WindowEnd
	}
	err := db.QueryRow(ctx, `
		INSERT INTO ingest_runs (workspace_id, source, product, window_start, window_end)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		run.WorkspaceID, run.Source, run.Product, run.WindowStart, run.WindowEnd).Scan(&id)
	if err != nil {
		return counts, fmt.Errorf("start ingest run: %w", err)
	}

	observations, err := read(ctx)
	counts.Fetched = len(observations)
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
	if err == nil {
		err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			if err := insertAndMatch(ctx, tx, run, observations, &counts); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
				UPDATE ingest_runs SET status = 'succeeded', finished_at = clock_timestamp(),
				    fetched = $2, inserted = $3, duplicates = $4, window_start = $5, window_end = $6
				WHERE id = $1`, id, counts.Fetched, counts.Inserted, counts.Duplicates, run.WindowStart, run.WindowEnd)
			if err != nil {
				return fmt.Errorf("finish ingest run: %w", err)
			}
			return nil
		})
	}
	if err == nil {
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

func insertAndMatch(ctx context.Context, tx pgx.Tx, run ingestRun, observations []firms.Observation, counts *ingestCounts) error {
	if len(observations) == 0 {
		return nil
	}
	type record struct {
		firms.Observation
		SourceKey string `json:"source_key"`
	}
	records := make([]record, 0, len(observations))
	months := make(map[time.Time]struct{})
	for _, o := range observations {
		records = append(records, record{o, o.SourceKey(run.Source, run.Product)})
		year, month, _ := o.ObservedAt.UTC().Date()
		months[time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)] = struct{}{}
	}
	batch, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("encode detections: %w", err)
	}

	// Partition creation locks the parent table. Take the same lock order in
	// every ingest, including concurrent fixture imports and River jobs.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('open-fimra:detection-ingest'))"); err != nil {
		return fmt.Errorf("lock detection ingest: %w", err)
	}
	for month := range months {
		if _, err := tx.Exec(ctx, "SELECT ensure_detection_partition($1::date)", month); err != nil {
			return fmt.Errorf("ensure detection partition for %s: %w", month.Format("2006-01"), err)
		}
	}
	err = tx.QueryRow(ctx, ingestSQL, batch, run.Source, run.Product).Scan(&counts.Inserted, &counts.Matched)
	if err != nil {
		return fmt.Errorf("insert and match detections: %w", err)
	}
	counts.Duplicates = counts.Fetched - counts.Inserted
	return nil
}
