package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func checkWorkflow(t *testing.T, ctx context.Context, tx pgx.Tx, slug, prefix string, request func(string, string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	base := "/api/v1/workspaces/" + slug + "/alerts"
	var id string
	var lon, lat float64
	err := tx.QueryRow(ctx, `SELECT a.id::text,ST_X(d.geom),ST_Y(d.geom) FROM alerts a
 JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at) JOIN workspaces w ON w.id=a.workspace_id
 WHERE w.slug=$1 AND a.queue='unit' AND a.unit_path='ds.c1.d1.r2.b05' ORDER BY a.id DESC LIMIT 1`, slug).Scan(&id, &lon, &lat)
	if err != nil {
		t.Fatal(err)
	}
	path := base + "/" + id
	call := func(method, endpoint, user, body string, status int) string {
		t.Helper()
		w := request(method, path+endpoint, user, body)
		if w.Code != status {
			t.Fatalf("%s %s %s %s: got %d want %d: %s", method, endpoint, user, body, w.Code, status, w.Body)
		}
		return w.Body.String()
	}
	// A short deadline makes the overdue check deterministic even on fast runners.
	if _, err := tx.Exec(ctx, `UPDATE workflow_transitions SET time_limit=interval '1 second' WHERE from_step='new' AND to_step='acknowledged'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE workflow_instances SET entered_at=entered_at-interval '1 minute' WHERE alert_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	detail := func(step string, overdue bool) {
		t.Helper()
		var row struct {
			Status   string
			StepTime time.Time `json:"step_time"`
			Overdue  bool
		}
		if err := json.Unmarshal([]byte(call("GET", "", "a2", "", 200)), &row); err != nil || row.Status != step || row.StepTime.IsZero() || row.Overdue != overdue {
			t.Fatalf("alert detail %+v: %v", row, err)
		}
	}
	detail("new", true)
	call("GET", "/history", "a3", "", 404)
	call("POST", "/steps", "a3", `{"step":"acknowledged"}`, 404)
	call("POST", "/steps", "a1", `{"step":"controlled"}`, 409)
	if _, err := tx.Exec(ctx, `UPDATE role_assignments SET unit_path='ds.c1.d1.r2.b05' WHERE workspace_id=$1 AND role_slug='field_staff'`, prefix+"01"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := tx.Exec(ctx, `UPDATE role_assignments SET unit_path='ds.c1.d1.r1.b01' WHERE workspace_id=$1 AND role_slug='field_staff'`, prefix+"01"); err != nil {
			t.Error(err)
		}
	}()
	call("POST", "/steps", "a3", `{"step":"false_alarm"}`, 403)
	ack := fmt.Sprintf(`{"step":"acknowledged","clientId":"phone-1","clientTime":%q,"note":"On duty"}`, time.Now().UTC().Format(time.RFC3339Nano))
	first := call("POST", "/steps", "a3", ack, 200)
	call("POST", "/steps", "a3", `{"step":"team_sent"}`, 200)
	if first != call("POST", "/steps", "a3", ack, 200) {
		t.Fatal("replay changed receipt")
	}
	call("POST", "/steps", "a3", `{"step":"controlled","clientId":"phone-1"}`, 409)
	call("POST", "/steps", "a3", `{"step":"reached_site"}`, 422)
	call("POST", "/steps", "a3", `{"step":"reached_site","location":{"longitude":0,"latitude":0}}`, 422)
	call("POST", "/steps", "a3", `{"step":"reached_site","reason":"No GPS"}`, 403)
	for _, step := range []string{"reached_site", "controlled"} {
		call("POST", "/steps", "a3", fmt.Sprintf(`{"step":%q,"location":{"longitude":%f,"latitude":%f}}`, step, lon, lat), 200)
	}
	call("POST", "/steps", "a2", `{"step":"extinguished","reason":"Confirmed by radio"}`, 200)
	for _, step := range []string{"first_report", "final_report"} {
		call("POST", "/steps", "a3", fmt.Sprintf(`{"step":%q}`, step), 200)
	}
	call("POST", "/steps", "a3", `{"step":"verified"}`, 403)
	// A covering field grant cannot borrow the power of an unrelated officer grant.
	grantTx, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grantTx.Exec(ctx, `UPDATE role_assignments SET unit_path='ds.c2.d3' WHERE workspace_id=$1 AND role_slug='division_officer'`, prefix+"01"); err != nil {
		t.Fatal(err)
	}
	if _, err := grantTx.Exec(ctx, `INSERT INTO role_assignments(user_id,workspace_id,hierarchy_id,unit_path,role_slug)
  SELECT user_id,workspace_id,hierarchy_id,'ds.c1.d1.r2.b05','field_staff' FROM role_assignments
  WHERE workspace_id=$1 AND role_slug='division_officer'`, prefix+"01"); err != nil {
		t.Fatal(err)
	}
	call("POST", "/steps", "a2", `{"step":"verified"}`, 403)
	if err := grantTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	call("POST", "/steps", "a2", `{"step":"verified"}`, 200)
	call("POST", "/steps", "a1", `{"step":"closed"}`, 200)
	detail("closed", false)
	var history struct {
		Entries []struct {
			Step, Actor, At string
			Longitude       *float64
			ReportPending   bool
		}
		Moves []any
	}
	if err := json.Unmarshal([]byte(call("GET", "/history", "a3", "", 200)), &history); err != nil {
		t.Fatal(err)
	}
	steps := []string{"new", "acknowledged", "team_sent", "reached_site", "controlled", "extinguished", "first_report", "final_report", "verified", "closed"}
	if len(history.Entries) != len(steps) || len(history.Moves) != 0 {
		t.Fatal(history)
	}
	for i, e := range history.Entries {
		if e.Step != steps[i] || e.At == "" || (i > 0 && e.Actor == "") || ((i == 0 || i == 3 || i == 4) && e.Longitude == nil) || e.ReportPending != (i == 6 || i == 7) {
			t.Fatalf("bad entry: %+v", e)
		}
	}
	var n int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='alert.step' AND target_id=$1`, id).Scan(&n)
	if err != nil || n != 10 {
		t.Fatalf("audit rows %d: %v", n, err)
	}
	var measured bool
	err = tx.QueryRow(ctx, `SELECT to_acknowledged >= interval '0' AND to_controlled>=to_reached_site AND to_reached_site>=to_acknowledged FROM alert_response_times WHERE alert_id=$1`, id).Scan(&measured)
	if err != nil || !measured {
		t.Fatalf("response times: %v %v", measured, err)
	}
	for _, sql := range []string{"UPDATE step_log SET note='changed' WHERE alert_id=" + id, "DELETE FROM step_log WHERE alert_id=" + id, "TRUNCATE step_log"} {
		err := pgx.BeginFunc(ctx, tx, func(save pgx.Tx) error { _, err := save.Exec(ctx, sql); return err })
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("append-only protection: %v", err)
		}
	}
	t.Log("New to Closed: 10 timeline entries and 10 audit rows; response times measured; field, officer and admin permissions checked")
}
