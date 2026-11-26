package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// TestExportIntegration needs the migrated development stack, Keycloak and
// the geo worker. Downloads go to FIMRA_EXPORT_CHECK_DIR when it is set, so
// that make export-test-integration can read them back with OGR.
func TestExportIntegration(t *testing.T) {
	if os.Getenv("FIMRA_EXPORT_INTEGRATION") != "1" {
		t.Skip("Run make export-test-integration with the worker and the migrated development stack running.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	cfg := loadConfig()
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := ensureDemoSeed(ctx, pool); err != nil {
		t.Fatalf("load the demo seed: %v", err)
	}
	// The worker must see committed rows, so this test uses the demo workspace
	// and the same synthetic detections as make demo-detections. Within one
	// hour a repeat run finds the same detections.
	counts, err := runIngest(ctx, pool, ingestRun{Source: demoSource, Product: demoProduct}, func(context.Context) ([]firms.Observation, error) {
		return readFixture(filepath.Join(fixtureDir, "demo.csv"), demoProduct, true, time.Now())
	})
	if err != nil || counts.Fetched != 11 {
		t.Fatalf("load demo detections: %+v, %v", counts, err)
	}

	provider, err := discoverProvider(ctx, cfg.issuer)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandler(pool, newAuthenticator(provider, cfg.issuer, cfg.audience),
		slog.Default(), devOrigins, t.TempDir(), nil, withExports(cfg.exportDir, time.Hour)))
	t.Cleanup(server.Close)
	checkDir := os.Getenv("FIMRA_EXPORT_CHECK_DIR")
	if checkDir != "" {
		// Keep only this run's downloads for the read-back step.
		old, err := filepath.Glob(filepath.Join(checkDir, "open-fimra-*"))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range old {
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	client := &http.Client{Timeout: 20 * time.Second}
	tokens := map[string]string{}
	for _, user := range []string{"state.admin", "division.officer", "beat.guard", "no.role"} {
		tokens[user] = demoToken(t, client, provider.Endpoint().TokenURL, user)
	}
	const base = "/api/v1/workspaces/demo"
	// Expire this run's exports at the end, so their files do not wait an hour.
	var created []string
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := pool.Exec(cleanup, "UPDATE exports SET expires_at = now() WHERE id = ANY($1::uuid[])", created); err != nil {
			t.Error(err)
		}
		if _, err := expireExports(cleanup, pool, cfg.exportDir); err != nil {
			t.Error(err)
		}
	})

	call := func(method, path, user string, body any) (int, []byte, http.Header) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(data)
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, data, res.Header
	}
	request := func(user string, body map[string]any) exportStatus {
		t.Helper()
		code, data, header := call(http.MethodPost, base+"/exports", user, body)
		var status exportStatus
		if code != http.StatusAccepted || json.Unmarshal(data, &status) != nil ||
			header.Get("Location") != base+"/exports/"+status.ID {
			t.Fatalf("%s export %v: HTTP %d %s", user, body, code, data)
		}
		created = append(created, status.ID)
		// The rows are saved before the request returns.
		var frozen int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM export_rows WHERE export_id = $1", status.ID).Scan(&frozen); err != nil ||
			frozen != status.RowCount {
			t.Fatalf("export %s froze %d rows, reported %d: %v", status.ID, frozen, status.RowCount, err)
		}
		for status.Status == "pending" {
			select {
			case <-ctx.Done():
				t.Fatalf("timed out waiting for export %s", status.ID)
			case <-time.After(100 * time.Millisecond):
			}
			code, data, _ := call(http.MethodGet, base+"/exports/"+status.ID, user, nil)
			if code != http.StatusOK || json.Unmarshal(data, &status) != nil {
				t.Fatalf("export status: HTTP %d %s", code, data)
			}
		}
		if status.Status != "ready" || status.FeatureCount == nil || *status.FeatureCount != status.RowCount || status.FileName == "" {
			t.Fatalf("export finished as %+v", status)
		}
		return status
	}
	download := func(user string, status exportStatus) []byte {
		t.Helper()
		code, data, header := call(http.MethodGet, base+"/exports/"+status.ID+"/file", user, nil)
		if code != http.StatusOK || header.Get("Content-Type") != exportFormats[status.Format].mediaType ||
			header.Get("Content-Disposition") != `attachment; filename="`+status.FileName+`"` ||
			status.FileBytes == nil || int64(len(data)) != *status.FileBytes {
			t.Fatalf("download %s: HTTP %d, headers %v, %d bytes", status.ID, code, header, len(data))
		}
		if checkDir != "" {
			if err := os.WriteFile(filepath.Join(checkDir, status.FileName), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return data
	}

	// What each user sees on the alert queue and on a tile of the whole world.
	alertIDs := func(user, query string) []int64 {
		t.Helper()
		var ids []int64
		cursor := ""
		for {
			q := query + "&limit=200"
			if cursor != "" {
				q += "&cursor=" + url.QueryEscape(cursor)
			}
			code, data, _ := call(http.MethodGet, base+"/alerts?"+q, user, nil)
			var page struct {
				Counts     map[string]int `json:"counts"`
				NextCursor string         `json:"nextCursor"`
				Items      []struct {
					ID string `json:"id"`
				} `json:"items"`
			}
			if code != http.StatusOK || json.Unmarshal(data, &page) != nil {
				t.Fatalf("%s alerts %s: HTTP %d %s", user, q, code, data)
			}
			for _, item := range page.Items {
				id, _ := strconv.ParseInt(item.ID, 10, 64)
				ids = append(ids, id)
			}
			if page.NextCursor == "" {
				total := 0
				for _, n := range page.Counts {
					total += n
				}
				if query == "hours=168" && total != len(ids) {
					t.Fatalf("%s queue counts %v disagree with %d alerts", user, page.Counts, len(ids))
				}
				slices.Sort(ids)
				return ids
			}
			cursor = page.NextCursor
		}
	}
	tileIDs := func(user, classes string) []int64 {
		t.Helper()
		code, data, _ := call(http.MethodGet, base+"/tiles/detections/0/0/0.mvt?hours=168&classes="+classes, user, nil)
		if code == http.StatusNoContent {
			return nil
		}
		if code != http.StatusOK {
			t.Fatalf("%s tile: HTTP %d", user, code)
		}
		var ids []int64
		for _, feature := range tileFeatures(t, data) {
			ids = append(ids, int64(feature["id"].(uint64)))
		}
		slices.Sort(ids)
		return ids
	}
	geojsonIDs := func(data []byte, field string) []int64 {
		t.Helper()
		var collection struct {
			Features []struct {
				Properties map[string]any `json:"properties"`
			} `json:"features"`
		}
		if err := json.Unmarshal(unzipMember(t, data, ".geojson"), &collection); err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, feature := range collection.Features {
			ids = append(ids, int64(feature.Properties[field].(float64)))
		}
		slices.Sort(ids)
		return ids
	}

	const allClasses = "inside,near_edge,outside,fixed_heat_source"
	exports := map[string]exportStatus{}
	for _, tc := range []struct{ user, subject string }{
		{"state.admin", "a1"}, {"division.officer", "a2"}, {"beat.guard", "a3"},
	} {
		t.Run(tc.user, func(t *testing.T) {
			wantAlerts := alertIDs(tc.user, "hours=168")
			alerts := request(tc.user, map[string]any{"kind": "alerts", "format": "geojson", "hours": 168})
			if got := geojsonIDs(download(tc.user, alerts), "alert_id"); !slices.Equal(got, wantAlerts) {
				t.Fatalf("alert export has %v, the queue shows %v", got, wantAlerts)
			}
			for _, classes := range []string{allClasses, "inside,near_edge,fixed_heat_source"} {
				wantDetections := tileIDs(tc.user, classes)
				detections := request(tc.user, map[string]any{"kind": "detections", "format": "geojson",
					"hours": 168, "forestClasses": splitClasses(classes)})
				if got := geojsonIDs(download(tc.user, detections), "detection_id"); !slices.Equal(got, wantDetections) {
					t.Fatalf("detection export with %s has %v, the tiles show %v", classes, got, wantDetections)
				}
				t.Logf("%s: %d alerts, %d detections (%s) match the queue and tiles", tc.user, len(wantAlerts), len(wantDetections), classes)
			}
			if tc.subject == "a3" && len(wantAlerts) != 0 {
				t.Fatalf("beat guard sees %d alerts", len(wantAlerts))
			}
			exports[tc.user] = alerts
		})
	}
	if t.Failed() {
		return
	}

	t.Run("recent jobs are private to the requester", func(t *testing.T) {
		for user, job := range exports {
			code, data, headers := call(http.MethodGet, base+"/exports", user, nil)
			var page struct{ Items []exportStatus }
			if code != http.StatusOK || headers.Get("Cache-Control") != "private, no-store" ||
				json.Unmarshal(data, &page) != nil || len(page.Items) > 20 {
				t.Fatalf("export history for %s: HTTP %d %s", user, code, data)
			}
			if !slices.ContainsFunc(page.Items, func(item exportStatus) bool {
				return item.ID == job.ID && item.Status == "ready" && item.FileName == job.FileName
			}) {
				t.Errorf("%s cannot reopen their ready export", user)
			}
			for other, otherJob := range exports {
				if other != user && slices.ContainsFunc(page.Items, func(item exportStatus) bool { return item.ID == otherJob.ID }) {
					t.Errorf("%s can see an export requested by %s", user, other)
				}
			}
		}
	})

	t.Run("filters match the alert queue", func(t *testing.T) {
		want := alertIDs("state.admin", "hours=24&queue=unit&status=new&class=inside&unitPath=ds.c1.d1")
		status := request("state.admin", map[string]any{"kind": "alerts", "format": "geojson", "hours": 24,
			"queue": "unit", "step": "new", "forestClasses": []string{"inside"}, "unitPath": "ds.c1.d1"})
		if got := geojsonIDs(download("state.admin", status), "alert_id"); !slices.Equal(got, want) || len(want) == 0 {
			t.Fatalf("filtered export has %v, the queue shows %v", got, want)
		}
	})

	t.Run("every format", func(t *testing.T) {
		for format := range exportFormats {
			for _, kind := range []string{"alerts", "detections"} {
				body := map[string]any{"kind": kind, "format": format, "hours": 168}
				if kind == "detections" {
					body["forestClasses"] = splitClasses(allClasses)
				}
				status := request("state.admin", body)
				if len(download("state.admin", status)) == 0 {
					t.Fatalf("%s %s export is empty", kind, format)
				}
			}
		}
	})

	t.Run("refused requests", func(t *testing.T) {
		admin := exports["state.admin"]
		for _, tc := range []struct {
			method, path, user string
			body               any
			want               int
		}{
			{http.MethodPost, base + "/exports", "no.role", map[string]any{"kind": "alerts", "format": "gpkg"}, http.StatusForbidden},
			{http.MethodGet, base + "/exports", "no.role", nil, http.StatusForbidden},
			{http.MethodGet, base + "/exports/" + admin.ID, "no.role", nil, http.StatusForbidden},
			{http.MethodGet, base + "/exports/" + admin.ID, "division.officer", nil, http.StatusNotFound},
			{http.MethodGet, base + "/exports/" + admin.ID + "/file", "division.officer", nil, http.StatusNotFound},
			{http.MethodGet, base + "/exports/" + admin.ID + "/file", "beat.guard", nil, http.StatusNotFound},
			{http.MethodGet, base + "/exports/" + admin.ID + "/file", "no.role", nil, http.StatusForbidden},
			{http.MethodPost, base + "/exports", "state.admin", map[string]any{"kind": "detections", "format": "gpkg", "queue": "unit"}, http.StatusBadRequest},
			{http.MethodPost, base + "/exports", "state.admin", map[string]any{"kind": "alerts", "format": "pdf"}, http.StatusBadRequest},
			{http.MethodPost, base + "/exports", "state.admin", map[string]any{"kind": "alerts", "format": "gpkg", "step": "unknown_step"}, http.StatusBadRequest},
			{http.MethodPost, base + "/exports", "state.admin", map[string]any{"kind": "alerts", "format": "gpkg", "hours": 721}, http.StatusBadRequest},
		} {
			if code, data, _ := call(tc.method, tc.path, tc.user, tc.body); code != tc.want {
				t.Errorf("%s %s as %s: HTTP %d %s, want %d", tc.method, tc.path, tc.user, code, data, tc.want)
			}
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+base+"/exports/"+admin.ID+"/file", nil)
		if res, err := client.Do(req); err != nil || res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous download: %v %v", res, err)
		} else {
			res.Body.Close()
		}
	})

	t.Run("audit", func(t *testing.T) {
		for user, status := range exports {
			var created, downloaded int
			var rowCount int
			var hours float64
			err := pool.QueryRow(ctx, `
				SELECT count(*) FILTER (WHERE action = 'export.create'),
				       count(*) FILTER (WHERE action = 'export.download'),
				       max((details->>'rowCount')::int) FILTER (WHERE action = 'export.create'),
				       max((details #>> '{filters,hours}')::float8) FILTER (WHERE action = 'export.create')
				FROM audit_log WHERE target_type = 'export' AND target_id = $1 AND actor_kind = 'user' AND actor_id = $2
			`, status.ID, seedPrefix+map[string]string{"state.admin": "a1", "division.officer": "a2", "beat.guard": "a3"}[user]).
				Scan(&created, &downloaded, &rowCount, &hours)
			if err != nil || created != 1 || downloaded != 1 || rowCount != status.RowCount || hours != 168 {
				t.Fatalf("%s audit: %d created, %d downloads, %d rows, %v hours: %v", user, created, downloaded, rowCount, hours, err)
			}
		}
	})

	t.Run("expiry", func(t *testing.T) {
		status := exports["division.officer"]
		var key string
		if err := pool.QueryRow(ctx, "SELECT file_key FROM exports WHERE id = $1", status.ID).Scan(&key); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(cfg.exportDir, key)
		if _, err := os.Stat(file); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "UPDATE exports SET expires_at = now() WHERE id = $1", status.ID); err != nil {
			t.Fatal(err)
		}
		if code, _, _ := call(http.MethodGet, base+"/exports/"+status.ID+"/file", "division.officer", nil); code != http.StatusGone {
			t.Fatalf("download after expiry: HTTP %d", code)
		}
		removed, err := expireExports(ctx, pool, cfg.exportDir)
		if err != nil || removed < 1 {
			t.Fatalf("cleanup removed %d: %v", removed, err)
		}
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("expired file still exists: %v", err)
		}
		var state string
		var rows int
		if err := pool.QueryRow(ctx, `SELECT status, (SELECT count(*) FROM export_rows WHERE export_id = e.id)
			FROM exports e WHERE id = $1`, status.ID).Scan(&state, &rows); err != nil || state != "expired" || rows != 0 {
			t.Fatalf("expired export is %s with %d rows: %v", state, rows, err)
		}
		if code, _, _ := call(http.MethodGet, base+"/exports/"+status.ID+"/file", "division.officer", nil); code != http.StatusGone {
			t.Fatalf("download after cleanup: HTTP %d", code)
		}
		t.Logf("cleanup expired %d exports and removed %s", removed, key)
	})
}

func splitClasses(classes string) []string {
	return strings.Split(classes, ",")
}

func unzipMember(t *testing.T, data []byte, suffix string) []byte {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range archive.File {
		if filepath.Ext(file.Name) == suffix {
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			body, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			return body
		}
	}
	t.Fatalf("no %s file in the archive", suffix)
	return nil
}
