package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// feedCheckInterval is how often feed health is checked.
const feedCheckInterval = 5 * time.Minute

type heartbeatArgs struct{}

func (heartbeatArgs) Kind() string { return "heartbeat" }

type heartbeatWorker struct {
	river.WorkerDefaults[heartbeatArgs]
}

func (*heartbeatWorker) Work(context.Context, *river.Job[heartbeatArgs]) error {
	return nil
}

func newJobClient(pool *pgxpool.Pool, logger *slog.Logger, firmsMapKey string) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &heartbeatWorker{})
	river.AddWorker(workers, &rematchWorker{pool: pool})

	const heartbeat = "heartbeat"
	queues := map[string]river.QueueConfig{heartbeat: {MaxWorkers: 1}, rematchQueue: {MaxWorkers: 1}}
	periodic := []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(5*time.Minute),
			func() (river.JobArgs, *river.InsertOpts) {
				return heartbeatArgs{}, &river.InsertOpts{Queue: heartbeat}
			},
			&river.PeriodicJobOpts{RunOnStart: true},
		),
	}

	// Feed health is checked even without a map key: a feed that stopped
	// because its key was removed is a silent failure too.
	const feedHealth = "feed_health"
	queues[feedHealth] = river.QueueConfig{MaxWorkers: 1}
	river.AddWorker(workers, &feedHealthWorker{db: pool, logger: logger})
	periodic = append(periodic, river.NewPeriodicJob(
		river.PeriodicInterval(feedCheckInterval),
		func() (river.JobArgs, *river.InsertOpts) {
			return feedHealthArgs{}, &river.InsertOpts{
				Queue: feedHealth, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByPeriod: feedCheckInterval},
			}
		},
		&river.PeriodicJobOpts{RunOnStart: true},
	))

	if firmsMapKey != "" {
		const queue = "firms"
		interval := 15 * time.Minute
		// The poller and backfill share one area client, and so one rate limiter.
		area := firms.NewArea(firmsMapKey, nil)
		queues[queue] = river.QueueConfig{MaxWorkers: 1}
		river.AddWorker(workers, &firmsPollWorker{pool: pool, area: area})
		// Backfill has its own queue so that a long backfill never delays a poll.
		queues[backfillQueue] = river.QueueConfig{MaxWorkers: 1}
		river.AddWorker(workers, &firmsBackfillWorker{db: pool, fetch: area.FetchRange, available: area.Availability})
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(interval),
			func() (river.JobArgs, *river.InsertOpts) {
				return firmsPollArgs{}, &river.InsertOpts{
					Queue: queue, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByPeriod: interval},
				}
			},
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}
	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:       logger,
		Queues:       queues,
		Workers:      workers,
		PeriodicJobs: periodic,
	})
}
