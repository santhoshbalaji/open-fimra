package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

func TestIncidentResponseIntegration(t *testing.T) {
	ctx, pool := notificationTestDB(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "SET LOCAL jit=off"); err != nil {
		t.Fatal(err)
	}
	suffix, slug, prefix := loadSeedCopy(t, ctx, tx)
	workspace := prefix + "01"
	if _, err := tx.Exec(ctx, `UPDATE layer_features
SET geom=ST_Multi(ST_Union(geom,ST_MakeEnvelope(86.25,16.34,86.27,16.35,4326)))
WHERE layer_version_id=$1 AND code='F01'`, prefix+"31"); err != nil {
		t.Fatal(err)
	}
	request := notificationRequests(t, api{reader: tx, logger: slog.Default()}.handler, prefix, "")
	base := "/api/v1/workspaces/" + slug
	call := func(user, method, path string, input any, want int, out any) {
		t.Helper()
		var raw string
		if input != nil {
			body, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			raw = string(body)
		}
		res := request(user, method, base+path, raw)
		if res.Code != want {
			t.Fatalf("%s %s as %s: HTTP %d, want %d: %s", method, path, user, res.Code, want, res.Body)
		}
		if out != nil && json.Unmarshal(res.Body.Bytes(), out) != nil {
			t.Fatalf("invalid response from %s: %s", path, res.Body)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	day := now.Format(time.DateOnly)
	first, second := 1, 2
	settings := responseSettings{Extended: true, Targets: []responseTarget{
		{From: day, Through: day, Step: "new", TargetMinutes: first, EscalationMinutes: &second},
	}}
	settingPath := "/settings/incident-response"
	call("a2", http.MethodPut, settingPath, settings, 403, nil)
	invalid := settings
	invalid.Targets = append(append([]responseTarget{}, settings.Targets...), settings.Targets[0])
	call("a1", http.MethodPut, settingPath, invalid, 400, nil)

	type intakeReceipt struct{ IncidentID, AlertID, Source string }
	lon, lat := 86.295, 16.420
	radio := intakeRequest{ID: "1e3a0000-0000-4000-8000-000000000001", Source: "radio",
		ObservedAt: now.Add(-6 * time.Hour), Longitude: &lon, Latitude: &lat, Note: "Smoke above the fictional ridge."}
	var old intakeReceipt
	oldInput := radio
	oldInput.ID = "1e3a0000-0000-4000-8000-000000000002"
	oldInput.ObservedAt = now.Add(-60 * time.Hour)
	call("a2", http.MethodPost, "/fire-intake", oldInput, 201, &old)

	var saved responseSettings
	call("a1", http.MethodPut, settingPath, settings, 200, &saved)
	if !reflect.DeepEqual(saved, settings) {
		t.Fatalf("saved response settings: %+v", saved)
	}

	var intake intakeReceipt
	call("a3", http.MethodPost, "/fire-intake", radio, 403, nil)
	call("a2", http.MethodPost, "/fire-intake", radio, 201, &intake)
	var replay intakeReceipt
	call("a2", http.MethodPost, "/fire-intake", radio, 200, &replay)
	if intake != replay || intake.Source != "radio" {
		t.Fatalf("intake was not retained: %+v %+v", intake, replay)
	}
	casePath := "/incidents/" + intake.IncidentID

	run := ingestRun{Source: "response_" + suffix, Product: demoProduct, WorkspaceID: &workspace}
	counts, err := runIngest(ctx, tx, run, func(context.Context) ([]firms.Observation, error) {
		return []firms.Observation{{Satellite: "N", Instrument: "VIIRS", Longitude: 86.2988, Latitude: 16.420,
			ObservedAt: now.Add(-time.Hour), Raw: map[string]string{"reference_id": "overpass"}}}, nil
	})
	if err != nil || counts.Inserted != 1 {
		t.Fatalf("satellite ingest: %+v %v", counts, err)
	}
	var satellite, satelliteFirst, satelliteCase string
	if err := tx.QueryRow(ctx, `SELECT m.alert_id::text,m.incident_id::text,m.first_incident_id::text FROM incident_members m
JOIN alerts a ON a.id=m.alert_id JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
WHERE a.workspace_id=$1 AND d.source=$2`, workspace, run.Source).
		Scan(&satellite, &satelliteCase, &satelliteFirst); err != nil || satelliteCase != intake.IncidentID {
		t.Fatalf("radio case %s and satellite case %s: %v", intake.IncidentID, satelliteCase, err)
	}

	for range 3 {
		if err := checkOverdueNotifications(ctx, tx, now.Add(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	var notifications []string
	if err := tx.QueryRow(ctx, `SELECT array_agg(n.event||':'||right(u.oidc_subject,2) ORDER BY n.event,u.oidc_subject)
FROM notifications n JOIN users u ON u.id=n.recipient_id
WHERE n.workspace_id=$1 AND n.event IN ('incident_overdue','incident_escalated') AND n.channel='in_app'`,
		workspace).Scan(&notifications); err != nil || !reflect.DeepEqual(notifications,
		[]string{"incident_escalated:a1", "incident_overdue:a1", "incident_overdue:a2"}) {
		t.Fatalf("expected one notice per recipient and escalation: %v (%v)", notifications, err)
	}

	move := func(path, user, step string, info map[string]any, want int) {
		t.Helper()
		if info == nil {
			info = map[string]any{}
		}
		info["step"] = step
		call(user, http.MethodPost, path+"/steps", info, want, nil)
	}
	oldPath := "/incidents/" + old.IncidentID
	move(oldPath, "a2", "acknowledged", nil, 200)
	move(oldPath, "a2", "assigned", nil, 409)
	move(oldPath, "a2", "team_sent", nil, 200)
	move(casePath, "a2", "acknowledged", nil, 200)
	move(casePath, "a2", "assigned", map[string]any{"note": "Ridge crew"}, 200)
	move(casePath, "a2", "team_sent", nil, 422)
	move(casePath, "a2", "team_sent", map[string]any{"estimatedAt": now.Add(time.Hour)}, 200)
	move(casePath, "a2", "reached_site", map[string]any{"reason": "Arrival confirmed by radio."}, 200)
	flash := map[string]any{"clientId": "field-01", "actualAt": now.Add(-45 * time.Minute),
		"response": map[string]any{"status": "Fire active", "helpNeeded": "Water refill"}}
	move(casePath, "a2", "flash_update", flash, 200)
	move(casePath, "a2", "flash_update", flash, 200)
	update := map[string]any{"clientId": "field-02", "estimatedAt": now.Add(-30 * time.Minute),
		"response": map[string]any{"status": "Flames slowing"}}
	move(casePath, "a2", "response_update", update, 200)
	move(casePath, "a2", "response_update", map[string]any{"response": map[string]string{"status": "Crew on site"}}, 200)
	move(casePath, "a2", "controlled", map[string]any{"reason": "Radio confirmation."}, 200)
	move(casePath, "a2", "extinguished", map[string]any{"reason": "Radio confirmation."}, 200)
	move(casePath, "a2", "watch", nil, 200)
	move(casePath, "a2", "reopened", nil, 422)
	move(casePath, "a2", "reopened", map[string]any{"reason": "Hot edge confirmed on recheck."}, 200)

	var history json.RawMessage
	call("a2", http.MethodGet, casePath+"/history", nil, 200, &history)
	for _, detail := range []string{"Radio report", "Fire active", "Flames slowing", "Hot edge confirmed", "syncedAt"} {
		if !strings.Contains(string(history), detail) {
			t.Fatalf("history missing %q: %s", detail, history)
		}
	}
	var timingOK bool
	if err := tx.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(occurred_at > COALESCE(actual_at,estimated_at)
 AND recorded_at >= occurred_at) FROM step_log WHERE incident_id=$1::text::bigint AND client_id IN ('field-01','field-02')`,
		intake.IncidentID).Scan(&timingOK); err != nil || !timingOK {
		t.Fatalf("offline times and idempotency: %v %v", timingOK, err)
	}

	// A field phone records late steps on its alert. They also appear on the case.
	fieldLon, fieldLat := 86.255, 16.345
	staff := intakeRequest{ID: "1e3a0000-0000-4000-8000-000000000003", Source: "staff",
		ObservedAt: now.Add(-4 * time.Hour), Longitude: &fieldLon, Latitude: &fieldLat}
	var field intakeReceipt
	call("a3", http.MethodPost, "/fire-intake", staff, 201, &field)
	fieldCase := "/incidents/" + field.IncidentID
	fieldAlert := "/alerts/" + field.AlertID
	move(fieldAlert, "a3", "acknowledged", nil, 200)
	move(fieldAlert, "a3", "assigned", map[string]any{"note": "Ridge crew"}, 403)
	move(fieldAlert, "a2", "assigned", map[string]any{"note": "Ridge crew"}, 200)
	move(fieldAlert, "a3", "team_sent", map[string]any{"estimatedAt": now.Add(time.Hour)}, 200)
	point := map[string]float64{"longitude": fieldLon, "latitude": fieldLat}
	move(fieldAlert, "a3", "reached_site", map[string]any{"location": point}, 200)
	move(fieldAlert, "a3", "flash_update", map[string]any{"clientId": "phone-flash", "clientTime": now.Add(-time.Hour),
		"response": map[string]any{"status": "Field crew reports active fire"}}, 200)
	move(fieldAlert, "a3", "response_update", map[string]any{"clientId": "phone-update", "estimatedAt": now.Add(-time.Minute),
		"response": map[string]any{"status": "Fire perimeter checked"}}, 200)
	move(fieldAlert, "a3", "controlled", map[string]any{"location": point}, 200)
	move(fieldAlert, "a3", "extinguished", map[string]any{"location": point}, 200)
	call("a3", http.MethodGet, fieldCase+"/history", nil, 200, &history)
	if !strings.Contains(string(history), "Field crew reports active fire") || !strings.Contains(string(history), "Fire perimeter checked") {
		t.Fatalf("alert updates did not reach case history: %s", history)
	}
	move(fieldCase, "a2", "unsafe", nil, 200)
	move(fieldCase, "a3", "reopened", map[string]any{"reason": "New evidence."}, 403)
	move(fieldCase, "a2", "reopened", map[string]any{"reason": "New evidence."}, 200)
	for _, outcome := range []string{"false_alarm", "duplicate", "controlled_burn", "not_reachable", "outside_jurisdiction"} {
		move(fieldCase, "a2", outcome, nil, 200)
		move(fieldCase, "a2", "reopened", map[string]any{"reason": "New evidence."}, 200)
	}
	public := staff
	public.ID = "1e3a0000-0000-4000-8000-000000000004"
	public.Source = "public"
	public.IncidentID = field.IncidentID
	call("a2", http.MethodPost, "/fire-intake", public, 201, &replay)
	if replay.IncidentID != field.IncidentID || replay.AlertID == field.AlertID {
		t.Fatalf("an explicit link should keep a second source record: %+v", replay)
	}

	// Split and merge preserve both the original memberships and the case logs.
	type membershipReceipt struct{ TargetID, SourceID, Reason string }
	split := membershipRequest{Operation: "split", AlertIDs: []string{satellite}, Reason: "Survey found a separate fire."}
	var impact membershipImpact
	call("a2", http.MethodPost, casePath+"/membership?preview=true", split, 200, &impact)
	call("a2", http.MethodPost, casePath+"/membership", split, 409, nil)
	split.PreviewKey = impact.PreviewKey
	var splitResult membershipReceipt
	call("a2", http.MethodPost, casePath+"/membership", split, 200, &splitResult)
	scopedPath := "/incidents/" + splitResult.TargetID
	move(scopedPath, "a2", "non_forest", nil, 200)
	merge := membershipRequest{Operation: "merge", TargetID: intake.IncidentID, Reason: "Recheck confirmed one fire."}
	call("a2", http.MethodPost, scopedPath+"/membership?preview=true", merge, 200, &impact)
	merge.PreviewKey = impact.PreviewKey
	call("a2", http.MethodPost, scopedPath+"/membership", merge, 200, nil)
	call("a2", http.MethodGet, scopedPath+"/history", nil, 200, &history)
	if !strings.Contains(string(history), "Non-forest") {
		t.Fatalf("the old case lost its disposition history: %s", history)
	}
	if err := tx.QueryRow(ctx, `SELECT incident_id::text,first_incident_id::text FROM incident_members WHERE alert_id=$1::text::bigint`,
		satellite).Scan(&satelliteCase, &satelliteFirst); err != nil || satelliteCase != intake.IncidentID || satelliteFirst != intake.IncidentID {
		t.Fatalf("membership continuity: current %s first %s: %v", satelliteCase, satelliteFirst, err)
	}
}
