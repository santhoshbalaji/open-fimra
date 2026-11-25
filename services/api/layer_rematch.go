package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

//go:embed layer_rematch.sql
var rematchSQL string

// Use the ingest lock so a replay batch or new detection sees one complete set
// of layers. A publish between batches is safe: the newer replay covers its
// own window and every updated match records the versions it actually used.
const detectionWriteLock = "SELECT pg_advisory_xact_lock(hashtext('open-fimra:detection-ingest'))"

const rematchQueue = "layer_rematch"

type rematchArgs struct {
	VersionID string `json:"versionId"`
}

func (rematchArgs) Kind() string { return rematchQueue }

type rematchWorker struct {
	river.WorkerDefaults[rematchArgs]
	pool *pgxpool.Pool
}

func (*rematchWorker) Timeout(*river.Job[rematchArgs]) time.Duration { return 30 * time.Minute }

func (w *rematchWorker) Work(ctx context.Context, job *river.Job[rematchArgs]) error {
	for {
		var done bool
		err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, detectionWriteLock); err != nil {
				return fmt.Errorf("lock detection writes: %w", err)
			}
			err := tx.QueryRow(ctx, rematchSQL, job.Args.VersionID).Scan(&done)
			if errors.Is(err, pgx.ErrNoRows) {
				done = true // A previous attempt finished, or the layer was deleted.
				return nil
			}
			return err
		})
		if err != nil {
			return fmt.Errorf("re-match detections for layer version %s: %w", job.Args.VersionID, err)
		}
		if done {
			return nil
		}
	}
}
