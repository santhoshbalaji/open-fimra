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

	const heartbeat = "heartbeat"
	queues := map[string]river.QueueConfig{heartbeat: {MaxWorkers: 1}}
	periodic := []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(5*time.Minute),
			func() (river.JobArgs, *river.InsertOpts) {
				return heartbeatArgs{}, &river.InsertOpts{Queue: heartbeat}
			},
			&river.PeriodicJobOpts{RunOnStart: true},
		),
	}
	if firmsMapKey != "" {
		const queue = "firms"
		interval := 15 * time.Minute
		queues[queue] = river.QueueConfig{MaxWorkers: 1}
		river.AddWorker(workers, &firmsPollWorker{pool: pool, area: firms.NewArea(firmsMapKey, nil)})
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
