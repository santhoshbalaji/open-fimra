package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

const (
	demoSource  = "demo"
	demoProduct = "VIIRS_SNPP_NRT"
)

func runCommand(ctx context.Context, cfg config, args []string) error {
	switch args[0] {
	case "ingest-firms":
		return ingestCommand(ctx, cfg, args)
	case "backfill-firms":
		return backfillCommand(ctx, cfg, args)
	case "feed-health":
		return feedHealthCommand(ctx, cfg, args)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func ingestCommand(ctx context.Context, cfg config, args []string) error {
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

func backfillCommand(ctx context.Context, cfg config, args []string) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	slug := flags.String("workspace", "demo", "slug of the workspace to backfill")
	first := flags.String("from", "", "first UTC day, as YYYY-MM-DD")
	last := flags.String("to", "", "last UTC day, as YYYY-MM-DD, included")
	products := flags.String("products", "VIIRS_SNPP_SP,MODIS_SP", "comma-separated FIRMS standard-processing products")
	fixtures := flags.String("fixtures", "", "read FIRMS files from this folder instead of calling FIRMS")
	demo := flags.Bool("demo", false, "load the demo seed first")
	enqueue := flags.Bool("enqueue", false, "queue a backfill job for the running service instead of running it here")
	if err := flags.Parse(args[1:]); err != nil {
		return fmt.Errorf("read backfill flags: %w", err)
	}
	if flags.NArg() != 0 {
		return errors.New("backfill-firms takes no positional arguments")
	}
	job := firmsBackfillArgs{First: *first, Last: *last}
	for _, p := range strings.Split(*products, ",") {
		if p = strings.TrimSpace(p); p != "" {
			if !firms.IsStandard(p) {
				return fmt.Errorf("%s is not a FIRMS standard-processing product", p)
			}
			job.Products = append(job.Products, p)
		}
	}
	if len(job.Products) == 0 {
		return errors.New("backfill-firms needs at least one product")
	}
	if _, err := job.request(); err != nil {
		return fmt.Errorf("backfill-firms needs -from and -to as YYYY-MM-DD: %w", err)
	}
	if *enqueue && *fixtures != "" {
		return errors.New("a queued backfill always calls FIRMS, so -enqueue cannot use -fixtures")
	}

	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("configure backfill database: %w", err)
	}
	defer pool.Close()
	if *demo {
		if err := ensureDemoSeed(ctx, pool); err != nil {
			return fmt.Errorf("load the demo seed: %w", err)
		}
	}
	if err := pool.QueryRow(ctx, "SELECT id::text FROM workspaces WHERE slug = $1", *slug).Scan(&job.WorkspaceID); err != nil {
		return fmt.Errorf("find workspace %q: %w", *slug, err)
	}

	if *enqueue {
		client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{})
		if err != nil {
			return fmt.Errorf("configure the job queue: %w", err)
		}
		inserted, err := client.Insert(ctx, job, nil)
		if err != nil {
			return fmt.Errorf("queue the backfill: %w", err)
		}
		if inserted.UniqueSkippedAsDuplicate {
			fmt.Printf("The same backfill is already queued as job %d.\n", inserted.Job.ID)
		} else {
			fmt.Printf("Queued backfill job %d. The service runs it when it has a FIRMS map key.\n", inserted.Job.ID)
		}
		return nil
	}

	fetch, available := fixtureRange(*fixtures), availableDays(nil)
	if *fixtures == "" {
		if cfg.firmsMapKey == "" {
			return errors.New("set FIMRA_FIRMS_MAP_KEY, or use -fixtures to read files")
		}
		area := firms.NewArea(cfg.firmsMapKey, nil)
		fetch, available = area.FetchRange, area.Availability
	}
	req, _ := job.request()
	if *fixtures != "" {
		req.Source = demoSource
	}
	result, err := backfillFIRMS(ctx, pool, req, fetch, available,
		func(product string, chunk firms.Chunk, counts ingestCounts, err error) {
			outcome := fmt.Sprintf("fetched %d, inserted %d, linked %d", counts.Fetched, counts.Inserted, counts.Linked)
			if err != nil {
				outcome = "failed: " + err.Error()
			}
			span := fmt.Sprintf("%d days", chunk.Days)
			if chunk.Days == 1 {
				span = "1 day"
			}
			fmt.Printf("%s from %s, %s: %s\n", product, chunk.Start.Format(time.DateOnly), span, outcome)
		})
	fmt.Printf("Ran %d chunks (%d failed) and skipped %d finished days and %d days FIRMS does not have yet. "+
		"Fetched %d, inserted %d, duplicates %d, linked %d, matches %d.\n",
		result.Chunks, result.Failed, result.SkippedDays, result.UnavailableDays, result.Counts.Fetched,
		result.Counts.Inserted, result.Counts.Duplicates, result.Counts.Linked, result.Counts.Matched)
	return err
}

func feedHealthCommand(ctx context.Context, cfg config, args []string) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return errors.Join(errors.New("feed-health takes no arguments"), err)
	}
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("configure feed health database: %w", err)
	}
	defer pool.Close()
	now := time.Now()
	result, err := checkFeedHealth(ctx, pool, slog.New(slog.NewTextHandler(os.Stderr, nil)), now)
	if err != nil {
		return err
	}
	facts, err := loadFeedFacts(ctx, pool, nil, now)
	if err != nil {
		return err
	}
	for _, f := range facts {
		settings, _ := readFeedSettings(f.Settings)
		latest := "none"
		if f.LatestObservedAt != nil {
			latest = f.LatestObservedAt.UTC().Format(time.RFC3339)
		}
		fmt.Printf("%s %s %s: %s, latest observation %s, %d failures in 24 hours\n", f.WorkspaceSlug, f.Source,
			f.Product, feedStatus(f, settings.Thresholds, now), latest, f.FailuresLast24h)
	}
	fmt.Printf("Checked %d feeds, opened %d alerts and resolved %d.\n", result.Feeds, result.Opened, result.Resolved)
	return nil
}
