package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

const defaultFIRMSPollInterval = 5 * time.Minute

type firmsPollSettings struct {
	interval time.Duration
	products []string
}

func defaultFIRMSProducts() []string {
	return strings.Split(firms.DefaultPollProducts, ",")
}

func firmsPollProducts(raw string) ([]string, error) {
	if raw == "" {
		return defaultFIRMSProducts(), nil
	}
	allowed := defaultFIRMSProducts()
	var products []string
	for _, value := range strings.Split(raw, ",") {
		product := strings.TrimSpace(value)
		if !slices.Contains(allowed, product) || slices.Contains(products, product) {
			return nil, fmt.Errorf("FIMRA_FIRMS_POLL_PRODUCTS must list distinct products from %s", firms.DefaultPollProducts)
		}
		products = append(products, product)
	}
	return products, nil
}

func readFIRMSPollSettings() (firmsPollSettings, error) {
	interval, err := time.ParseDuration(envOr("FIMRA_FIRMS_POLL_INTERVAL", defaultFIRMSPollInterval.String()))
	if err != nil || interval < time.Minute {
		return firmsPollSettings{}, errors.New("FIMRA_FIRMS_POLL_INTERVAL must be a duration of at least one minute, such as 5m")
	}
	products, err := firmsPollProducts(envOr("FIMRA_FIRMS_POLL_PRODUCTS", ""))
	return firmsPollSettings{interval: interval, products: products}, err
}

type firmsPollArgs struct{}

func (firmsPollArgs) Kind() string { return "firms_poll" }

type firmsPollWorker struct {
	river.WorkerDefaults[firmsPollArgs]
	pool *pgxpool.Pool
	keys *firmsKeys

	mu       sync.Mutex
	previous map[pollFeed]time.Time
}

func (*firmsPollWorker) Timeout(*river.Job[firmsPollArgs]) time.Duration {
	return 14 * time.Minute
}

func (w *firmsPollWorker) Work(ctx context.Context, job *river.Job[firmsPollArgs]) error {
	// A late tick is disposable. A second worker can discard ticks while an
	// earlier poll is still processing its workspaces.
	if time.Since(job.ScheduledAt) >= w.keys.pollSettings.interval || !w.mu.TryLock() {
		return nil
	}
	defer w.mu.Unlock()

	conn, err := w.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if time.Since(job.ScheduledAt) >= w.keys.pollSettings.interval {
		return nil
	}
	var locked bool
	const lock = "hashtext('open-fimra:firms-poll')"
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock("+lock+")").Scan(&locked); err != nil {
		// An interrupted query may have acquired the session lock.
		discardFIRMSPollConn(ctx, conn)
		return err
	}
	if !locked {
		w.previous = nil
		return nil
	}
	defer func() {
		unlock, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlock, "SELECT pg_advisory_unlock("+lock+")"); err != nil {
			discardFIRMSPollConn(ctx, conn)
		}
	}()
	return w.poll(ctx, conn, time.Now())
}

func discardFIRMSPollConn(ctx context.Context, conn *pgxpool.Conn) {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = conn.Hijack().Close(closeCtx)
}

type workspaceArea struct {
	id  string
	box firms.Bounds
}

type pollArea struct {
	key  [32]byte
	box  firms.Bounds
	days int
}

type pollFeed struct {
	pollArea
	workspace, product string
}

type firmsPollDB interface {
	ingestDB
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (w *firmsPollWorker) poll(ctx context.Context, db firmsPollDB, now time.Time) error {
	previous := w.previous
	w.previous = make(map[pollFeed]time.Time)
	rows, err := db.Query(ctx, `
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

	type group struct {
		area       pollArea
		key        string
		keyErr     error
		workspaces []string
	}
	var groups []*group
	byArea := make(map[pollArea]*group)
	for _, area := range areas {
		key, source, err := w.keys.keyFrom(ctx, db, area.id)
		if source == keyMissing {
			continue
		}
		id := pollArea{key: sha256.Sum256([]byte(key)), box: area.box, days: firms.Days}
		if err != nil {
			groups = append(groups, &group{area: id, keyErr: fmt.Errorf("read the FIRMS map key: %w", err), workspaces: []string{area.id}})
			continue
		}
		g := byArea[id]
		if g == nil {
			g = &group{area: id, key: key}
			byArea[id] = g
			groups = append(groups, g)
		}
		g.workspaces = append(g.workspaces, area.id)
	}

	now = now.UTC()
	var failures []error
	for _, group := range groups {
		client := w.keys.client(group.key)
		for _, product := range w.keys.pollSettings.products {
			// Keep one result (including any failure) until every workspace
			// using this request has recorded its own run.
			var observations []firms.Observation
			var fetchedAt time.Time
			readErr := group.keyErr
			fetched := false
			for _, workspace := range group.workspaces {
				if ctx.Err() != nil {
					return errors.Join(append(failures, ctx.Err())...)
				}
				feed := pollFeed{pollArea: group.area, workspace: workspace, product: product}
				run := ingestRun{WorkspaceID: &workspace, Source: firms.Source, Product: product,
					WindowStart: firms.Day(now).AddDate(0, 0, 1-group.area.days), WindowEnd: now}
				_, err := runIngestTimed(ctx, db, run, func(ctx context.Context) ([]firms.Observation, fetchTiming, error) {
					if !fetched && readErr == nil {
						fetched = true
						observations, readErr = client.Fetch(ctx, product, group.area.box)
						if readErr == nil {
							readErr = db.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&fetchedAt)
						}
					}
					timing := fetchTiming{fetchedAt: fetchedAt}
					prior := previous[feed]
					gap, interval := fetchedAt.Sub(prior), w.keys.pollSettings.interval
					// A failed or missed poll, a restart, or a changed request
					// leaves no recent comparable baseline.
					if !prior.IsZero() && gap > 0 && gap-interval <= interval {
						timing.previousFetchedAt = &prior
					}
					return observations, timing, readErr
				})
				if err != nil {
					failures = append(failures, fmt.Errorf("poll %s for workspace %s: %w", product, workspace, err))
					continue
				}
				w.previous[feed] = fetchedAt
			}
		}
	}
	return errors.Join(failures...)
}
