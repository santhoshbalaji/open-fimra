package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// TestWizardIntegration takes a fresh install to a working alert map using
// only the first-run wizard's endpoints. It needs a database with migrations
// applied and nothing else, a geo worker on that database, Keycloak and the
// mail catcher: run it with make wizard-test-integration.
func TestWizardIntegration(t *testing.T) {
	if os.Getenv("FIMRA_WIZARD_INTEGRATION") != "1" {
		t.Skip("Run make wizard-test-integration with the development stack running.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	cfg := loadConfig()
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var used bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM workspaces) OR EXISTS (SELECT 1 FROM users)
			OR EXISTS (SELECT 1 FROM role_assignments) OR EXISTS (SELECT 1 FROM install_setup)
	`).Scan(&used); err != nil || used {
		t.Fatalf("the wizard test needs an empty database with only migrations applied (used: %v, error: %v)", used, err)
	}

	// Capture every log line to check that no secret or address is written.
	var logs lockedBuffer
	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(&logs, os.Stderr), nil))
	settingsKey := randomSettingsKey(t)
	secrets, err := newSettingsCipher(settingsKey, "")
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeFIRMS(t, testMapKey)
	keys := newFIRMSKeys(pool, secrets, "", fake.transport())
	const setupCode = "WZRD-TEST-CODE-2345"

	t.Setenv("FIMRA_SMTP_ADDR", os.Getenv("FIMRA_TEST_MAILPIT_SMTP"))
	sender := "wizard-test-" + strings.ToLower(rand.Text()) + "@example.invalid"
	t.Setenv("FIMRA_SMTP_FROM", sender)
	t.Setenv("FIMRA_SMTP_SECURITY", "local")
	t.Setenv("FIMRA_SMTP_USERNAME", "")
	t.Setenv("FIMRA_SMTP_PASSWORD", "")
	t.Setenv("FIMRA_WEB_URL", "http://127.0.0.1:4200")
	mailpit, err := readSMTPConfig()
	if err != nil || mailpit == nil {
		t.Fatalf("configure the mail catcher: %v", err)
	}
	// The poll runs below on demand, so the River client gets no FIRMS workers.
	jobs, err := newJobClient(pool, logger, nil, mailpit, pushSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := jobs.Stop(stop); err != nil {
			t.Error(err)
		}
	})

	provider, err := discoverProvider(ctx, cfg.issuer)
	if err != nil {
		t.Fatal(err)
	}
	auth := newAuthenticator(provider, cfg.issuer, cfg.audience)
	auth.emailEnabled = true
	store, err := newLocalAttachments(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandler(pool, auth, logger, devOrigins, cfg.uploadDir, jobs,
		withSetup(newSetupGuard(setupCode), secrets, keys), withAttachments(store, defaultAttachmentBytes)))
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 20 * time.Second}
	tokens := map[string]string{}
	for _, user := range []string{"state.admin", "division.officer", "beat.guard", "no.role"} {
		tokens[user] = demoToken(t, client, provider.Endpoint().TokenURL, user)
	}

	var responses lockedBuffer
	call := func(user, method, path string, body any, want int, out any) {
		t.Helper()
		var reader io.Reader
		contentType := ""
		switch b := body.(type) {
		case nil:
		case *multipartBody:
			reader, contentType = &b.buf, b.contentType
		default:
			data, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			reader, contentType = bytes.NewReader(data), "application/json"
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if user != "" {
			req.Header.Set("Authorization", "Bearer "+tokens[user])
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
		responses.Write(data)
		if res.StatusCode != want {
			t.Fatalf("%s %s as %s got HTTP %d (%s), want %d", method, path, user, res.StatusCode, data, want)
		}
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
		}
	}
	send := func(user, method, path, contentType string, body []byte) (int, []byte, http.Header) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", contentType)
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
		responses.Write(data)
		return res.StatusCode, data, res.Header
	}
	me := func(user string) meResponse {
		t.Helper()
		var profile meResponse
		call(user, http.MethodGet, "/api/v1/me", nil, http.StatusOK, &profile)
		return profile
	}

	// Each step builds on the one before, so a failure stops the whole test.
	section := func(name string, run func(*testing.T)) {
		t.Helper()
		t.Log("Step: " + name)
		run(t)
	}

	const slug = "wizard-state"
	base := "/api/v1/workspaces/" + slug
	var state setupState
	pack := starterPackRun{ctx: ctx, pool: pool, base: base, call: call, send: send}

	section("first administrator", func(t *testing.T) {
		if profile := me("state.admin"); !profile.Setup.BootstrapAvailable || len(profile.RoleAssignments) != 0 {
			t.Fatalf("a fresh install must offer setup: %+v", profile)
		}
		request := bootstrapRequest{SetupCode: "WRONG-CODE-0000-0000", Mode: "state",
			WorkspaceName: "Wizard State", WorkspaceSlug: slug, StateCode: "DS"}
		call("no.role", http.MethodPost, "/api/v1/setup/bootstrap", request, http.StatusForbidden, nil)
		request.Mode = "national"
		request.SetupCode = setupCode
		call("no.role", http.MethodPost, "/api/v1/setup/bootstrap", request, http.StatusBadRequest, nil)
		call("", http.MethodPost, "/api/v1/setup/bootstrap", request, http.StatusUnauthorized, nil)
		request.Mode = "state"

		// Two claims at once: exactly one wins.
		var wg sync.WaitGroup
		statuses := make(chan int, 2)
		for range 2 {
			wg.Go(func() {
				data, _ := json.Marshal(request)
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/setup/bootstrap", bytes.NewReader(data))
				req.Header.Set("Authorization", "Bearer "+tokens["state.admin"])
				res, err := client.Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				res.Body.Close()
				statuses <- res.StatusCode
			})
		}
		wg.Wait()
		close(statuses)
		got := map[int]int{}
		for status := range statuses {
			got[status]++
		}
		if got[http.StatusCreated] != 1 || got[http.StatusConflict] != 1 {
			t.Fatalf("two concurrent claims gave %v, want one 201 and one 409", got)
		}
		// Later claims, even with the right code, fail.
		request.WorkspaceSlug = "second"
		call("no.role", http.MethodPost, "/api/v1/setup/bootstrap", request, http.StatusConflict, nil)

		var claims, audits int
		if err := pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM install_setup WHERE claimed_by = 'd0000000-0000-4000-8000-0000000000a1'),
			       (SELECT count(*) FROM audit_log WHERE action = 'setup.bootstrap' AND actor_id = 'd0000000-0000-4000-8000-0000000000a1')
		`).Scan(&claims, &audits); err != nil || claims != 1 || audits != 1 {
			t.Fatalf("expected one claim and one audit entry, got %d and %d (%v)", claims, audits, err)
		}
		profile := me("state.admin")
		if profile.Setup.BootstrapAvailable || len(profile.RoleAssignments) != 1 ||
			profile.RoleAssignments[0].Role != "state_admin" || profile.RoleAssignments[0].Unit.Path != "ds" ||
			len(profile.Setup.Unfinished) != 1 || profile.Setup.Unfinished[0] != slug {
			t.Fatalf("the first administrator must hold the root grant and see the wizard: %+v", profile)
		}
		call("no.role", http.MethodGet, base+"/setup", nil, http.StatusForbidden, nil)
		if profile := me("no.role"); profile.Setup.BootstrapAvailable || len(profile.Setup.Unfinished) != 0 {
			t.Fatalf("other users must not see setup: %+v", profile)
		}
	})

	section("hierarchy preset", func(t *testing.T) {
		call("state.admin", http.MethodGet, base+"/setup", nil, http.StatusOK, &state)
		preset, _ := findPreset("circle-division-range-beat")
		if len(state.Presets) < 2 || state.Mode != "state" || state.Steps["workspace"].Status != "done" {
			t.Fatalf("unexpected initial setup state: %+v", state)
		}
		levels := make([]presetLevel, len(preset.Levels))
		for i, level := range preset.Levels {
			levels[i] = presetLevel{Slug: level.Slug, Labels: levelLabels{"en": level.Labels["en"], "hi": level.Labels["hi"]}}
		}
		levels[4].Labels["hi"] = "बीट (वन)"
		call("state.admin", http.MethodPost, base+"/setup/hierarchy",
			hierarchyRequest{Preset: preset.Slug, Levels: levels[:1]}, http.StatusBadRequest, nil)
		call("state.admin", http.MethodPost, base+"/setup/hierarchy",
			hierarchyRequest{Preset: preset.Slug, Levels: append([]presetLevel{{Slug: "province", Labels: levelLabels{"en": "Province"}}}, levels[1:]...)},
			http.StatusConflict, nil)
		call("state.admin", http.MethodPost, base+"/setup/hierarchy", hierarchyRequest{Preset: preset.Slug, Levels: levels}, http.StatusOK, &state)
		if len(state.Levels) != 5 || state.Levels[4].Labels["hi"] != "बीट (वन)" || *state.Preset != preset.Slug ||
			len(state.Layers) != 7 || state.Steps["hierarchy"].Status != "done" {
			t.Fatalf("unexpected hierarchy: %+v", state)
		}
	})

	upload := func(layer setupLayer, name string, data []byte) draftResponse {
		t.Helper()
		body := &multipartBody{}
		form := multipart.NewWriter(&body.buf)
		settings := map[string]any{"nameField": "name", "codeField": "code"}
		fields := map[string]string{"layerSlug": layer.Slug, "layerName": layer.Name, "role": layer.Role}
		if layer.Depth != nil {
			settings["depth"] = *layer.Depth
			settings["parentCodeField"] = "parent_code"
			fields["hierarchy"] = state.Hierarchy
		}
		encoded, _ := json.Marshal(settings)
		fields["settings"] = string(encoded)
		for key, value := range fields {
			if err := form.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
		part, err := form.CreateFormFile("file", name+".geojson")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		body.contentType = form.FormDataContentType()
		var draft draftResponse
		call("state.admin", http.MethodPost, base+"/layer-imports", body, http.StatusAccepted, &draft)
		for draft.Status == "pending" {
			select {
			case <-ctx.Done():
				t.Fatalf("timed out waiting for the geo worker to import %s", name)
			case <-time.After(150 * time.Millisecond):
			}
			call("state.admin", http.MethodGet, base+"/layer-imports/"+draft.VersionID.String(), nil, http.StatusOK, &draft)
		}
		if draft.Status != "ready" {
			t.Fatalf("%s import: %+v", name, draft)
		}
		return draft
	}
	preview := func(draft draftResponse) (blocked, confirmation string) {
		t.Helper()
		var result struct {
			Blocked           string `json:"blocked"`
			GrantConfirmation string `json:"grantConfirmation"`
		}
		call("state.admin", http.MethodGet, base+"/layer-imports/"+draft.VersionID.String()+"/preview", nil, http.StatusOK, &result)
		return result.Blocked, result.GrantConfirmation
	}
	publish := func(layer setupLayer, name string, data []byte) {
		t.Helper()
		draft := upload(layer, name, data)
		blocked, confirmation := preview(draft)
		if blocked != "" || confirmation != "" {
			t.Fatalf("%s cannot be published: %q, grant confirmation %q, problems %+v", name, blocked, confirmation, draft.Problems)
		}
		call("state.admin", http.MethodPost, base+"/layer-imports/"+draft.VersionID.String()+"/publish",
			map[string]any{"days": 30, "grantConfirmation": ""}, http.StatusAccepted, nil)
		t.Logf("published %s: %d units, %d features, %d review items", name, draft.UnitCount, draft.FeatureCount, len(draft.Problems))
	}

	section("layers", func(t *testing.T) {
		call("state.admin", http.MethodPost, base+"/setup/steps/layers", map[string]string{"status": "done"}, http.StatusConflict, nil)
		stateFile := readLayerFixture(t, "state")
		// A state code that differs from the one chosen at setup must not
		// replace the root unit that holds the administrator's grant.
		wrong := upload(state.Layers[0], "state", bytes.Replace(stateFile, []byte(`"DS"`), []byte(`"XS"`), 1))
		if blocked, _ := preview(wrong); !strings.Contains(blocked, "state code") {
			t.Fatalf("a mismatched state code must block publishing, got %q", blocked)
		}
		publish(state.Layers[0], "state", stateFile)
		call("state.admin", http.MethodGet, base+"/setup", nil, http.StatusOK, &state)
		if !state.BoundarySet || !state.Root.Published || state.Root.Path != "ds" {
			t.Fatalf("publishing the state level must set the workspace boundary: %+v", state)
		}
		for i, name := range []string{"circles", "divisions", "ranges"} {
			publish(state.Layers[i+1], name, readLayerFixture(t, name))
		}

		// The demo beats have deliberate defects that block publishing.
		raw := upload(state.Layers[4], "beats", readLayerFixture(t, "beats"))
		var resumed setupState
		call("state.admin", http.MethodGet, base+"/setup", nil, http.StatusOK, &resumed)
		beats := resumed.Layers[4]
		if resumed.Steps["hierarchy"].Status != "done" || resumed.Steps["layers"].Status != "" ||
			beats.Published || beats.Latest == nil || beats.Latest.VersionID != raw.VersionID {
			t.Fatalf("setup must resume with the unfinished layer and its draft: %+v", beats)
		}
		for _, ref := range []string{"feature:16", "feature:22", "feature:17"} {
			if !hasBlockingProblem(raw, ref) {
				t.Fatalf("beats should block on %s: %+v", ref, raw.Problems)
			}
		}
		call("state.admin", http.MethodPost, base+"/layer-imports/"+raw.VersionID.String()+"/publish",
			map[string]any{"days": 30, "grantConfirmation": ""}, http.StatusConflict, nil)
		publish(state.Layers[4], "beats", repairedBeats(t))
		publish(state.Layers[5], "forest", readLayerFixture(t, "forest"))
		publish(state.Layers[6], "exclusions", readLayerFixture(t, "exclusions"))
		call("state.admin", http.MethodPost, base+"/setup/steps/layers", map[string]string{"status": "skipped"}, http.StatusBadRequest, nil)
		call("state.admin", http.MethodPost, base+"/setup/steps/layers", map[string]string{"status": "done"}, http.StatusOK, &state)
		for _, layer := range state.Layers {
			if !layer.Published {
				t.Fatalf("%s is not published: %+v", layer.Slug, state.Layers)
			}
		}
	})

	section("FIRMS key", func(t *testing.T) {
		call("state.admin", http.MethodPost, base+"/setup/steps/firms", map[string]string{"status": "done"}, http.StatusConflict, nil)
		const wrongKey = "fedcba9876543210fedcba9876543210"
		var status firmsStatus
		var test firmsTestResult
		call("state.admin", http.MethodPut, base+"/settings/firms", map[string]string{"mapKey": "not a key"}, http.StatusBadRequest, nil)
		call("division.officer", http.MethodPut, base+"/settings/firms", map[string]string{"mapKey": testMapKey}, http.StatusForbidden, nil)
		call("state.admin", http.MethodPut, base+"/settings/firms", map[string]string{"mapKey": wrongKey}, http.StatusOK, &status)
		call("state.admin", http.MethodPost, base+"/settings/firms/test", nil, http.StatusOK, &test)
		if test.OK || !strings.Contains(test.Message, "HTTP 403") {
			t.Fatalf("a rejected key must fail the test: %+v", test)
		}
		call("state.admin", http.MethodPut, base+"/settings/firms", map[string]string{"mapKey": testMapKey}, http.StatusOK, &status)
		if !status.Set || status.Source != keyFromSettings || status.Hint != "cdef" || status.SetAt == nil || !status.Encryption {
			t.Fatalf("unexpected key status: %+v", status)
		}
		call("state.admin", http.MethodPost, base+"/settings/firms/test", nil, http.StatusOK, &test)
		if !test.OK || test.Source != keyFromSettings {
			t.Fatalf("the saved key must pass the test: %+v", test)
		}
		var stored []byte
		if err := pool.QueryRow(ctx, "SELECT ciphertext FROM workspace_settings_secrets").Scan(&stored); err != nil ||
			bytes.Contains(stored, []byte(testMapKey)) {
			t.Fatalf("the key must be stored encrypted: %v", err)
		}
		// Older installations keep working with the environment variable.
		if _, source, err := newFIRMSKeys(pool, secrets, "environmentkey000000", nil).key(ctx, "00000000-0000-4000-8000-000000000000"); err != nil || source != keyFromEnvironment {
			t.Fatalf("a workspace without a saved key must fall back to FIMRA_FIRMS_MAP_KEY: %s %v", source, err)
		}
		call("state.admin", http.MethodPost, base+"/setup/steps/firms", map[string]string{"status": "done"}, http.StatusOK, nil)
	})

	section("starter forms", pack.enable)

	section("invite officers", func(t *testing.T) {
		invites := []invitationRequest{
			{Email: "Division.Officer@example.invalid", Role: "division_officer", UnitPath: "ds.c1.d1"},
			{Email: "beat.guard@example.invalid", Role: "field_staff", UnitPath: "ds.c1.d1.r2.b05"},
			{Email: "no.role@example.invalid", Role: "field_staff", UnitPath: "ds.c1.d1.r1.b01"},
			{Email: "late.officer@example.invalid", Role: "field_staff", UnitPath: "ds.c1.d1.r1.b01"},
		}
		call("state.admin", http.MethodPost, base+"/invitations", invitationRequest{Email: "a@b@example.invalid", Role: "field_staff", UnitPath: "ds"}, http.StatusBadRequest, nil)
		call("state.admin", http.MethodPost, base+"/invitations", invitationRequest{Email: "x@example.invalid", Role: "field_staff", UnitPath: "ds.c9"}, http.StatusBadRequest, nil)
		call("division.officer", http.MethodPost, base+"/invitations", invites[0], http.StatusForbidden, nil)
		for _, invite := range invites {
			call("state.admin", http.MethodPost, base+"/invitations", invite, http.StatusCreated, nil)
		}
		var list struct {
			Invitations []invitation `json:"invitations"`
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			call("state.admin", http.MethodGet, base+"/invitations", nil, http.StatusOK, &list)
			sent := 0
			for _, item := range list.Invitations {
				if strings.Contains(item.Email, ".officer@") || strings.Contains(item.Email, ".guard@") {
					t.Fatalf("the list must mask addresses: %+v", item)
				}
				if item.EmailStatus == "sent" {
					sent++
				}
			}
			if len(list.Invitations) == 4 && sent == 4 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("invitation emails were not sent: %+v", list.Invitations)
			}
			time.Sleep(300 * time.Millisecond)
		}
		for _, invite := range invites {
			address := strings.ToLower(invite.Email)
			if got := countInvitationMail(t, ctx, sender, address); got != 1 {
				t.Fatalf("the mail catcher has %d invitations for a test address, want 1", got)
			}
		}
		call("state.admin", http.MethodPost, base+"/setup/steps/invitations", map[string]string{"status": "done"}, http.StatusOK, nil)
	})

	section("finish", func(t *testing.T) {
		call("state.admin", http.MethodPost, base+"/setup/finish", nil, http.StatusOK, &state)
		if state.FinishedAt == nil || len(me("state.admin").Setup.Unfinished) != 0 {
			t.Fatalf("the wizard must close once finished: %+v", state)
		}
		call("state.admin", http.MethodPost, base+"/setup/reopen", nil, http.StatusOK, &state)
		if state.FinishedAt != nil || len(me("state.admin").Setup.Unfinished) != 1 {
			t.Fatal("reopening must show the wizard again")
		}
		for _, step := range setupSteps {
			if state.Steps[step].Status != "done" {
				t.Fatalf("reopening must keep completed steps: %s = %+v", step, state.Steps[step])
			}
		}
		call("state.admin", http.MethodPost, base+"/setup/steps/invitations", map[string]string{"status": "skipped"}, http.StatusOK, nil)
		call("state.admin", http.MethodGet, base+"/setup", nil, http.StatusOK, &state)
		if state.Steps["invitations"].Status != "skipped" {
			t.Fatalf("resume must retain a skipped step: %+v", state.Steps["invitations"])
		}
		call("state.admin", http.MethodPost, base+"/setup/finish", nil, http.StatusOK, &state)
	})

	section("claim invitations", func(t *testing.T) {
		// An expired invitation grants nothing and is deleted.
		if _, err := pool.Exec(ctx, `UPDATE invitations SET created_at = now() - interval '15 days',
			expires_at = now() - interval '1 day' WHERE email = 'no.role@example.invalid'`); err != nil {
			t.Fatal(err)
		}
		if profile := me("no.role"); profile.Setup.Claimed != 0 || len(profile.RoleAssignments) != 0 {
			t.Fatalf("an expired invitation must not grant a role: %+v", profile)
		}

		// A token whose email is not verified cannot claim an invitation.
		local := localTokens(t, pool, logger)
		if res := local("late-officer", "late.officer@example.invalid", false); res.Setup.Claimed != 0 || len(res.RoleAssignments) != 0 {
			t.Fatalf("an unverified email must not claim: %+v", res)
		}
		if res := local("late-officer", "late.officer@example.invalid", true); res.Setup.Claimed != 1 || len(res.RoleAssignments) != 1 {
			t.Fatalf("a verified email must claim: %+v", res)
		}

		for user, want := range map[string]string{"division.officer": "ds.c1.d1", "beat.guard": "ds.c1.d1.r2.b05"} {
			profile := me(user)
			if profile.Setup.Claimed != 1 || len(profile.RoleAssignments) != 1 || profile.RoleAssignments[0].Unit.Path != want {
				t.Fatalf("%s must claim one grant at %s: %+v", user, want, profile)
			}
			if again := me(user); again.Setup.Claimed != 0 || len(again.RoleAssignments) != 1 {
				t.Fatalf("%s claimed twice: %+v", user, again)
			}
		}
		var left, claimed, expired int
		if err := pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM invitations),
			       (SELECT count(*) FROM audit_log WHERE action = 'invitation.claim'),
			       (SELECT count(*) FROM audit_log WHERE action = 'invitation.expire')
		`).Scan(&left, &claimed, &expired); err != nil || left != 0 || claimed != 3 || expired != 1 {
			t.Fatalf("invitations left %d, claimed %d, expired %d (%v)", left, claimed, expired, err)
		}
	})

	section("alert map", func(t *testing.T) {
		// The poller reads the key saved in settings. The fake server accepts
		// only that key, so a successful run proves which key was used.
		if err := pollFIRMS(ctx, pool, keys.workspaceFetch, time.Now()); err != nil {
			t.Fatal(err)
		}
		var runs, inserted int
		if err := pool.QueryRow(ctx, `
			SELECT count(*), COALESCE(sum(inserted), 0) FROM ingest_runs r JOIN workspaces w ON w.id = r.workspace_id
			WHERE w.slug = $1 AND r.status = 'succeeded' AND r.kind = 'poll'
		`, slug).Scan(&runs, &inserted); err != nil || runs != len(firms.Products) || inserted != 11 {
			t.Fatalf("expected %d successful polls inserting 11 detections, got %d and %d (%v)", len(firms.Products), runs, inserted, err)
		}
		for _, tc := range []struct {
			user               string
			unit, edge, triage int
			review             bool
			underPath          string
		}{
			{"state.admin", 4, 1, 2, true, "ds"},
			{"division.officer", 1, 0, 1, true, "ds.c1.d1"},
			{"beat.guard", 1, 0, 0, false, "ds.c1.d1.r2.b05"},
		} {
			var page struct {
				Counts    map[string]int    `json:"counts"`
				Items     []json.RawMessage `json:"items"`
				CanReview bool              `json:"canReview"`
			}
			call(tc.user, http.MethodGet, base+"/alerts", nil, http.StatusOK, &page)
			if page.Counts["unit"] != tc.unit || page.Counts["edge_review"] != tc.edge || page.Counts["triage"] != tc.triage ||
				len(page.Items) != tc.unit+tc.edge+tc.triage || page.CanReview != tc.review {
				t.Fatalf("%s sees %v (review %v), want unit %d, edge %d, triage %d", tc.user, page.Counts, page.CanReview, tc.unit, tc.edge, tc.triage)
			}
			for _, raw := range page.Items {
				var alert struct {
					Queue    string `json:"queue"`
					UnitPath string `json:"unit_path"`
				}
				_ = json.Unmarshal(raw, &alert)
				if alert.Queue == "unit" && !strings.HasPrefix(alert.UnitPath+".", tc.underPath+".") {
					t.Fatalf("%s sees a unit alert at %s outside %s", tc.user, alert.UnitPath, tc.underPath)
				}
			}
			t.Logf("%s: %d unit, %d edge review, %d triage alerts", tc.user, tc.unit, tc.edge, tc.triage)
		}
		call("no.role", http.MethodGet, base+"/alerts", nil, http.StatusForbidden, nil)
		status := 0
		for _, user := range []string{"state.admin", "division.officer", "beat.guard"} {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+base+"/tiles/detections/8/189/116.mvt?classes=inside", nil)
			req.Header.Set("Authorization", "Bearer "+tokens[user])
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if status = res.StatusCode; status != http.StatusOK {
				t.Fatalf("%s got HTTP %d for the alert map tile", user, status)
			}
		}
	})

	section("starter forms on the default workflow", pack.workflow)
	section("manual fire report", pack.manualReport)

	section("no secrets or addresses leak", func(t *testing.T) {
		for name, output := range map[string]string{"logs": logs.String(), "API responses": responses.String()} {
			for _, secret := range []string{testMapKey, "fedcba9876543210fedcba9876543210", settingsKey, setupCode} {
				if strings.Contains(output, secret) {
					t.Fatalf("a secret appeared in the %s", name)
				}
			}
			if name == "logs" && strings.Contains(output, "example.invalid") && !strings.Contains(output, "open-fimra@example.invalid") {
				t.Fatal("an invited address appeared in the logs")
			}
		}
		var leaks int
		if err := pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM audit_log WHERE details::text LIKE '%@%' OR target_id LIKE '%@%')
			     + (SELECT count(*) FROM river_job WHERE args::text LIKE '%@%')
		`).Scan(&leaks); err != nil || leaks != 0 {
			t.Fatalf("an address is stored in the audit log or job queue: %d (%v)", leaks, err)
		}
	})
}

type multipartBody struct {
	buf         bytes.Buffer
	contentType string
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func hasBlockingProblem(draft draftResponse, ref string) bool {
	for _, problem := range draft.Problems {
		if problem.FeatureRef != nil && *problem.FeatureRef == ref && problem.BlocksPublish {
			return true
		}
	}
	return false
}

// repairedBeats fixes the demo beats as an officer would after the review
// list: it fills the two missing codes and removes the B17 bulge into B20.
func repairedBeats(t *testing.T) []byte {
	t.Helper()
	var collection struct {
		Type     string `json:"type"`
		Features []struct {
			Type       string            `json:"type"`
			Properties map[string]string `json:"properties"`
			Geometry   struct {
				Type        string         `json:"type"`
				Coordinates [][][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(readLayerFixture(t, "beats"), &collection); err != nil {
		t.Fatal(err)
	}
	rings := map[string][][2]float64{}
	for i, f := range collection.Features {
		if f.Properties["code"] == "" {
			f.Properties["code"] = fmt.Sprintf("B%02d", i+1)
		}
		if f.Properties["parent_code"] == "" {
			f.Properties["parent_code"] = fmt.Sprintf("R%d", i/3+1)
		}
		rings[f.Properties["code"]] = f.Geometry.Coordinates[0]
	}
	for i, a := range rings["B17"] {
		for _, b := range rings["B20"] {
			if a[1] == b[1] && a[0]-b[0] > .00399 && a[0]-b[0] < .00401 {
				rings["B17"][i] = b
			}
		}
	}
	encoded, err := json.Marshal(collection)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func countInvitationMail(t *testing.T, ctx context.Context, sender, address string) int {
	t.Helper()
	endpoint := os.Getenv("FIMRA_TEST_MAILPIT_URL") + "/api/v1/search?query=" +
		url.QueryEscape(`from:"`+sender+`" to:"`+address+`" subject:"You are invited to Open FIMRA"`)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("reach the mail catcher: %v", err)
	}
	defer res.Body.Close()
	var search struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&search) != nil {
		t.Fatalf("search the mail catcher: HTTP %d", res.StatusCode)
	}
	return len(search.Messages)
}

// localTokens signs tokens with a test key so the email_verified claim can be
// false, which the demo accounts never are. It returns the /api/v1/me answer.
func localTokens(t *testing.T, pool *pgxpool.Pool, logger *slog.Logger) func(subject, email string, verified bool) meResponse {
	t.Helper()
	key, signer := testSigner(t)
	const issuer, audience = "https://issuer.invalid", "open-fimra-api"
	auth := authenticator{issuers: map[string]bool{issuer: true}, verifier: oidc.NewVerifier(issuer,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: audience})}
	handler := newHandler(pool, auth, logger, devOrigins, "", nil)
	return func(subject, email string, verified bool) meResponse {
		t.Helper()
		claims := jwt.Claims{Issuer: issuer, Subject: "wizard-test-" + subject, Audience: jwt.Audience{audience},
			Expiry: jwt.NewNumericDate(time.Now().Add(time.Minute))}
		token, err := jwt.Signed(signer).Claims(claims).Claims(map[string]any{
			"preferred_username": subject, "email": email, "email_verified": verified,
		}).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var profile meResponse
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &profile) != nil {
			t.Fatalf("local sign-in got HTTP %d: %s", w.Code, w.Body)
		}
		return profile
	}
}
