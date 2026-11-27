package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/nodd"
)

const noddWindow = 6 * time.Hour

type noddPollArgs struct{}

func (noddPollArgs) Kind() string { return "noaa_nodd_poll" }

type noddPollWorker struct {
	river.WorkerDefaults[noddPollArgs]
	pool       *pgxpool.Pool
	client     *nodd.Client
	satellites []nodd.Satellite
}

func (*noddPollWorker) Timeout(*river.Job[noddPollArgs]) time.Duration { return 10 * time.Minute }

func (w *noddPollWorker) Work(ctx context.Context, _ *river.Job[noddPollArgs]) error {
	return pollNODD(ctx, w.pool, w.client, w.satellites, time.Now().UTC())
}

func addNODDJobs(pool *pgxpool.Pool, workers *river.Workers, queues map[string]river.QueueConfig, periodic []*river.PeriodicJob) ([]*river.PeriodicJob, error) {
	enabled, err := strconv.ParseBool(envOr("FIMRA_NOAA_NODD_ENABLED", "false"))
	if err != nil {
		return nil, errors.New("FIMRA_NOAA_NODD_ENABLED must be a boolean")
	}
	if !enabled {
		return periodic, nil
	}
	interval, err := time.ParseDuration(envOr("FIMRA_NOAA_NODD_POLL_INTERVAL", "5m"))
	if err != nil || interval < time.Minute || interval > 30*time.Minute {
		return nil, errors.New("FIMRA_NOAA_NODD_POLL_INTERVAL must be a duration from 1m to 30m")
	}
	var satellites []nodd.Satellite
	for _, code := range strings.Split(envOr("FIMRA_NOAA_NODD_SATELLITES", "n20,n21"), ",") {
		code = strings.ToLower(strings.TrimSpace(code))
		i := slices.IndexFunc(nodd.Satellites[:], func(s nodd.Satellite) bool { return s.Code == code })
		if i < 0 || slices.Contains(satellites, nodd.Satellites[i]) {
			return nil, fmt.Errorf("FIMRA_NOAA_NODD_SATELLITES must list n20 and/or n21 once each, got %q", code)
		}
		satellites = append(satellites, nodd.Satellites[i])
	}

	const queue = "noaa_nodd"
	queues[queue] = river.QueueConfig{MaxWorkers: 1}
	river.AddWorker(workers, &noddPollWorker{
		pool: pool, client: nodd.NewClient(&http.Client{Timeout: 25 * time.Second}), satellites: satellites,
	})
	return append(periodic, river.NewPeriodicJob(
		river.PeriodicInterval(interval),
		func() (river.JobArgs, *river.InsertOpts) {
			return noddPollArgs{}, &river.InsertOpts{
				Queue: queue, MaxAttempts: 1,
				UniqueOpts: river.UniqueOpts{ByQueue: true, ByState: []rivertype.JobState{
					rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
					rivertype.JobStateRetryable, rivertype.JobStateScheduled,
				}},
			}
		}, &river.PeriodicJobOpts{RunOnStart: true},
	)), nil
}

func pollNODD(ctx context.Context, db interface {
	ingestDB
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, client *nodd.Client, satellites []nodd.Satellite, now time.Time) error {
	rows, err := db.Query(ctx, `
		SELECT id::text, ST_XMin(boundary), ST_YMin(boundary), ST_XMax(boundary), ST_YMax(boundary)
		FROM workspaces WHERE boundary IS NOT NULL AND NOT ST_IsEmpty(boundary) ORDER BY slug`)
	if err != nil {
		return fmt.Errorf("read NOAA workspace bounds: %w", err)
	}
	areas, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (workspaceArea, error) {
		var a workspaceArea
		err := row.Scan(&a.id, &a.box.West, &a.box.South, &a.box.East, &a.box.North)
		return a, err
	})
	if err != nil {
		return fmt.Errorf("collect NOAA workspace bounds: %w", err)
	}
	if len(areas) == 0 {
		return nil
	}
	boxes := make([]firms.Bounds, len(areas))
	for i, area := range areas {
		boxes[i] = area.box
	}
	start := now.Add(-noddWindow)
	var failures []error
	for _, sat := range satellites {
		var observations [][]firms.Observation
		var fetchErr error
		for i, area := range areas {
			_, err := runIngest(ctx, db, ingestRun{
				WorkspaceID: &area.id, Source: nodd.Source, Product: sat.Product,
				WindowStart: start, WindowEnd: now, AllowPartial: true,
			}, func(ctx context.Context) ([]firms.Observation, error) {
				// Every workspace has its own run, with a single shared fetch.
				if observations == nil && fetchErr == nil {
					observations, fetchErr = client.Fetch(ctx, sat, start, now, boxes)
				}
				if observations == nil {
					return nil, fetchErr
				}
				return observations[i], fetchErr
			})
			if err != nil {
				failures = append(failures, fmt.Errorf("poll %s for workspace %s: %w", sat.Product, area.id, err))
			}
		}
	}
	return errors.Join(failures...)
}

// Only the operational NOAA I-band and the matching FIRMS VIIRS products
// can be linked across sources.
func noddCounterparts(source, product string) (string, []string) {
	for _, sat := range nodd.Satellites {
		stem := strings.TrimSuffix(sat.Product, "_EFIRE")
		nrt, sp := stem+"_NRT", stem+"_SP"
		if source == nodd.Source && product == sat.Product {
			return firms.Source, []string{nrt, sp}
		}
		if source == firms.Source && (product == nrt || product == sp) {
			return nodd.Source, []string{sat.Product}
		}
	}
	return "", nil
}
