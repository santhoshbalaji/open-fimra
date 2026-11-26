package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The worker reads a committed snapshot, so use a separate fictional workspace.
func TestDailyReturnIntegration(t *testing.T) {
	if os.Getenv("FIMRA_REPORT_INTEGRATION") != "1" {
		t.Skip("Run make situation-report-test-integration.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, loadConfig().databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	prefix := fmt.Sprintf("daily-%016x-", rand.Uint64())
	slug := prefix + "test"
	var workspace, hierarchy string
	err = tx.QueryRow(ctx, `WITH w AS (
    INSERT INTO workspaces(slug,name) VALUES ($1,'Fictional reporting workspace') RETURNING id
), h AS (
    INSERT INTO hierarchies(workspace_id,slug,name,kind)
    SELECT id,'territorial','Territorial','territorial' FROM w RETURNING id,workspace_id
) SELECT workspace_id::text,id::text FROM h`, slug).Scan(&workspace, &hierarchy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO hierarchy_levels(hierarchy_id,depth,slug,labels) VALUES
    ($1,0,'state','{"en":"State"}'),($1,1,'circle','{"en":"Circle"}'),($1,2,'division','{"en":"Division"}')`,
		hierarchy); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO units(hierarchy_id,depth,code,name,path,valid_from)
SELECT $1,depth,code,name,path::ltree,'-infinity'
FROM (VALUES (0,'S','Fictional State','dailyroot'),(1,'C1','West Circle','dailyroot.c1'),(1,'C2','East Circle','dailyroot.c2'),
    (2,'D1','Hill Division','dailyroot.c1.d1'),(2,'D2','Valley Division','dailyroot.c1.d2'),
    (2,'D3','River Division','dailyroot.c2.d3'),(2,'D4','Lake Division','dailyroot.c2.d4')) unit(depth,code,name,path)`, hierarchy); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `WITH actors AS (
    INSERT INTO users(oidc_subject,username,display_name) VALUES ($1,$1,'Demo State Reviewer'),($2,$2,'Demo Division Officer')
    RETURNING id,oidc_subject
) INSERT INTO role_assignments(user_id,workspace_id,hierarchy_id,unit_path,role_slug)
SELECT id,$3,$4,g.path::ltree,g.role FROM actors
JOIN (VALUES ($1,'dailyroot','state_admin'),($2,'dailyroot.c1.d1','division_officer'),($2,'dailyroot.c1.d2','division_officer'))
    g(subject,path,role) ON oidc_subject=g.subject`, prefix+"a1", prefix+"a2", workspace, hierarchy); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)
	observed := day.Add(10 * time.Hour)
	if _, err = tx.Exec(ctx, `SELECT ensure_detection_partition($1::date)`, day.Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	var alert int64
	err = tx.QueryRow(ctx, `WITH detection AS (
    INSERT INTO detections(source,product,observed_at,geom,source_key)
    VALUES('daily_test','synthetic',$1,ST_SetSRID(ST_MakePoint(10,10),4326),$2) RETURNING id,observed_at
), matched AS (
    INSERT INTO detection_matches (workspace_id,detection_id,observed_at,forest_class)
    SELECT $3,id,observed_at,'inside' FROM detection RETURNING detection_id,observed_at
)
INSERT INTO alerts(workspace_id,detection_id,observed_at,queue,hierarchy_id,unit_path,hierarchy,
    hierarchy_unit_paths,unit_paths,forest_class,layer_version_ids)
SELECT $3,detection_id,observed_at,'unit',$4,'dailyroot.c1.d1','{}',
    jsonb_build_object($4::uuid::text,jsonb_build_array('dailyroot.c1.d1')),
    ARRAY['dailyroot.c1.d1']::ltree[],'inside','{}' FROM matched RETURNING id`, observed, slug, workspace, hierarchy).Scan(&alert)
	if err != nil {
		t.Fatal(err)
	}
	// The same alert is also visible to another division through support and its
	// overlay. Its dispatch lead is still the first division.
	if _, err = tx.Exec(ctx, `UPDATE alerts SET dispatch_support=jsonb_build_array(dispatch_unit(u)),
    hierarchy_unit_paths=jsonb_build_object($2::uuid::text,jsonb_build_array('dailyroot.c1.d1','dailyroot.c1.d2')),
    boundary_snapshot=jsonb_build_object('overlays',jsonb_build_array(jsonb_build_object('code','Fictional reserve')))
FROM units u WHERE alerts.id=$1 AND u.hierarchy_id=$2 AND u.current_path='dailyroot.c1.d2'`, alert, hierarchy); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	request := notificationRequests(t, api{pool: pool, reader: pool, logger: slog.Default()}.handler, prefix, "")
	base := "/api/v1/workspaces/" + slug
	call := func(actor, method, path string, input any, want int, out any) []byte {
		t.Helper()
		body := ""
		if input != nil {
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			body = string(raw)
		}
		res := request(actor, method, base+path, body)
		if res.Code != want {
			t.Fatalf("%s %s as %s: HTTP %d, want %d: %s", method, path, actor, res.Code, want, res.Body)
		}
		if out != nil && json.Unmarshal(res.Body.Bytes(), out) != nil {
			t.Fatalf("invalid response from %s: %s", path, res.Body)
		}
		return res.Body.Bytes()
	}

	policy := dailyPolicy{Hierarchy: "territorial", Level: "division", TimeZone: "America/New_York",
		Cutoff: "18:00", From: "2026-03-07", To: "2026-03-09", Approver: "state_admin"}
	call("a2", "POST", "/daily-return-policy", policy, 403, nil)
	call("a1", "POST", "/daily-return-policy", policy, 201, nil)
	path := "/daily-returns?day=2026-03-08"
	first := path + "&unit=dailyroot.c1.d1"
	second := path + "&unit=dailyroot.c1.d2"
	type receipt struct{ ID string }
	type unitView struct{ Snapshot dailySnapshot }
	var earlier, corrected, nilReturn, pdfJob receipt

	call("a2", "POST", first, dailyRequest{Action: "submit", Remarks: "Check the fictional ridge.", Received: 2}, 201, &earlier)
	call("a2", "POST", second, dailyRequest{Action: "submit", Nil: true}, 201, &nilReturn)
	call("a1", "POST", second, dailyRequest{Action: "approved", Base: nilReturn.ID}, 201, nil)
	call("a2", "POST", first, dailyRequest{Action: "approved", Base: earlier.ID}, 403, nil)
	call("a1", "POST", first, dailyRequest{Action: "returned", Base: earlier.ID, Remarks: "Check the assistance count."}, 201, nil)
	call("a2", "POST", first, dailyRequest{Action: "submit", Base: earlier.ID, Remarks: "Assistance tally corrected.", Received: 1}, 201, &corrected)
	call("a1", "POST", first, dailyRequest{Action: "approved", Base: corrected.ID}, 201, nil)

	var previous, shown, zero unitView
	call("a2", "GET", first+"&revision="+earlier.ID, nil, 200, &previous)
	call("a1", "GET", first, nil, 200, &shown)
	call("a1", "GET", second, nil, 200, &zero)
	if !zero.Snapshot.DailyReturn.Nil || len(zero.Snapshot.DailyReturn.Totals) != 0 {
		t.Fatalf("nil figures: %+v", zero.Snapshot)
	}
	current, prior := shown.Snapshot, previous.Snapshot
	if current.Review.Previous == nil || *current.Review.Previous != earlier.ID || prior.Review.Status != "returned" ||
		prior.DailyReturn.Totals["assistance.received"] != 2 || current.DailyReturn.Totals["assistance.received"] != 1 {
		t.Fatalf("history: prior %+v, corrected %+v", prior, current)
	}
	if want := day.Add(22 * time.Hour); !current.DailyReturn.Cutoff.Equal(want) || current.Period.End.Sub(current.Period.Start) != 23*time.Hour {
		t.Fatalf("local cutoff and window: %+v", current.Period)
	}

	var coverage struct {
		Coverage struct {
			Expected, Reported, Missing int
			Daily, Season               map[string]float64
			Units                       []struct {
				Path, Status string
				Nil          bool
			}
		}
	}
	call("a1", "GET", path, nil, 200, &coverage)
	c := coverage.Coverage
	if c.Expected != 4 || c.Reported != 2 || c.Missing != 2 || len(c.Units) != 4 ||
		!c.Units[1].Nil || c.Units[2].Status != "missing" || c.Daily["source.satellite"] != 1 ||
		c.Daily["assistance.received"] != 1 || c.Season["source.satellite"] != 1 {
		t.Fatalf("coverage: %+v", c)
	}
	coverage.Coverage.Daily = nil
	call("a1", "GET", "/daily-returns?day=2026-03-09&scope=dailyroot.c1", nil, 200, &coverage)
	if c := coverage.Coverage; c.Expected != 2 || c.Missing != 2 || c.Daily["source.satellite"] != 0 ||
		c.Season["source.satellite"] != 1 {
		t.Fatalf("season figures leaked into the following day: %+v", c)
	}
	// Independently evaluate the supporting unit's automatic totals. The nil
	// return itself deliberately skipped collection.
	var support []dailyFact
	if err := pool.QueryRow(ctx, dailyReturnFactsSQL, workspace, hierarchy, "dailyroot.c1.d2",
		current.Period.Start, current.Period.End, policy.TimeZone).Scan(&support); err != nil || len(support) != 0 {
		t.Fatalf("supporting division took credit for the lead's alert: %v %v", support, err)
	}
	for range 2 {
		for _, now := range []time.Time{current.DailyReturn.Cutoff.Add(-30 * time.Minute), current.DailyReturn.Cutoff} {
			if _, err := pool.Exec(ctx, dailyReturnNoticesSQL, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	var misses int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications
JOIN daily_return_notices d ON d.id=notifications.daily_notice_id
WHERE workspace_id=$1 AND notifications.event='daily_return_missing' AND d.day='2026-03-08'`, workspace).Scan(&misses); err != nil || misses != 2 {
		t.Fatalf("missed cutoff notices: got %d, want 2: %v", misses, err)
	}

	// A transfer between filings can put the same source record in two saved
	// unit returns. The roll-up still counts the underlying alert only once.
	if _, err := pool.Exec(ctx, `UPDATE alerts SET dispatch_lead=dispatch_unit(u)
FROM units u WHERE alerts.id=$1 AND u.hierarchy_id=$2 AND u.current_path='dailyroot.c1.d2'`, alert, hierarchy); err != nil {
		t.Fatal(err)
	}
	var transferred receipt
	call("a2", "POST", second, dailyRequest{Action: "submit", Base: nilReturn.ID}, 201, &transferred)
	call("a1", "POST", second, dailyRequest{Action: "approved", Base: transferred.ID}, 201, nil)
	call("a1", "GET", path, nil, 200, &coverage)
	if got := coverage.Coverage.Daily["source.satellite"]; got != 1 {
		t.Fatalf("state roll-up counted a transferred alert %v times", got)
	}

	call("a1", "POST", first, dailyRequest{Action: "pdf", Base: corrected.ID}, 201, &pdfJob)
	var saved dailySnapshot
	if err := pool.QueryRow(ctx, "SELECT content FROM situation_reports WHERE id=$1::uuid", pdfJob.ID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current, saved) {
		t.Fatalf("PDF snapshot differs from the screen: %+v versus %+v", saved, current)
	}
	pdfPath := "/situation-reports/" + pdfJob.ID
	var status reportStatus
	for {
		call("a1", "GET", pdfPath, nil, 200, &status)
		if status.Status != "pending" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("daily PDF did not finish")
		case <-time.After(100 * time.Millisecond):
		}
	}
	if status.Status != "ready" || status.PageCount == nil {
		t.Fatalf("daily PDF status: %+v", status)
	}
	pdf := call("a1", "GET", pdfPath+"/file", nil, 200, nil)
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("expected a PDF")
	}
	if folder := os.Getenv("FIMRA_REPORT_CHECK_DIR"); folder != "" {
		name := filepath.Join(folder, "open-fimra-daily")
		text := []string{"Daily situation return", "Hill Division", "Revision 2", "Approved",
			"Assistance tally corrected.", fmt.Sprintf("Assistance received %.0f", current.DailyReturn.Totals["assistance.received"]),
			fmt.Sprintf("Satellite reports %.0f", current.DailyReturn.Totals["source.satellite"])}
		expected, _ := json.Marshal(map[string]any{"pages": *status.PageCount, "text": text})
		for ext, data := range map[string][]byte{".pdf": pdf, ".json": expected} {
			if err := os.WriteFile(name+ext, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := pool.Exec(ctx, "UPDATE situation_reports SET expires_at=now() WHERE id=$1::uuid", pdfJob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := expireSituationReports(ctx, pool); err != nil {
		t.Fatal(err)
	}
}

func TestDailyDate(t *testing.T) {
	for day, want := range map[string]bool{"2026-03-08": true, "2026-02-29": false, "0000-01-01": false, "": false} {
		if got := dailyDate(day); got != want {
			t.Errorf("dailyDate(%q) = %v, want %v", day, got, want)
		}
	}
}
