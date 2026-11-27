package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/cdse"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

const (
	cdseOverlap       = 30 * time.Minute
	cdseInitialWindow = 24 * time.Hour
	cdseArchive       = 30 * 24 * time.Hour
)

type cdsePollArgs struct{}

func (cdsePollArgs) Kind() string { return "cdse_poll" }

type cdsePollWorker struct {
	river.WorkerDefaults[cdsePollArgs]
	pool   *pgxpool.Pool
	client *cdse.Client
}

func (*cdsePollWorker) Timeout(*river.Job[cdsePollArgs]) time.Duration { return 8 * time.Minute }

func (w *cdsePollWorker) Work(ctx context.Context, _ *river.Job[cdsePollArgs]) error {
	return pollCDSE(ctx, w.pool, w.client, time.Now().UTC())
}

func addCDSEJobs(pool *pgxpool.Pool, workers *river.Workers, queues map[string]river.QueueConfig, periodic []*river.PeriodicJob) ([]*river.PeriodicJob, error) {
	enabled, err := strconv.ParseBool(envOr("FIMRA_CDSE_ENABLED", "false"))
	if err != nil {
		return nil, errors.New("FIMRA_CDSE_ENABLED must be a boolean")
	}
	if !enabled {
		return periodic, nil
	}
	interval, err := time.ParseDuration(envOr("FIMRA_CDSE_POLL_INTERVAL", "10m"))
	if err != nil || interval < time.Minute || interval > 30*time.Minute {
		return nil, errors.New("FIMRA_CDSE_POLL_INTERVAL must be a duration from 1m to 30m")
	}
	accessKey, err := cdseCredential("FIMRA_CDSE_S3_ACCESS_KEY")
	if err != nil {
		return nil, err
	}
	secretKey, err := cdseCredential("FIMRA_CDSE_S3_SECRET_KEY")
	if err != nil {
		return nil, err
	}
	client := cdse.NewClient(&http.Client{Timeout: 25 * time.Second}, cdse.Catalogue, cdse.ObjectStore, func(req *http.Request) {
		signS3(req, emptyPayloadHash, accessKey, secretKey, "default", time.Now())
	})
	const queue = "cdse"
	queues[queue] = river.QueueConfig{MaxWorkers: 1}
	river.AddWorker(workers, &cdsePollWorker{pool: pool, client: client})
	return append(periodic, river.NewPeriodicJob(
		river.PeriodicInterval(interval),
		func() (river.JobArgs, *river.InsertOpts) {
			return cdsePollArgs{}, &river.InsertOpts{
				Queue: queue, MaxAttempts: 1,
				UniqueOpts: river.UniqueOpts{ByQueue: true, ByState: []rivertype.JobState{
					rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
					rivertype.JobStateRetryable, rivertype.JobStateScheduled,
				}},
			}
		}, &river.PeriodicJobOpts{RunOnStart: true},
	)), nil
}

func cdseCredential(name string) (string, error) {
	value, file := os.Getenv(name), os.Getenv(name+"_FILE")
	if value != "" && file != "" {
		return "", fmt.Errorf("set only one of %s and %s_FILE", name, name)
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("could not read %s_FILE", name)
		}
		value = strings.TrimSpace(string(data))
	}
	if value == "" || strings.ContainsAny(value, " \t\r\n\x00") {
		return "", fmt.Errorf("%s or %s_FILE must provide a non-empty key without whitespace", name, name)
	}
	return value, nil
}

type cdseDB interface {
	ingestDB
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func pollCDSE(ctx context.Context, db cdseDB, client *cdse.Client, now time.Time) error {
	var failures []error
	for _, satellite := range cdse.Satellites {
		if err := pollCDSEProduct(ctx, db, client, satellite, now); err != nil {
			failures = append(failures, fmt.Errorf("poll %s: %w", cdse.Product(satellite), err))
		}
	}
	return errors.Join(failures...)
}

func pollCDSEProduct(ctx context.Context, db cdseDB, client *cdse.Client, satellite string, now time.Time) error {
	product := cdse.Product(satellite)
	rows, err := db.Query(ctx, `
		SELECT w.id::text, ST_XMin(w.boundary), ST_YMin(w.boundary), ST_XMax(w.boundary), ST_YMax(w.boundary),
		    GREATEST($3::timestamptz, COALESCE(cursor.window_end - make_interval(secs => $4), first_poll.window_start, $5))
		FROM workspaces w
		LEFT JOIN LATERAL (
		    SELECT r.window_end FROM ingest_runs r
		    WHERE r.workspace_id = w.id AND r.source = 'cdse' AND r.product = $1 AND r.kind = 'poll'
		      AND r.status = 'succeeded' AND r.window_end <= $2
		    ORDER BY r.started_at DESC, r.id DESC LIMIT 1
		) cursor ON true
		LEFT JOIN LATERAL (
		    SELECT r.window_start FROM ingest_runs r
		    WHERE cursor.window_end IS NULL AND r.workspace_id = w.id AND r.source = 'cdse'
		      AND r.product = $1 AND r.kind = 'poll' AND r.window_end <= $2
		    ORDER BY r.started_at, r.id LIMIT 1
		) first_poll ON true
		WHERE w.boundary IS NOT NULL AND NOT ST_IsEmpty(w.boundary)
		ORDER BY w.slug`, product, now, now.Add(-cdseArchive), cdseOverlap.Seconds(), now.Add(-cdseInitialWindow))
	if err != nil {
		return fmt.Errorf("read CDSE workspace areas: %w", err)
	}
	type areaWindow struct {
		workspaceArea
		start time.Time
	}
	areas, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (areaWindow, error) {
		var a areaWindow
		err := row.Scan(&a.id, &a.box.West, &a.box.South, &a.box.East, &a.box.North, &a.start)
		return a, err
	})
	if err != nil {
		return fmt.Errorf("collect CDSE workspace areas: %w", err)
	}
	if len(areas) == 0 {
		return nil
	}
	boxes := make([]firms.Bounds, 0, len(areas))
	starts := make(map[string]time.Time, len(areas))
	start := now
	for _, area := range areas {
		boxes = append(boxes, area.box)
		starts[area.id] = area.start
		if area.start.Before(start) {
			start = area.start
		}
	}

	// Fetch once for all workspaces. Record catalogue failures for every
	// workspace and object failures only for those covered by the granule.
	var observations map[string][]firms.Observation
	var areaErrors map[string]error
	var fetchErr error
	var failures []error
	for _, area := range areas {
		_, err := runIngest(ctx, db, ingestRun{
			WorkspaceID: &area.id, Source: cdse.Source, Product: product,
			WindowStart: area.start, WindowEnd: now, AllowPartial: true,
		}, func(ctx context.Context) ([]firms.Observation, error) {
			if observations == nil {
				observations, areaErrors, fetchErr = fetchCDSE(ctx, db, client, satellite, start, now, boxes, starts)
			}
			return observations[area.id], errors.Join(fetchErr, areaErrors[area.id])
		})
		if err != nil {
			failures = append(failures, fmt.Errorf("workspace %s: %w", area.id, err))
		}
	}
	return errors.Join(failures...)
}

func fetchCDSE(ctx context.Context, db cdseDB, client *cdse.Client, satellite string, start, end time.Time, boxes []firms.Bounds, starts map[string]time.Time) (map[string][]firms.Observation, map[string]error, error) {
	observations := make(map[string][]firms.Observation)
	areaErrors := make(map[string]error)
	granules, err := client.Search(ctx, satellite, start, end, boxes)
	for _, g := range granules {
		if len(g.GeoFootprint) == 0 || string(g.GeoFootprint) == "null" {
			err = errors.Join(err, errors.New("CDSE catalogue omitted a granule footprint"))
			continue
		}
		var ids []string
		// Refine the catalogue's coarse bounding-box union against the
		// actual workspace polygons before making any authenticated GETs.
		if footprintErr := db.QueryRow(ctx, `
			SELECT ARRAY(SELECT w.id::text FROM workspaces w
			    WHERE ST_Intersects(w.boundary, ST_SetSRID(ST_GeomFromGeoJSON($1::jsonb), 4326)))`,
			g.GeoFootprint).Scan(&ids); footprintErr != nil {
			err = errors.Join(err, fmt.Errorf("read CDSE workspace intersections: %w", footprintErr))
			continue
		}
		ids = slices.DeleteFunc(ids, func(id string) bool {
			areaStart, ok := starts[id]
			return !ok || g.PublicationDate.Before(areaStart)
		})
		if len(ids) == 0 {
			continue
		}
		records, downloadErr := client.Download(ctx, g)
		if downloadErr == nil {
			downloadErr = distributeCDSE(ctx, db, ids, records, observations)
		}
		if downloadErr != nil {
			for _, id := range ids {
				areaErrors[id] = errors.Join(areaErrors[id], fmt.Errorf("%s: %w", g.Name, downloadErr))
			}
		}
		if ctx.Err() != nil {
			return observations, areaErrors, ctx.Err()
		}
	}
	return observations, areaErrors, err
}

func distributeCDSE(ctx context.Context, db cdseDB, ids []string, records []firms.Observation, observations map[string][]firms.Observation) error {
	if len(records) == 0 {
		return nil
	}
	type point struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	}
	points := make([]point, len(records))
	for i, o := range records {
		points[i] = point{o.Latitude, o.Longitude}
	}
	batch, err := json.Marshal(points)
	if err != nil {
		return fmt.Errorf("encode CDSE locations: %w", err)
	}
	rows, err := db.Query(ctx, `
		SELECT w.id::text, p.ordinality
		FROM ROWS FROM (jsonb_to_recordset($1::jsonb) AS (latitude float8, longitude float8))
		     WITH ORDINALITY AS p
		JOIN workspaces w ON w.id::text = ANY($2::text[])
		WHERE detection_in_workspace(w, ST_SetSRID(ST_Point(p.longitude, p.latitude), 4326))`, batch, ids)
	if err != nil {
		return fmt.Errorf("classify CDSE workspace locations: %w", err)
	}
	var id string
	var ordinal int
	_, err = pgx.ForEachRow(rows, []any{&id, &ordinal}, func() error {
		observations[id] = append(observations[id], records[ordinal-1])
		return nil
	})
	if err != nil {
		return fmt.Errorf("collect CDSE workspace locations: %w", err)
	}
	return nil
}
