package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

type heartbeatArgs struct{}

func (heartbeatArgs) Kind() string { return "heartbeat" }

type heartbeatWorker struct {
	river.WorkerDefaults[heartbeatArgs]
}

func (*heartbeatWorker) Work(context.Context, *river.Job[heartbeatArgs]) error {
	return nil
}

func newJobClient(pool *pgxpool.Pool, logger *slog.Logger) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &heartbeatWorker{})

	const queue = "heartbeat"
	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:  logger,
		Queues:  map[string]river.QueueConfig{queue: {MaxWorkers: 1}},
		Workers: workers,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(5*time.Minute),
				func() (river.JobArgs, *river.InsertOpts) {
					return heartbeatArgs{}, &river.InsertOpts{Queue: queue}
				},
				&river.PeriodicJobOpts{RunOnStart: true},
			),
		},
	})
}
