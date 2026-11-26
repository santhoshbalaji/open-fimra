package main

import (
	"context"
	"crypto"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

func TestDispatchIntegration(t *testing.T) {
	if os.Getenv("FIMRA_API_INTEGRATION") != "1" {
		t.Skip("Run make api-test-integration.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
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
	defer tx.Rollback(context.Background())
	suffix, slug, prefix := loadSeedCopy(t, ctx, tx)
	now := time.Now().UTC().Truncate(time.Minute)
	key, signer := testSigner(t)
	const issuer, audience = "https://issuer.invalid", "open-fimra-api"
	auth := authenticator{issuers: map[string]bool{issuer: true}, verifier: oidc.NewVerifier(issuer,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: audience})}
	handler := api{reader: tx, feedDB: tx, logger: slog.Default()}.handler(auth, devOrigins)
	base := "/api/v1/workspaces/" + slug
	call := func(method, path, subject string, input any, status int) string {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, base+path, strings.NewReader(string(body)))
		token := signedToken(t, signer, jwt.Claims{Issuer: issuer, Subject: prefix + subject,
			Audience: jwt.Audience{audience}, Expiry: jwt.NewNumericDate(now.Add(5 * time.Minute))})
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s as %s: got %d, want %d: %s", method, path, subject, w.Code, status, w.Body)
		}
		return w.Body.String()
	}
	unit := func(path string) string {
		t.Helper()
		var id string
		if err := tx.QueryRow(ctx, "SELECT id::text FROM units WHERE hierarchy_id = $1 AND path = $2::ltree",
			prefix+"10", path).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	hillBeat := unit("ds.c1.d1.r1.b01")
	reserveBeat := unit("ds.c2.d4.r7.b19")
	lake := unit("ds.c2.d4")
	river := unit("ds.c2.d3")
	change := now.Add(-3 * time.Hour)
	current := dispatchRuleInput{Name: "Reserve core", Priority: 10, EffectiveFrom: change,
		OverlayLayer: "protected-areas", OverlayCode: "PA01-C", OverlayZone: "core", Lead: lake,
		Supporting: []string{hillBeat}}
	earlier := current
	earlier.Name, earlier.EffectiveFrom, earlier.EffectiveTo, earlier.Lead =
		"Earlier reserve rule", now.Add(-10*time.Hour), &change, reserveBeat
	earlier.Supporting = nil
	call("GET", "/settings/dispatch-rules", "a2", nil, 403)
	call("POST", "/settings/dispatch-rules", "a1", earlier, 201)
	call("POST", "/settings/dispatch-rules", "a1", current, 201)
	competing := current
	competing.Name, competing.EffectiveFrom, competing.Lead = "Competing core rule", now.Add(-time.Hour), reserveBeat
	var rule struct{ ID string }
	if err := json.Unmarshal([]byte(call("POST", "/settings/dispatch-rules", "a1", competing, 201)), &rule); err != nil {
		t.Fatal(err)
	}
	call("PUT", "/settings/dispatch-rules/"+rule.ID+"/end", "a1",
		map[string]time.Time{"effectiveTo": now.Add(time.Hour)}, 200)
	call("GET", "/settings/dispatch-rules", "a1", nil, 200)

	source := "dispatch_" + suffix
	observations := []firms.Observation{
		{Satellite: "N", Instrument: "VIIRS", Longitude: 86.591907, Latitude: 16.338721, ObservedAt: now.Add(-4 * time.Hour)},
		{Satellite: "N", Instrument: "VIIRS", Longitude: 86.591907, Latitude: 16.338721, ObservedAt: change},
		{Satellite: "N", Instrument: "VIIRS", Longitude: 86.591907, Latitude: 16.338721, ObservedAt: now.Add(-30 * time.Minute)},
		{Satellite: "N", Instrument: "VIIRS", Longitude: 86.558840, Latitude: 16.418379, ObservedAt: now.Add(-10 * time.Minute)},
	}
	// Read a single alert by observation time to check the stored decision.
	_, err = runIngest(ctx, tx, ingestRun{Source: source, Product: demoProduct},
		func(context.Context) ([]firms.Observation, error) { return observations, nil })
	if err != nil {
		t.Fatal(err)
	}
	alert := func(index int) string {
		t.Helper()
		var id string
		if err := tx.QueryRow(ctx, `SELECT a.id::text FROM alerts a
            JOIN detections d ON (d.observed_at, d.id) = (a.observed_at, a.detection_id)
            WHERE a.workspace_id = $1 AND d.source = $2 AND a.observed_at = $3`,
			prefix+"01", source, observations[index].ObservedAt).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return "/alerts/" + id
	}
	contains := func(body string, values ...string) {
		t.Helper()
		for _, value := range values {
			if !strings.Contains(body, value) {
				t.Fatalf("response lacks %q: %s", value, body)
			}
		}
	}
	contains(call("GET", alert(0), "a1", nil, 200), "Earlier reserve rule", "Beat 19")
	contains(call("GET", alert(1), "a3", nil, 200), "Reserve core", "Lake Division", "Beat 01", prefix+"61")
	contains(call("GET", alert(2), "a1", nil, 200), "dispatch_rule_conflict", "candidates", "Beat 19")
	call("GET", alert(2), "a3", nil, 404)
	contains(call("GET", alert(3), "a1", nil, 200), "Beat 17", "Beat 20", "overlapping_units")

	// A supporting officer sees the alert but cannot hand it over.
	assign := alertAssignmentRequest{river, "Send the river team."}
	call("GET", alert(1)+"/assignment", "a2", nil, 404)
	call("POST", alert(1)+"/assignment", "a2", assign, 422)
	call("POST", alert(2)+"/assignment", "a1", alertAssignmentRequest{reserveBeat, "Confirm the reserve beat."}, 200)
	call("POST", alert(1)+"/assignment", "a1", assign, 200)

	duty := dutyShiftInput{Name: "Reserve duty", Kind: "control_room", StartsAt: now.Add(time.Hour),
		EndsAt: now.Add(2 * time.Hour), Units: []string{river}}
	var member string
	if err := tx.QueryRow(ctx, "SELECT id::text FROM users WHERE oidc_subject = $1", prefix+"a2").Scan(&member); err != nil {
		t.Fatal(err)
	}
	duty.Members = []string{member}
	call("GET", "/settings/duty", "a2", nil, 403)
	var shift struct{ ID string }
	if err := json.Unmarshal([]byte(call("POST", "/settings/duty", "a1", duty, 201)), &shift); err != nil {
		t.Fatal(err)
	}
	moveShift := func(window string) {
		t.Helper()
		if _, err := tx.Exec(ctx, fmt.Sprintf("UPDATE duty_shifts SET %s WHERE id = $1::text::bigint", window), shift.ID); err != nil {
			t.Fatal(err)
		}
	}
	call("GET", alert(1)+"/support", "a2", nil, 404)
	moveShift("starts_at = statement_timestamp()")
	call("GET", alert(1)+"/support", "a2", nil, 200)
	call("GET", alert(1), "a2", nil, 200)
	support := alertAssignmentRequest{reserveBeat, "Ask for a local crew."}
	first := call("POST", alert(1)+"/support", "a2", support, 200)
	if retry := call("POST", alert(1)+"/support", "a2", support, 200); retry != first {
		t.Fatalf("support receipt changed on retry: %s / %s", first, retry)
	}
	contains(call("GET", alert(1), "a3", nil, 200), "Ask for a local crew.", "Reserve core")
	moveShift("starts_at = statement_timestamp() - interval '1 minute', ends_at = statement_timestamp()")
	call("GET", alert(1)+"/support", "a2", nil, 404)
	contains(call("GET", "/settings/duty", "a1", nil, 200), "Reserve duty")
	// A watcher can see the alert during coverage, and loses access on cancellation.
	call("GET", alert(0), "a3", nil, 404)
	if err := tx.QueryRow(ctx, "SELECT id::text FROM users WHERE oidc_subject = $1", prefix+"a3").Scan(&member); err != nil {
		t.Fatal(err)
	}
	watcher := dutyShiftInput{Name: "Reserve watch", Kind: "watcher", StartsAt: now.Add(-time.Minute),
		EndsAt: now.Add(time.Hour), Units: []string{reserveBeat}, Members: []string{member}}
	if err := json.Unmarshal([]byte(call("POST", "/settings/duty", "a1", watcher, 201)), &shift); err != nil {
		t.Fatal(err)
	}
	call("GET", alert(0), "a3", nil, 200)
	call("DELETE", "/settings/duty/"+shift.ID, "a1", nil, 200)
	call("GET", alert(0), "a3", nil, 404)
	var log, homeRoles int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE workspace_id = $1 AND
          action IN ('alert.assign', 'alert.transfer', 'alert.support', 'duty.schedule', 'duty.cancel', 'dispatch.rule.create', 'dispatch.rule.end')`, prefix+"01").Scan(&log); err != nil || log != 10 {
		t.Fatalf("expected ten audit rows, got %d: %v", log, err)
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM role_assignments WHERE user_id = $1::text::uuid", member).Scan(&homeRoles); err != nil || homeRoles != 1 {
		t.Fatalf("home grants changed: %d, %v", homeRoles, err)
	}
}
