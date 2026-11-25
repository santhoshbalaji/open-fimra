package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

type firmsPollArgs struct{}

func (firmsPollArgs) Kind() string { return "firms_poll" }

type firmsPollWorker struct {
	river.WorkerDefaults[firmsPollArgs]
	pool *pgxpool.Pool
	keys *firmsKeys
}

func (*firmsPollWorker) Timeout(*river.Job[firmsPollArgs]) time.Duration {
	return 14 * time.Minute
}

func (w *firmsPollWorker) Work(ctx context.Context, _ *river.Job[firmsPollArgs]) error {
	return pollFIRMS(ctx, w.pool, w.keys.workspaceFetch, time.Now())
}

type fetchArea func(context.Context, string, firms.Bounds) ([]firms.Observation, error)

// fetchFor returns the fetcher for one workspace, or nil when it has no key.
type fetchFor func(ctx context.Context, workspaceID string) (fetchArea, error)

type workspaceArea struct {
	id  string
	box firms.Bounds
}

func pollFIRMS(ctx context.Context, pool interface {
	ingestDB
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, fetchWith fetchFor, now time.Time) error {
	rows, err := pool.Query(ctx, `
		SELECT id::text, ST_XMin(boundary), ST_YMin(boundary), ST_XMax(boundary), ST_YMax(boundary)
		FROM workspaces WHERE boundary IS NOT NULL AND NOT ST_IsEmpty(boundary) ORDER BY slug`)
	if err != nil {
		return fmt.Errorf("read FIRMS workspace bounds: %w", err)
	}
	areas, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (workspaceArea, error) {
		var a workspaceArea
		err := row.Scan(&a.id, &a.box.West, &a.box.South, &a.box.East, &a.box.North)
		return a, err
	})
	if err != nil {
		return fmt.Errorf("collect FIRMS workspace bounds: %w", err)
	}

	// FIRMS day ranges use UTC calendar days, including today.
	now = now.UTC()
	start := now.Truncate(24*time.Hour).AddDate(0, 0, 1-firms.Days)
	var failures []error
	for _, area := range areas {
		fetch, keyErr := fetchWith(ctx, area.id)
		if keyErr == nil && fetch == nil {
			// No key for this workspace. Feed health notices if it stays quiet.
			continue
		}
		if keyErr != nil {
			// Record the failure on each feed so feed health shows the cause.
			fetch = func(context.Context, string, firms.Bounds) ([]firms.Observation, error) {
				return nil, fmt.Errorf("read the FIRMS map key: %w", keyErr)
			}
		}
		for _, product := range firms.Products {
			if ctx.Err() != nil {
				return errors.Join(append(failures, ctx.Err())...)
			}
			_, err := runIngest(ctx, pool, ingestRun{
				WorkspaceID: &area.id, Source: firms.Source, Product: product, WindowStart: start, WindowEnd: now,
			}, func(ctx context.Context) ([]firms.Observation, error) {
				return fetch(ctx, product, area.box)
			})
			if err != nil {
				failures = append(failures, fmt.Errorf("poll %s for workspace %s: %w", product, area.id, err))
			}
		}
	}
	return errors.Join(failures...)
}
