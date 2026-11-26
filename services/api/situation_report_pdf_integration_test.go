package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// TestSituationReportIntegration needs the migrated development stack,
// Keycloak and the geo worker. It makes a PDF as each demo user and checks
// that its saved numbers match the dashboard. The PDFs and the text each must
// contain go to FIMRA_REPORT_CHECK_DIR, where make
// situation-report-test-integration reads them back with pypdf.
func TestSituationReportIntegration(t *testing.T) {
	if os.Getenv("FIMRA_REPORT_INTEGRATION") != "1" {
		t.Skip("Run make situation-report-test-integration with the worker and the migrated development stack running.")
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
	// and the same synthetic detections as make demo-detections.
	if _, err := runIngest(ctx, pool, ingestRun{Source: demoSource, Product: demoProduct}, func(context.Context) ([]firms.Observation, error) {
		return readFixture(filepath.Join(fixtureDir, "demo.csv"), demoProduct, true, time.Now())
	}); err != nil {
		t.Fatalf("load demo detections: %v", err)
	}
	provider, err := discoverProvider(ctx, cfg.issuer)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandler(pool, newAuthenticator(provider, cfg.issuer, cfg.audience),
		slog.Default(), devOrigins, t.TempDir(), nil, withExports(cfg.exportDir, time.Hour)))
	t.Cleanup(server.Close)
	checkDir := os.Getenv("FIMRA_REPORT_CHECK_DIR")
	if checkDir != "" {
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
	var created []string
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if _, err := pool.Exec(cleanup, "UPDATE situation_reports SET expires_at = now() WHERE id = ANY($1::uuid[])", created); err != nil {
			t.Error(err)
		}
		if _, err := expireSituationReports(cleanup, pool); err != nil {
			t.Error(err)
		}
	})
	const base = "/api/v1/workspaces/demo"
	call := func(method, path, user, body string) (int, []byte, http.Header) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tokens[user])
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

	for _, user := range []string{"state.admin", "division.officer", "beat.guard"} {
		t.Run(user, func(t *testing.T) {
			code, data, _ := call("GET", base+"/summary?hours=168", user, "")
			var dashboard workspaceSummary
			if code != 200 || json.Unmarshal(data, &dashboard) != nil {
				t.Fatalf("summary: %d %s", code, data)
			}
			code, dataView, _ := call("GET", base+"/situation-report?hours=168&snapshot=1", user, "")
			var viewed situationReport
			if code != 200 || json.Unmarshal(dataView, &viewed) != nil || viewed.SnapshotID == "" {
				t.Fatalf("view: %d %s", code, dataView)
			}
			created = append(created, viewed.SnapshotID)
			body := fmt.Sprintf(`{"snapshotId":%q}`, viewed.SnapshotID)
			if user == "state.admin" {
				if code, _, _ := call("POST", base+"/situation-reports", "division.officer", body); code != 404 {
					t.Fatalf("another user's snapshot: %d", code)
				}
			}
			var draft string
			if err := pool.QueryRow(ctx, "SELECT status FROM situation_reports WHERE id=$1", viewed.SnapshotID).Scan(&draft); err != nil || draft != "draft" {
				t.Fatalf("draft: %q, %v", draft, err)
			}
			code, data, header := call("POST", base+"/situation-reports", user, body)
			var status reportStatus
			if code != http.StatusAccepted || json.Unmarshal(data, &status) != nil {
				t.Fatalf("create: %d %s", code, data)
			}
			if status.ID != viewed.SnapshotID || header.Get("Location") != base+"/situation-reports/"+status.ID {
				t.Fatalf("location %q", header.Get("Location"))
			}
			var content []byte
			if err := pool.QueryRow(ctx, "SELECT content FROM situation_reports WHERE id=$1", status.ID).Scan(&content); err != nil {
				t.Fatal(err)
			}
			var saved situationReport
			if err := json.Unmarshal(content, &saved); err != nil {
				t.Fatal(err)
			}
			var viewedJSON, savedJSON map[string]any
			if err := json.Unmarshal(dataView, &viewedJSON); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(content, &savedJSON); err != nil {
				t.Fatal(err)
			}
			delete(viewedJSON, "snapshotId")
			if !reflect.DeepEqual(savedJSON, viewedJSON) {
				t.Fatal("the PDF did not use the report shown on screen")
			}
			// The demo feed is fixed, so the dashboard counts agree exactly.
			if !reflect.DeepEqual(saved.Counts.Detections, dashboard.Detections) || saved.Counts.Active != dashboard.Active ||
				saved.Counts.Overdue != dashboard.Overdue || !reflect.DeepEqual(saved.Counts.Queues, dashboard.Queues) ||
				!reflect.DeepEqual(saved.Counts.Steps, dashboard.Steps) || saved.Counts.PendingReports != dashboard.PendingReports {
				t.Fatalf("saved report %+v, dashboard %+v", saved.Counts, dashboard.summaryCounts)
			}
			for status.Status == "pending" {
				select {
				case <-ctx.Done():
					t.Fatalf("timed out waiting for report %s; is the geo worker running?", status.ID)
				case <-time.After(100 * time.Millisecond):
				}
				code, data, _ := call("GET", base+"/situation-reports/"+status.ID, user, "")
				if code != 200 || json.Unmarshal(data, &status) != nil {
					t.Fatalf("status: %d %s", code, data)
				}
			}
			if status.Status != "ready" || status.PageCount == nil || *status.PageCount < 1 || status.FileName == "" {
				t.Fatalf("report finished as %+v", status)
			}
			code, pdf, header := call("GET", base+"/situation-reports/"+status.ID+"/file", user, "")
			if code != 200 || header.Get("Content-Type") != "application/pdf" || !bytes.HasPrefix(pdf, []byte("%PDF-")) ||
				status.FileBytes == nil || len(pdf) != *status.FileBytes {
				t.Fatalf("download: %d %v %d bytes", code, header, len(pdf))
			}
			if code, _, _ := call("GET", base+"/situation-reports/"+status.ID+"/file", "no.role", ""); code != 403 {
				t.Fatalf("no.role download: %d", code)
			}
			if checkDir != "" {
				total := saved.Alerts.Total
				alerts := fmt.Sprintf("%d alerts in the period", total)
				if total == 1 {
					alerts = "1 alert in the period"
				}
				expect := map[string]any{"pages": *status.PageCount, "text": []string{
					"Situation report", saved.Scope, alerts,
					fmt.Sprintf("Past their time limit: %d", saved.Counts.Overdue),
					fmt.Sprintf("Waiting for a report: %d", saved.Counts.PendingReports),
					"How the numbers are worked out", "not an official Government of India system",
				}}
				name := filepath.Join(checkDir, "open-fimra-"+strings.ReplaceAll(user, ".", "-"))
				data, _ := json.Marshal(expect)
				if err := os.WriteFile(name+".pdf", pdf, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name+".json", data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("%s: %s, %d alerts, %d pages, %d bytes", user, saved.Scope, saved.Alerts.Total, *status.PageCount, len(pdf))
		})
	}
	if code, _, _ := call("POST", base+"/situation-reports", "no.role", `{"hours":24}`); code != 403 {
		t.Fatalf("no.role create: %d", code)
	}
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE target_id = ANY($1) AND action IN ('report.create','report.download')`,
		created).Scan(&audits); err != nil || audits != 2*len(created) {
		t.Fatalf("audit entries %d for %d reports: %v", audits, len(created), err)
	}
}
