package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

const (
	demoSource  = "demo"
	demoProduct = "VIIRS_SNPP_NRT"
)

func runCommand(ctx context.Context, cfg config, args []string) error {
	if args[0] != "ingest-firms" {
		return fmt.Errorf("unknown command %q", args[0])
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	file := flags.String("file", "", "path to a FIRMS CSV file")
	product := flags.String("product", demoProduct, "FIRMS product used for the file")
	demo := flags.Bool("demo", false, "load the demo seed and shift the demo fixture times")
	if err := flags.Parse(args[1:]); err != nil {
		return fmt.Errorf("read ingest flags: %w", err)
	}
	if *file == "" || flags.NArg() != 0 {
		return fmt.Errorf("ingest-firms requires -file and takes no positional arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("configure ingest database: %w", err)
	}
	defer pool.Close()

	source := firms.Source
	if *demo {
		if err := ensureDemoSeed(ctx, pool); err != nil {
			return fmt.Errorf("load the demo seed: %w", err)
		}
		source = demoSource
	}
	now := time.Now()
	counts, err := runIngest(ctx, pool, ingestRun{Source: source, Product: *product}, func(context.Context) ([]firms.Observation, error) {
		return readFixture(*file, *product, *demo, now)
	})
	if err != nil {
		return err
	}
	fmt.Printf("Fetched %d, inserted %d, duplicates %d, matches %d.\n",
		counts.Fetched, counts.Inserted, counts.Duplicates, counts.Matched)
	return nil
}

func readFixture(path, product string, demo bool, now time.Time) ([]firms.Observation, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open FIRMS fixture: %w", err)
	}
	defer file.Close()
	observations, err := firms.Parse(file, product)
	if err != nil {
		return observations, err
	}
	if demo {
		// The fixture encodes offsets from midnight UTC on January 8, 2000.
		// Round to the hour so a repeat load within that hour uses the same keys.
		anchor := time.Date(2000, 1, 8, 0, 0, 0, 0, time.UTC)
		base := now.UTC().Truncate(time.Hour)
		for i := range observations {
			o := &observations[i]
			o.ObservedAt = base.Add(o.ObservedAt.Sub(anchor))
			o.Raw["acq_date"] = o.ObservedAt.Format("2006-01-02")
			o.Raw["acq_time"] = o.ObservedAt.Format("1504")
		}
	}
	return observations, nil
}

func ensureDemoSeed(ctx context.Context, db ingestDB) error {
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('open-fimra:demo-seed'))"); err != nil {
			return fmt.Errorf("lock the demo seed: %w", err)
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM workspaces WHERE slug = 'demo')").Scan(&exists); err != nil {
			return fmt.Errorf("look up the demo workspace: %w", err)
		}
		if !exists {
			if err := execSeed(ctx, tx, "load.sql"); err != nil {
				return err
			}
		}
		return execSeed(ctx, tx, "roles.sql")
	})
}

func execSeed(ctx context.Context, tx pgx.Tx, file string) error {
	body, err := os.ReadFile(filepath.Join("..", "..", "seed", "demo-state", file))
	if err != nil {
		return fmt.Errorf("read demo seed %s: %w", file, err)
	}
	if _, err := tx.Exec(ctx, string(body)); err != nil {
		return fmt.Errorf("run demo seed %s: %w", file, err)
	}
	return nil
}
