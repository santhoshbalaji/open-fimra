package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func checkSummary(t *testing.T, ctx context.Context, tx pgx.Tx, slug, prefix string, request func(string, string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	// Keep the simulated re-match local to this test.
	save, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer save.Rollback(ctx)
	base := "/api/v1/workspaces/" + slug
	workspace := prefix + "01"
	// The workflow test closed one alert. Take a second to its first report and
	// leave it overdue. Both contribute observations to the response percentiles.
	var id string
	if err := tx.QueryRow(ctx, `SELECT a.id::text FROM alerts a JOIN workflow_instances i ON i.alert_id=a.id
 WHERE workspace_id=$1 AND current_step='new' AND observed_at BETWEEN now()-interval '7 days' AND now()
 ORDER BY a.id LIMIT 1`, workspace).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"acknowledged", "team_sent", "reached_site", "controlled", "extinguished", "first_report"} {
		body := fmt.Sprintf(`{"step":%q}`, step)
		if step == "reached_site" || step == "controlled" || step == "extinguished" {
			body = fmt.Sprintf(`{"step":%q,"reason":"Synthetic radio report"}`, step)
		}
		w := request("POST", base+"/alerts/"+id+"/steps", "a1", body)
		if w.Code != 200 {
			t.Fatalf("move to %s: %d %s", step, w.Code, w.Body)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow_instances SET entered_at=now()-interval '4 days' WHERE alert_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	// A replay moves the detection outside its former forest and unit. Its
	// already-routed alert keeps its original class, path and workflow.
	if _, err := tx.Exec(ctx, `UPDATE detection_matches m SET forest_class='outside',hierarchy_unit_paths='{}',
    unit_paths='{}',hierarchy='{}',triage_reason=NULL,forest_feature_id=NULL FROM alerts a
    WHERE a.id=$1 AND (m.workspace_id,m.detection_id,m.observed_at)=(a.workspace_id,a.detection_id,a.observed_at)`, id); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, subject, path string
		review              bool
	}{
		{"state.admin", "a1", "", true}, {"division.officer", "a2", "ds.c1.d1", true}, {"beat.guard", "a3", "ds.c1.d1.r1.b01", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Freeze the expected population from the known seed hierarchy and grant.
			// These checks use neither API queries nor the shared grant and timing views.
			for _, filter := range []struct {
				query, class, path string
				hours              int
			}{
				{"", "", "", 168}, {"?hours=24&class=inside&unitPath=ds.c1.d1", "inside", "ds.c1.d1", 24}, {"?hours=1", "", "", 1},
			} {
				_, err := tx.Exec(ctx, `CREATE TEMP TABLE summary_matches ON COMMIT DROP AS
    SELECT detection_id,observed_at,forest_class FROM detection_matches
    WHERE workspace_id=$1 AND observed_at<=now()
    AND ($2='' OR hierarchy_unit_paths -> $3::text ? $2)
    AND ($4='' OR forest_class=$4)
    AND ($5='' OR EXISTS (SELECT FROM unnest(unit_paths) p WHERE p::text=$5 OR starts_with(p::text,$5||'.')))`,
					pgx.QueryExecModeSimpleProtocol, workspace, tc.path, prefix+"10", filter.class, filter.path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = tx.Exec(ctx, `CREATE TEMP TABLE summary_alerts ON COMMIT DROP AS
    SELECT a.id,a.queue,a.created_at,i.template_id,i.entered_at,i.current_step
    FROM alerts a
    JOIN workflow_instances i ON i.alert_id=a.id WHERE a.workspace_id=$1
    AND a.observed_at BETWEEN now()-$2::integer*interval '1 hour' AND now() AND ($3 OR a.queue='unit')
    AND ($4='' OR a.hierarchy_unit_paths -> $5::text ? $4)
    AND ($6='' OR a.forest_class=$6)
    AND ($7='' OR EXISTS (SELECT FROM unnest(a.unit_paths) p WHERE p::text=$7 OR starts_with(p::text,$7||'.')))`,
					pgx.QueryExecModeSimpleProtocol, workspace, filter.hours, tc.review, tc.path, prefix+"10", filter.class, filter.path)
				if err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				w := request("GET", base+"/summary"+filter.query, tc.subject, "")
				elapsed := time.Since(start)
				if w.Code != 200 {
					t.Fatalf("%d: %s", w.Code, w.Body)
				}
				var got workspaceSummary
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				count := func(query string, args ...any) int {
					t.Helper()
					var n int
					if err := tx.QueryRow(ctx, query, args...).Scan(&n); err != nil {
						t.Fatal(err)
					}
					return n
				}
				check := func(label string, got, want int) {
					t.Helper()
					if got != want {
						t.Fatalf("%s%s: got %d want %d", label, filter.query, got, want)
					}
				}
				for _, c := range []string{"inside", "near_edge", "outside", "fixed_heat_source"} {
					check(c, got.Detections[c], count("SELECT count(*) FROM summary_matches WHERE forest_class=$1 AND observed_at>=now()-$2::int*interval '1 hour'", c, filter.hours))
					check(c+" today", got.Today[c], count("SELECT count(*) FROM summary_matches WHERE forest_class=$1 AND (observed_at AT TIME ZONE 'Asia/Kolkata')::date=(now() AT TIME ZONE 'Asia/Kolkata')::date", c))
				}
				for _, q := range []string{"unit", "edge_review", "triage"} {
					check(q, got.Queues[q], count("SELECT count(*) FROM summary_alerts WHERE queue=$1", q))
				}
				var steps map[string]int
				if err := tx.QueryRow(ctx, `SELECT coalesce(jsonb_object_agg(current_step,n),'{}') FROM
   (SELECT current_step,count(*) n FROM summary_alerts GROUP BY 1) s`).Scan(&steps); err != nil || !reflect.DeepEqual(steps, got.Steps) {
					t.Fatalf("steps %v want %v: %v", got.Steps, steps, err)
				}
				check("active", got.Active, count("SELECT count(*) FROM summary_alerts WHERE current_step NOT IN ('closed','false_alarm','duplicate','controlled_burn','not_reachable')"))
				check("pending", got.PendingReports, count("SELECT count(*) FROM summary_alerts WHERE current_step IN ('first_report','final_report')"))
				check("overdue", got.Overdue, count(`SELECT count(*) FROM summary_alerts a WHERE now()-entered_at >
   (SELECT min(time_limit) FROM workflow_transitions WHERE template_id=a.template_id AND from_step=a.current_step)`))
				for _, step := range []string{"acknowledged", "reached_site", "controlled"} {
					var expected responsePercentiles
					err := tx.QueryRow(ctx, `SELECT percentile_cont(.5) WITHIN GROUP (ORDER BY duration),
    percentile_cont(.9) WITHIN GROUP (ORDER BY duration) FROM (
    SELECT extract(epoch FROM min(l.occurred_at)-a.created_at) duration FROM summary_alerts a
    JOIN step_log l ON l.alert_id=a.id WHERE l.to_step=$1 GROUP BY a.id,a.created_at) milestones`, step).Scan(&expected.Median, &expected.P90)
					actual, ok := got.ResponseSeconds[step]
					if err != nil || !ok || !reflect.DeepEqual(expected, actual) {
						t.Fatalf("%s percentiles got %+v want %+v: %v", step, actual, expected, err)
					}
				}
				if tc.subject == "a1" {
					var expected []summaryFeed
					err := tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(f) ORDER BY source,product),'[]') FROM (
    SELECT source, product, CASE WHEN now()-coalesce(max(fetched_at) FILTER (WHERE status='succeeded'),min(started_at))>interval '1 hour' THEN 'failing'
    WHEN now()-coalesce(max(latest_observed_at),min(started_at))>interval '36 hours' THEN 'quiet'
    WHEN now()-coalesce(max(latest_observed_at),min(started_at))>interval '18 hours' THEN 'delayed' ELSE 'healthy' END status
    FROM ingest_runs WHERE workspace_id=$1 AND kind='poll' GROUP BY source,product) f`, workspace).Scan(&expected)
					if err != nil || !reflect.DeepEqual(got.FeedHealth, expected) {
						t.Fatalf("feeds %+v want %+v: %v", got.FeedHealth, expected, err)
					}
				} else if got.FeedHealth != nil {
					t.Fatal("feed health visible outside root grant")
				}
				if _, err := tx.Exec(ctx, "DROP TABLE summary_matches,summary_alerts"); err != nil {
					t.Fatal(err)
				}
				if filter.query == "" {
					samples := []time.Duration{elapsed}
					for range 19 {
						start = time.Now()
						if w := request("GET", base+"/summary", tc.subject, ""); w.Code != 200 {
							t.Fatal(w.Body)
						}
						samples = append(samples, time.Since(start))
					}
					slices.Sort(samples)
					t.Logf("summary, 20 signed requests: median %s, p95 %s; active %d, overdue %d, pending %d", samples[9], samples[18], got.Active, got.Overdue, got.PendingReports)
					if samples[18] > 500*time.Millisecond {
						t.Fatal("demo summary exceeded 500 ms p95 budget")
					}
				}
			}
		})
	}
	if w := request("GET", base+"/summary", "a4", ""); w.Code != 403 {
		t.Fatalf("no.role got %d", w.Code)
	}
}
