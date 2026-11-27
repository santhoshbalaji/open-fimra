package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
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

// newJobClient starts the FIRMS poller and backfill workers when keys is not
// nil. Each poll reads every workspace's key from its settings, so a key
// saved in the wizard takes effect at the next poll without a restart.
func newJobClient(pool *pgxpool.Pool, logger *slog.Logger, keys *firmsKeys, email notificationChannel, push pushSettings) (*river.Client[pgx.Tx], error) {
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
	river.AddWorker(workers, &feedHealthWorker{db: pool, logger: logger, liveProducts: keys.liveProducts()})
	periodic = append(periodic, river.NewPeriodicJob(
		river.PeriodicInterval(feedCheckInterval),
		func() (river.JobArgs, *river.InsertOpts) {
			return feedHealthArgs{}, &river.InsertOpts{
				Queue: feedHealth, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByPeriod: feedCheckInterval},
			}
		},
		&river.PeriodicJobOpts{RunOnStart: true},
	))

	queues[notificationQueue] = river.QueueConfig{MaxWorkers: 1}
	river.AddWorker(workers, &notificationWorker{db: pool, email: email, push: push})
	periodic = append(periodic, river.NewPeriodicJob(
		river.PeriodicInterval(notificationInterval),
		func() (river.JobArgs, *river.InsertOpts) {
			return notificationArgs{}, &river.InsertOpts{
				Queue: notificationQueue, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByPeriod: notificationInterval},
			}
		},
		&river.PeriodicJobOpts{RunOnStart: true},
	))

	if sender, ok := email.(invitationSender); ok {
		queues[invitationEmailQueue] = river.QueueConfig{MaxWorkers: 1}
		river.AddWorker(workers, &invitationEmailWorker{db: pool, sender: sender})
	}
	queues[invitationCleanup] = river.QueueConfig{MaxWorkers: 1}
	river.AddWorker(workers, &invitationCleanupWorker{db: pool})
	periodic = append(periodic, river.NewPeriodicJob(
		river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) {
			return invitationCleanupArgs{}, &river.InsertOpts{
				Queue: invitationCleanup, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByPeriod: time.Hour},
			}
		},
		&river.PeriodicJobOpts{RunOnStart: true},
	))

	if keys != nil {
		const queue = "firms"
		interval := keys.pollSettings.interval
		states := append(rivertype.UniqueOptsByStateDefault(), rivertype.JobStateDiscarded, rivertype.JobStateCancelled)
		// The poller and backfill share one client, and so one rate limiter, per key.
		// The second worker can discard a tick while a slow poll is running.
		queues[queue] = river.QueueConfig{MaxWorkers: 2}
		river.AddWorker(workers, &firmsPollWorker{pool: pool, keys: keys})
		// Backfill has its own queue so that a long backfill never delays a poll.
		queues[backfillQueue] = river.QueueConfig{MaxWorkers: 1}
		river.AddWorker(workers, &firmsBackfillWorker{db: pool, keys: keys})
		periodic = append(periodic, river.NewPeriodicJob(
			river.PeriodicInterval(interval),
			func() (river.JobArgs, *river.InsertOpts) {
				return firmsPollArgs{}, &river.InsertOpts{
					Queue: queue, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByPeriod: interval, ByState: states},
				}
			},
			&river.PeriodicJobOpts{RunOnStart: true},
		))
	}
	periodic, err := addNODDJobs(pool, workers, queues, periodic)
	if err != nil {
		return nil, err
	}
	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Logger:       logger,
		Queues:       queues,
		Workers:      workers,
		PeriodicJobs: periodic,
	})
}
