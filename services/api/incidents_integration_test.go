package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// Synthetic reference clusters in the fictional Demo State. Distances are
// geodesic and use the default rule: 1,500 metres and 24 hours.
type clusterPoint struct {
	id       string
	lon, lat float64
	hoursAgo float64
}

var incidentPoints = []clusterPoint{
	// Three fires in beat B05 about 400 metres and three hours apart: one incident.
	{"group-1", 86.295, 16.420, 8}, {"group-2", 86.2988, 16.420, 5}, {"group-3", 86.2988, 16.4236, 2},
	// 1,594 metres south of group-1 at the same time: just outside the distance.
	{"too-far", 86.295, 16.4056, 2},
	// The same place as group-1, 25 hours earlier: just outside the time window.
	{"too-late", 86.295, 16.420, 33},
	// Two fires 1,923 metres apart. A third, 961 metres from each, arrives later.
	{"bridge-west", 86.290, 16.420, 62}, {"bridge-east", 86.308, 16.420, 61},
	// 854 metres apart across the B05 and B08 boundary. B08 is outside the
	// division officer's division.
	{"units-b05", 86.312, 16.430, 100}, {"units-b08", 86.320, 16.430, 99.5},
	// 1,068 metres apart across the B01 and B04 boundary. The beat guard has B01 only.
	{"units-b01", 86.255, 16.345, 130}, {"units-b04", 86.265, 16.345, 129.5},
}

var bridgePoint = clusterPoint{"bridge-middle", 86.299, 16.420, 60}

var wantClusters = [][]string{
	{"bridge-east", "bridge-middle", "bridge-west"}, {"group-1", "group-2", "group-3"},
	{"too-far"}, {"too-late"}, {"units-b01", "units-b04"}, {"units-b05", "units-b08"},
}

func TestIncidentsIntegration(t *testing.T) {
	ctx, pool := notificationTestDB(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	suffix, slug, prefix := loadSeedCopy(t, ctx, tx)
	workspaceID := prefix + "01"
	// A synthetic forest patch across the B01 and B04 boundary.
	if _, err := tx.Exec(ctx, `UPDATE layer_features SET geom=ST_Multi(ST_Union(geom,ST_MakeEnvelope(86.25,16.34,86.27,16.35,4326)))
WHERE layer_version_id=$1 AND code='F01'`, prefix+"31"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	source := "incidents_" + suffix
	run := ingestRun{Source: source, Product: demoProduct, WorkspaceID: &workspaceID}
	ingest := func(db ingestDB, points ...clusterPoint) ingestCounts {
		t.Helper()
		observations := make([]firms.Observation, len(points))
		for i, p := range points {
			observations[i] = firms.Observation{Satellite: "N", Instrument: "VIIRS", Longitude: p.lon, Latitude: p.lat,
				ObservedAt: now.Add(-time.Duration(p.hoursAgo * float64(time.Hour))), Raw: map[string]string{"reference_id": p.id}}
		}
		counts, err := runIngest(ctx, db, run, func(context.Context) ([]firms.Observation, error) { return observations, nil })
		if err != nil {
			t.Fatal(err)
		}
		return counts
	}
	type member struct {
		Ref                 string
		Incident, FirstSeen int64
	}
	members := func(db pgx.Tx) map[string]member {
		t.Helper()
		rows, err := db.Query(ctx, `SELECT d.raw->>'reference_id',m.incident_id,m.first_incident_id
FROM alerts a JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
JOIN incident_members m ON m.alert_id=a.id WHERE a.workspace_id=$1 AND d.source=$2`, workspaceID, source)
		if err != nil {
			t.Fatal(err)
		}
		found, err := pgx.CollectRows(rows, pgx.RowToStructByPos[member])
		if err != nil {
			t.Fatal(err)
		}
		byRef := map[string]member{}
		for _, m := range found {
			byRef[m.Ref] = m
		}
		return byRef
	}
	clusters := func(byRef map[string]member) [][]string {
		groups := map[int64][]string{}
		for ref, m := range byRef {
			groups[m.Incident] = append(groups[m.Incident], ref)
		}
		var result [][]string
		for _, refs := range groups {
			sort.Strings(refs)
			result = append(result, refs)
		}
		sort.Slice(result, func(i, j int) bool { return result[i][0] < result[j][0] })
		return result
	}

	t.Run("arrival order does not change membership", func(t *testing.T) {
		reverse, err := tx.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer reverse.Rollback(ctx)
		// Thirteen one-point ingests: skip query compilation to keep this quick.
		if _, err := reverse.Exec(ctx, "SET LOCAL jit = off"); err != nil {
			t.Fatal(err)
		}
		all := append(slices.Clone(incidentPoints), bridgePoint)
		sort.Slice(all, func(i, j int) bool { return all[i].hoursAgo < all[j].hoursAgo })
		for _, p := range all {
			ingest(reverse, p)
		}
		if got := clusters(members(reverse)); !reflect.DeepEqual(got, wantClusters) {
			t.Fatalf("newest first gave %v, want %v", got, wantClusters)
		}
	})

	if counts := ingest(tx, incidentPoints...); counts.Inserted != len(incidentPoints) {
		t.Fatalf("ingest clusters: %+v", counts)
	}
	before := members(tx)
	if before["bridge-west"].Incident == before["bridge-east"].Incident {
		t.Fatal("fires 1,923 metres apart share an incident before the bridge")
	}
	ingest(tx, bridgePoint)
	byRef := members(tx)
	if got := clusters(byRef); !reflect.DeepEqual(got, wantClusters) {
		t.Fatalf("clusters %v, want %v", got, wantClusters)
	}
	survivor, absorbed := before["bridge-west"].Incident, before["bridge-east"].Incident
	var mergedInto int64
	if err := tx.QueryRow(ctx, "SELECT merged_into FROM incidents WHERE id=$1", absorbed).Scan(&mergedInto); err != nil || mergedInto != survivor ||
		byRef["bridge-middle"].Incident != survivor || byRef["bridge-east"].FirstSeen != absorbed {
		t.Fatalf("bridge merge: %d merged into %d, members %+v: %v", absorbed, mergedInto, byRef, err)
	}
	t.Logf("six reference clusters grouped; incident %d merged into %d", absorbed, survivor)

	notified := func() map[string][]string {
		t.Helper()
		rows, err := tx.Query(ctx, `SELECT d.raw->>'reference_id',ARRAY(SELECT u.username FROM notifications n JOIN users u ON u.id=n.recipient_id
 WHERE n.alert_id=a.id AND n.event='alert_new' AND n.channel='in_app' ORDER BY u.username)
FROM alerts a JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
WHERE a.workspace_id=$1 AND d.source=$2`, workspaceID, source)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string][]string{}
		for rows.Next() {
			var ref string
			var names []string
			if err := rows.Scan(&ref, &names); err != nil {
				t.Fatal(err)
			}
			got[ref] = names
		}
		return got
	}
	officer := []string{"division.officer"}
	wantNotified := map[string][]string{
		"group-1": officer, "group-2": {}, "group-3": {}, "too-far": officer, "too-late": officer,
		"bridge-west": officer, "bridge-east": officer, "bridge-middle": {},
		"units-b05": officer, "units-b08": {}, "units-b01": {"beat.guard", "division.officer"}, "units-b04": {},
	}
	t.Run("one new-alert notification per incident and recipient", func(t *testing.T) {
		if got := notified(); !reflect.DeepEqual(got, wantNotified) {
			t.Fatalf("notified %v, want %v", got, wantNotified)
		}
	})

	snapshot := func(db pgx.Tx) string {
		t.Helper()
		var state string
		err := db.QueryRow(ctx, `SELECT jsonb_build_object(
 'incidents',(SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id) FROM incidents i WHERE i.workspace_id=$1),
 'members',(SELECT jsonb_agg(to_jsonb(m) ORDER BY m.alert_id) FROM incident_members m JOIN alerts a ON a.id=m.alert_id WHERE a.workspace_id=$1),
 'workflows',(SELECT jsonb_agg(to_jsonb(wi) ORDER BY wi.id) FROM workflow_instances wi JOIN incidents i ON i.id=wi.incident_id WHERE i.workspace_id=$1),
 'steps',(SELECT count(*) FROM step_log l JOIN incidents i ON i.id=l.incident_id WHERE i.workspace_id=$1),
 'notifications',(SELECT count(*) FROM notifications WHERE workspace_id=$1))::text`, workspaceID).Scan(&state)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	t.Run("replay and re-match change no incident", func(t *testing.T) {
		want := snapshot(tx)
		if counts := ingest(tx, append(slices.Clone(incidentPoints), bridgePoint)...); counts.Duplicates != len(incidentPoints)+1 {
			t.Fatalf("replay: %+v", counts)
		}
		if got := snapshot(tx); got != want {
			t.Fatalf("replay changed incidents:\n%s\n%s", want, got)
		}
		// A shared test database can hold synthetic detections that other
		// tests committed before this workspace copy existed. The first
		// re-match gives them alerts, which group like any new alert. Every
		// later re-match must change nothing. The savepoint keeps the counts
		// below to this test's own clusters.
		rematch, err := tx.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer rematch.Rollback(ctx)
		for i := range 3 {
			if i == 1 {
				want = snapshot(rematch)
			}
			if _, err := rematch.Exec(ctx, `INSERT INTO layer_rematches(layer_version_id,river_job_id,window_start,window_end) VALUES($1,0,$2,$3)
ON CONFLICT(layer_version_id) DO UPDATE SET cursor_time=NULL,cursor_id=NULL,finished_at=NULL`, prefix+"31", now.Add(-140*time.Hour), now); err != nil {
				t.Fatal(err)
			}
			var done bool
			if err := rematch.QueryRow(ctx, rematchSQL, prefix+"31").Scan(&done); err != nil || !done {
				t.Fatalf("re-match did not finish: %v", err)
			}
			if got := snapshot(rematch); i > 0 && got != want {
				t.Fatalf("re-match changed incidents:\n%s\n%s", want, got)
			}
		}
	})

	request := notificationRequests(t, api{reader: tx, logger: slog.Default()}.handler, prefix, "")
	base := "/api/v1/workspaces/" + slug + "/incidents"
	call := func(subject, method, path, body string, status int, result any) {
		t.Helper()
		w := request(subject, method, path, body)
		if w.Code != status {
			t.Fatalf("%s %s %s: got %d, want %d: %s", subject, method, path, w.Code, status, w.Body)
		}
		if result != nil {
			if err := json.Unmarshal(w.Body.Bytes(), result); err != nil {
				t.Fatal(err)
			}
		}
	}
	type incident struct {
		ID            string
		Status        string
		AlertCount    int            `json:"alert_count"`
		MergedInto    *string        `json:"merged_into"`
		BeatNames     []string       `json:"beat_names"`
		DivisionNames []string       `json:"division_names"`
		Queues        map[string]int `json:"queues"`
		Bounds        []float64
	}
	type page struct {
		Items      []incident
		Total      int
		NextCursor string
	}
	id := func(ref string) string { return fmt.Sprint(byRef[ref].Incident) }
	counts := func(p page) map[string]int {
		got := map[string]int{}
		for _, item := range p.Items {
			got[item.ID] = item.AlertCount
		}
		return got
	}
	all := map[string]int{id("group-1"): 3, id("too-far"): 1, id("too-late"): 1, id("bridge-west"): 3, id("units-b05"): 2, id("units-b01"): 2}
	officerView := map[string]int{id("group-1"): 3, id("too-far"): 1, id("too-late"): 1, id("bridge-west"): 3, id("units-b05"): 1, id("units-b01"): 2}

	t.Run("each demo user sees incidents through their units", func(t *testing.T) {
		for _, tc := range []struct {
			user, subject string
			want          map[string]int
		}{
			{"state.admin", "a1", all}, {"division.officer", "a2", officerView}, {"beat.guard", "a3", map[string]int{id("units-b01"): 1}},
		} {
			var p page
			call(tc.subject, "GET", base, "", 200, &p)
			if got := counts(p); !reflect.DeepEqual(got, tc.want) || p.Total != len(tc.want) {
				t.Fatalf("%s sees %v (total %d), want %v", tc.user, got, p.Total, tc.want)
			}
			t.Logf("%s sees %d incidents", tc.user, len(tc.want))
		}
		call("a4", "GET", base, "", 403, nil)
		call("a3", "GET", base+"/"+id("units-b05"), "", 404, nil)
		call("a3", "GET", base+"/"+id("units-b05")+"/history", "", 404, nil)
		call("a3", "POST", base+"/"+id("units-b05")+"/steps", `{"step":"acknowledged"}`, 404, nil)
		call("a4", "GET", base+"/"+id("units-b01"), "", 403, nil)

		var detail incident
		call("a2", "GET", base+"/"+id("units-b05"), "", 200, &detail)
		if detail.AlertCount != 1 || !reflect.DeepEqual(detail.BeatNames, []string{"Beat 05"}) ||
			!reflect.DeepEqual(detail.DivisionNames, []string{"Hill Division"}) || detail.Bounds[0] != detail.Bounds[2] {
			t.Fatalf("officer detail leaks hidden members: %+v", detail)
		}
		call("a1", "GET", base+"/"+id("units-b05"), "", 200, &detail)
		if detail.AlertCount != 2 || len(detail.BeatNames) != 2 || len(detail.DivisionNames) != 2 {
			t.Fatalf("admin detail: %+v", detail)
		}
		call("a3", "GET", base+"/"+id("units-b01"), "", 200, &detail)
		if detail.AlertCount != 1 || !reflect.DeepEqual(detail.BeatNames, []string{"Beat 01"}) {
			t.Fatalf("guard detail: %+v", detail)
		}
		alerts := "/api/v1/workspaces/" + slug + "/alerts?incident="
		for _, tc := range []struct {
			subject, incident string
			want              int
		}{{"a1", id("units-b05"), 2}, {"a2", id("units-b05"), 1}, {"a3", id("units-b05"), 0}, {"a3", id("units-b01"), 1}, {"a1", fmt.Sprint(absorbed), 3}} {
			var rows alertPage
			call(tc.subject, "GET", alerts+tc.incident, "", 200, &rows)
			if len(rows.Items) != tc.want {
				t.Fatalf("%s members of %s: %d, want %d", tc.subject, tc.incident, len(rows.Items), tc.want)
			}
			for _, row := range rows.Items {
				var alert struct {
					Incident string `json:"incident_id"`
				}
				if err := json.Unmarshal(row, &alert); err != nil || (alert.Incident != tc.incident && tc.incident != fmt.Sprint(absorbed)) {
					t.Fatalf("member row %s: %v", row, err)
				}
			}
		}
		call("a1", "GET", alerts+"x", "", 400, nil)
	})

	t.Run("filters, pages and merged incidents", func(t *testing.T) {
		var p page
		call("a1", "GET", base+"?unitPath=ds.c1.d2", "", 200, &p)
		if got := counts(p); !reflect.DeepEqual(got, map[string]int{id("units-b05"): 2}) {
			t.Fatalf("unit path filter: %v", got)
		}
		call("a1", "GET", base+"?hours=24", "", 200, &p)
		if got := counts(p); !reflect.DeepEqual(got, map[string]int{id("group-1"): 3, id("too-far"): 1}) {
			t.Fatalf("hour filter: %v", got)
		}
		call("a1", "GET", base+"?queue=triage", "", 200, &p)
		if len(p.Items) != 0 {
			t.Fatalf("queue filter: %+v", p)
		}
		seen := map[string]bool{}
		call("a1", "GET", base+"?limit=4", "", 200, &p)
		for {
			for _, item := range p.Items {
				if seen[item.ID] {
					t.Fatal("duplicate cursor row", item.ID)
				}
				seen[item.ID] = true
			}
			if p.NextCursor == "" {
				break
			}
			cursor := p.NextCursor
			p = page{}
			call("a1", "GET", base+"?limit=4&cursor="+url.QueryEscape(cursor), "", 200, &p)
		}
		if len(seen) != len(all) || seen[fmt.Sprint(absorbed)] {
			t.Fatalf("pages returned %v", seen)
		}
		var merged incident
		call("a2", "GET", base+"/"+fmt.Sprint(absorbed), "", 200, &merged)
		if merged.MergedInto == nil || *merged.MergedInto != fmt.Sprint(survivor) || merged.AlertCount != 3 {
			t.Fatalf("merged detail: %+v", merged)
		}
		call("a1", "POST", base+"/"+fmt.Sprint(absorbed)+"/steps", `{"step":"acknowledged"}`, 409, nil)
		for _, query := range []string{"?status=old", "?class=bad", "?cursor=x", "?hours=0", "/0"} {
			call("a1", "GET", base+query, "", 400, nil)
		}
	})

	t.Run("activity sorting walks every visible incident in both directions", func(t *testing.T) {
		walk := func(direction string) []string {
			t.Helper()
			path := base + "?limit=2&sort=activity-" + direction
			next := path
			var ids []string
			seen := map[string]bool{}
			for next != "" {
				var p page
				call("a2", "GET", next, "", 200, &p)
				for _, row := range p.Items {
					if seen[row.ID] || row.AlertCount != officerView[row.ID] {
						t.Fatalf("invalid incident on %s page: %+v", direction, row)
					}
					seen[row.ID] = true
					ids = append(ids, row.ID)
				}
				next = ""
				if p.NextCursor != "" {
					next = path + "&cursor=" + url.QueryEscape(p.NextCursor)
				}
			}
			if len(ids) != len(officerView) {
				t.Fatalf("%s pages returned %v", direction, ids)
			}
			return ids
		}
		ascending, descending := walk("asc"), walk("desc")
		slices.Reverse(descending)
		if !slices.Equal(ascending, descending) {
			t.Fatalf("ascending order %v does not reverse descending order %v", ascending, descending)
		}
		call("a2", "GET", base+"?sort=unknown", "", 400, nil)
	})

	step := func(ref string) (string, string) {
		var current, entered string
		if err := tx.QueryRow(ctx, `SELECT wi.current_step,wi.entered_at::text FROM alerts a
JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at) JOIN workflow_instances wi ON wi.alert_id=a.id
WHERE a.workspace_id=$1 AND d.source=$2 AND d.raw->>'reference_id'=$3`, workspaceID, source, ref).Scan(&current, &entered); err != nil {
			t.Fatal(err)
		}
		return current, entered
	}
	type receipt struct {
		ID, Step, At string
		AlertsMoved  int
	}
	t.Run("incident steps move the members the caller may move", func(t *testing.T) {
		var r receipt
		ack := fmt.Sprintf(`{"step":"acknowledged","clientId":"room-1","clientTime":%q}`, time.Now().UTC().Format(time.RFC3339Nano))
		call("a2", "POST", base+"/"+id("group-1")+"/steps", ack, 200, &r)
		var again receipt
		call("a2", "POST", base+"/"+id("group-1")+"/steps", ack, 200, &again)
		if r.AlertsMoved != 3 || again != r {
			t.Fatalf("group acknowledgment %+v, replay %+v", r, again)
		}
		for _, ref := range []string{"group-1", "group-2", "group-3"} {
			if current, _ := step(ref); current != "acknowledged" {
				t.Fatalf("%s is at %s", ref, current)
			}
		}
		call("a2", "POST", base+"/"+id("units-b05")+"/steps", `{"step":"acknowledged"}`, 200, &r)
		if b05, _ := step("units-b05"); r.AlertsMoved != 1 || b05 != "acknowledged" {
			t.Fatalf("officer moved %d alerts, B05 at %s", r.AlertsMoved, b05)
		}
		if b08, _ := step("units-b08"); b08 != "new" {
			t.Fatalf("officer moved the hidden B08 alert to %s", b08)
		}
		call("a3", "POST", base+"/"+id("units-b01")+"/steps", `{"step":"false_alarm"}`, 403, nil)
		call("a3", "POST", base+"/"+id("units-b01")+"/steps", `{"step":"acknowledged"}`, 200, &r)
		if b04, _ := step("units-b04"); r.AlertsMoved != 1 || b04 != "new" {
			t.Fatalf("guard moved %d alerts, B04 at %s", r.AlertsMoved, b04)
		}
		call("a3", "POST", base+"/"+id("units-b01")+"/steps", `{"step":"team_sent"}`, 200, nil)
		call("a3", "POST", base+"/"+id("units-b01")+"/steps", `{"step":"reached_site","location":{"longitude":86.2,"latitude":16.2}}`, 422, nil)
		// Near the B04 member, which the guard cannot see.
		call("a3", "POST", base+"/"+id("units-b01")+"/steps", `{"step":"reached_site","location":{"longitude":86.266,"latitude":16.345}}`, 422, nil)
		call("a3", "POST", base+"/"+id("units-b01")+"/steps", `{"step":"reached_site","location":{"longitude":86.2555,"latitude":16.345}}`, 200, &r)
		if b01, _ := step("units-b01"); b01 != "reached_site" || r.AlertsMoved != 1 {
			t.Fatalf("guard arrival moved %d, B01 at %s", r.AlertsMoved, b01)
		}
		call("a1", "POST", base+"/"+id("units-b05")+"/steps", `{"step":"false_alarm","note":"Burning stubble"}`, 200, &r)
		if b08, _ := step("units-b08"); r.AlertsMoved != 2 || b08 != "false_alarm" {
			t.Fatalf("admin outcome moved %d, B08 at %s", r.AlertsMoved, b08)
		}
		var recent page
		call("a2", "GET", base+"?sort=activity-desc&limit=1", "", 200, &recent)
		if len(recent.Items) != 1 || recent.Items[0].ID != id("units-b05") {
			t.Fatalf("the last workflow update should be the newest activity: %+v", recent)
		}
		var history struct {
			Entries []struct {
				Step        string
				Actor       *string
				AlertsMoved int
			}
			Moves []any
		}
		call("a2", "GET", base+"/"+id("units-b05")+"/history", "", 200, &history)
		if len(history.Entries) != 3 || history.Entries[0].Actor != nil || history.Entries[2].Step != "false_alarm" ||
			history.Entries[2].AlertsMoved != 1 || len(history.Moves) != 0 {
			t.Fatalf("officer history: %+v", history)
		}
		var alertHistory struct {
			Entries []struct {
				Step     string
				Incident *string `json:"incidentId"`
			}
		}
		var b05 string
		if err := tx.QueryRow(ctx, `SELECT a.id::text FROM alerts a JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
WHERE a.workspace_id=$1 AND d.source=$2 AND d.raw->>'reference_id'='units-b05'`, workspaceID, source).Scan(&b05); err != nil {
			t.Fatal(err)
		}
		call("a2", "GET", "/api/v1/workspaces/"+slug+"/alerts/"+b05+"/history", "", 200, &alertHistory)
		if len(alertHistory.Entries) != 3 || alertHistory.Entries[1].Incident == nil || *alertHistory.Entries[1].Incident != id("units-b05") {
			t.Fatalf("alert history does not name the incident: %+v", alertHistory)
		}
		var audits int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='incident.step' AND target_id=$1`, id("units-b05")).Scan(&audits); err != nil || audits != 3 {
			t.Fatalf("incident audit rows %d: %v", audits, err)
		}
		var p page
		call("a1", "GET", base+"?status=false_alarm", "", 200, &p)
		if got := counts(p); !reflect.DeepEqual(got, map[string]int{id("units-b05"): 2}) {
			t.Fatalf("status filter: %v", got)
		}
	})

	t.Run("a closed incident takes no new members", func(t *testing.T) {
		call("a2", "POST", base+"/"+id("group-1")+"/steps", `{"step":"duplicate"}`, 200, nil)
		ingest(tx, clusterPoint{"after-close", 86.2988, 16.4236, 1})
		after := members(tx)
		if after["after-close"].Incident == byRef["group-3"].Incident {
			t.Fatal("a new fire joined a closed incident")
		}
	})

	t.Run("grouping settings", func(t *testing.T) {
		settings := "/api/v1/workspaces/" + slug + "/settings/incidents"
		var rule map[string]any
		call("a1", "GET", settings, "", 200, &rule)
		if rule["distanceMetres"] != 1500.0 || rule["windowHours"] != 24.0 {
			t.Fatalf("default rule %v", rule)
		}
		call("a2", "GET", settings, "", 403, nil)
		call("a4", "PUT", settings, `{"distanceMetres":2000,"windowHours":12}`, 403, nil)
		for _, body := range []string{`{"distanceMetres":50,"windowHours":12}`, `{"distanceMetres":2000,"windowHours":1.5}`, `{"distanceMetres":2000}`, `{"distanceMetres":2000,"windowHours":12,"x":1}`} {
			call("a1", "PUT", settings, body, 400, nil)
		}
		call("a1", "PUT", settings, `{"distanceMetres":2000,"windowHours":12}`, 200, &rule)
		if rule["distanceMetres"] != 2000.0 || rule["windowHours"] != 12.0 {
			t.Fatalf("saved rule %v", rule)
		}
		// A typing mistake in the stored settings falls back to the defaults.
		var distance float64
		var window string
		if err := tx.QueryRow(ctx, `SELECT distance_m,time_window::text FROM incident_rule('{"incidents":{"distanceMetres":"far","windowHours":-3}}')`).Scan(&distance, &window); err != nil || distance != 1500 || window != "24:00:00" {
			t.Fatalf("fallback rule %v %s: %v", distance, window, err)
		}
	})
	if !strings.HasPrefix(slug, "demo-test-") {
		t.Fatal("the test must run on its own workspace copy")
	}
}
