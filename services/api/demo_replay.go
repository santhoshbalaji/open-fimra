package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jackc/pgx/v5"
	"github.com/santhoshbalaji/open-fimra/packages/starter-pack"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

const (
	operationsSeedPrefix = "d0000000-0000-4000-8000-0000000000"
	replayStateCode      = "XS-DEMO"
	replayHill           = "ds.c1.d1"
	replayValley         = "ds.c1.d2"
	replayLake           = "ds.c2.d4"
	replayWildlife       = "dr.w1"
	replayAdmin          = "a1"
	replayOfficer        = "a2"
	replayGuard          = "a3"
	replayDuty           = "b1"
	replayReviewer       = "b2"
	replayDelegate       = "b3"
	replayRangeOfficer   = "b4"
	replayForester       = "b5"
	replayAlternate      = "b6"
	replaySquad          = "b7"
	replayCaller         = "b8"
	replayLiaison        = "b9"
	replayRiverOfficer   = "ba"
)

type operationsReplay struct {
	ctx                     context.Context
	tx                      pgx.Tx
	workspace, slug, prefix string
	stateCode               string
	handler                 http.Handler
	signer                  jose.Signer
	// After an error, skip subsequent requests in the same scenario.
	err error
}

type replayIntake struct{ AlertID, IncidentID string }

func demoReplayCommand(ctx context.Context, cfg config) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cipher, err := readSettingsCipher()
	if err != nil {
		return err
	}
	if cipher == nil {
		// The demo still produces verifiable files without a persistent
		// settings key. Only the public key is needed to read the receipts.
		var demoKey [32]byte
		rand.Read(demoKey[:])
		cipher, err = newSettingsCipher(base64.StdEncoding.EncodeToString(demoKey[:]), "")
		clear(demoKey[:])
		if err != nil {
			return err
		}
	}
	store, err := newLocalAttachments(envOr("FIMRA_ATTACHMENT_DIR", ".form-attachments"))
	if err != nil {
		return err
	}
	pool, err := openPool(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	var day string
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := ensureDemoSeed(ctx, tx); err != nil {
			return err
		}
		day, err = replayOperations(ctx, tx, "demo", operationsSeedPrefix, replayStateCode, store, cipher)
		return err
	})
	if err != nil {
		return err
	}
	if day == "" {
		fmt.Println("The synthetic operations replay is already loaded.")
	} else {
		fmt.Printf("Synthetic operations are ready. Open Demo State and select %s in Daily returns.\n", day)
	}
	return nil
}

func replayOperations(ctx context.Context, tx pgx.Tx, slug, prefix, stateCode string, store attachmentStore, cipher *settingsCipher) (string, error) {
	r := operationsReplay{ctx: ctx, tx: tx, workspace: prefix + "01", slug: slug, prefix: prefix, stateCode: stateCode}
	if _, err := tx.Exec(ctx, "SET LOCAL jit=off"); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, detectionWriteLock); err != nil {
		return "", err
	}
	var completed, occupied bool
	if err := tx.QueryRow(ctx, `SELECT
    EXISTS (SELECT FROM audit_log WHERE workspace_id=w.id AND action='demo.operations.replayed'),
    EXISTS (SELECT FROM alerts WHERE workspace_id=w.id)
    OR EXISTS (SELECT FROM forms WHERE workspace_id=w.id)
    OR EXISTS (SELECT FROM dispatch_rules WHERE workspace_id=w.id)
    OR EXISTS (SELECT FROM daily_return_policies WHERE workspace_id=w.id)
FROM workspaces w JOIN units u ON u.hierarchy_id=$2 AND u.code='S16' AND u.valid_to IS NULL
WHERE w.id=$1 AND w.slug=$3`, r.workspace, prefix+"10", slug).Scan(&completed, &occupied); err != nil {
		return "", fmt.Errorf("replay needs the current synthetic state seed in a fresh development database: %w", err)
	}
	if completed {
		return "", nil
	}
	if occupied {
		return "", errors.New("the synthetic operations replay needs a fresh demo workspace")
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "seed", "demo-state", "operations-roles.sql"))
	if err != nil {
		return "", err
	}
	grants := strings.ReplaceAll(string(body), operationsSeedPrefix, prefix)
	if _, err := tx.Exec(ctx, grants); err != nil {
		return "", err
	}

	// Like the dispatch integration tests, use a private in-process verifier.
	// Tokens never leave this command or share the real API's issuer.
	public, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", err
	}
	defer clear(key)
	r.signer, err = jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: key}, nil)
	if err != nil {
		return "", err
	}
	const issuer = "https://operations-replay.invalid"
	auth := authenticator{issuers: map[string]bool{issuer: true}, verifier: oidc.NewVerifier(issuer,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{public}}, &oidc.Config{ClientID: "open-fimra-api", SupportedSigningAlgs: []string{"EdDSA"}})}
	a := api{reader: tx, logger: slog.Default()}
	withAttachments(store, defaultAttachmentBytes)(&a)
	r.handler = a.handler(auth, devOrigins)

	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		return "", err
	}
	// All observations belong to one closed reporting window. The phone
	// reports later, with its original device time intact.
	cutoff := now.Truncate(time.Minute).Add(-2 * time.Minute)
	r.configureResponse()
	r.terminalCases(cutoff)
	r.reserveTransfer(cutoff)
	largeFire := r.ridgeFire(now, cutoff)
	r.referral(largeFire)
	if r.err != nil {
		return "", r.err
	}
	day, err := r.returnsAndExtract(cutoff, cipher)
	if err != nil {
		return "", err
	}
	if err := checkOperationsReplay(ctx, tx, r.workspace, day, r.stateCode); err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,details)
VALUES($1,'user',$2,'demo.operations.replayed','workspace',$1::uuid::text,jsonb_build_object('day',$3::text))`,
		r.workspace, prefix+replayAdmin, day)
	return day, err
}

func (r *operationsReplay) request(actor, method, path, contentType string, body []byte, want int) []byte {
	if r.err != nil {
		return nil
	}
	token, err := jwt.Signed(r.signer).Claims(jwt.Claims{
		Issuer: "https://operations-replay.invalid", Subject: r.prefix + actor,
		Audience: jwt.Audience{"open-fimra-api"}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}).Serialize()
	if err != nil {
		r.err = err
		return nil
	}
	req := httptest.NewRequestWithContext(r.ctx, method, "/api/v1/workspaces/"+r.slug+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	r.handler.ServeHTTP(response, req)
	if response.Code != want {
		r.err = fmt.Errorf("replay %s %s as %s: HTTP %d, expected %d: %s", method, path, actor, response.Code, want, response.Body)
		return nil
	}
	return response.Body.Bytes()
}

func (r *operationsReplay) call(actor, method, path string, input any, want int, output any) {
	if r.err != nil {
		return
	}
	raw, err := json.Marshal(input)
	if err != nil {
		r.err = err
		return
	}
	got := r.request(actor, method, path, "application/json", raw, want)
	if r.err == nil && output != nil {
		r.err = json.Unmarshal(got, output)
	}
}

func (r *operationsReplay) read(dest *string, query string, args ...any) {
	if r.err == nil {
		r.err = r.tx.QueryRow(r.ctx, query, args...).Scan(dest)
	}
}

func (r *operationsReplay) unit(path string) string {
	var id string
	r.read(&id, `SELECT u.id::text FROM units u JOIN hierarchies h ON u.hierarchy_id=h.id
WHERE h.workspace_id=$1 AND u.current_path=$2::ltree`, r.workspace, path)
	return id
}

func (r *operationsReplay) user(actor string) string {
	var id string
	r.read(&id, "SELECT id::text FROM users WHERE oidc_subject=$1", r.prefix+actor)
	return id
}

func (r *operationsReplay) newID() string {
	var id string
	r.read(&id, "SELECT gen_random_uuid()::text")
	return id
}

func (r *operationsReplay) intake(actor, source, note string, at time.Time, lon, lat float64) replayIntake {
	var receipt replayIntake
	r.call(actor, http.MethodPost, "/fire-intake", intakeRequest{
		ID: r.newID(), Source: source, Note: note, ObservedAt: at, Longitude: &lon, Latitude: &lat,
	}, http.StatusCreated, &receipt)
	return receipt
}

func (r *operationsReplay) step(actor, path string, input stepRequest) {
	r.call(actor, http.MethodPost, path+"/steps", input, http.StatusOK, nil)
}

func (r *operationsReplay) configureResponse() {
	r.call(replayAdmin, http.MethodPut, "/settings/incident-response", responseSettings{Extended: true, Targets: []responseTarget{}}, http.StatusOK, nil)
	var pack packState
	r.call(replayAdmin, http.MethodPost, "/starter-pack", nil, http.StatusOK, &pack)
	// The replay uses the published flash and assessment forms as workflow
	// gates. Keep the other published forms available for later evaluation.
	for _, form := range pack.Forms {
		if form.Form == starterpack.ResponseFlash || form.Form == starterpack.DamageAssessment {
			continue
		}
		for _, step := range form.Assigned {
			r.call(replayAdmin, http.MethodDelete, "/forms/"+form.Form+"/assignments/"+step, nil, http.StatusNoContent, nil)
		}
	}
}

func (r *operationsReplay) terminalCases(cutoff time.Time) {
	at := cutoff.Add(-7 * time.Hour)
	lon, lat := 86.669, 16.500
	noFire := r.intake(replayAdmin, "radio", "Smoke reported near Reed Section.", at, lon, lat)
	alert := "/incidents/" + noFire.IncidentID
	eta := at.Add(45 * time.Minute)
	r.step(replayForester, alert, stepRequest{Step: "acknowledged", ActualAt: &at})
	r.step(replayAdmin, alert, stepRequest{Step: "assigned", Note: "Reed Section forester checks the point.", ActualAt: &at})
	r.step(replayForester, alert, stepRequest{Step: "team_sent", ActualAt: &at, EstimatedAt: &eta})
	arrival := stepRequest{Step: "reached_site", ActualAt: &eta}
	arrival.Location = &struct {
		Longitude *float64 `json:"longitude"`
		Latitude  *float64 `json:"latitude"`
	}{&lon, &lat}
	r.step(replayForester, alert, arrival)
	r.step(replayAdmin, alert, stepRequest{Step: "false_alarm", ActualAt: &eta,
		Note: "No fire found. The forester confirmed the smoke was low cloud."})

	for _, c := range []struct {
		step, note, officer string
		lon, lat            float64
	}{
		{"outside_jurisdiction", "Work barge heat beyond the state boundary. Passed to harbour watch.", replayAdmin, 86.704, 16.503},
		{"not_reachable", "The washed-out bridge blocked access to the Brook Track point.", replayOfficer, 86.231, 16.339},
	} {
		report := r.intake(c.officer, "radio", c.note, at, c.lon, c.lat)
		if c.step == "not_reachable" {
			r.call(replayOfficer, http.MethodPost, "/alerts/"+report.AlertID+"/assignment",
				alertAssignmentRequest{UnitID: r.unit("ds.c1.d1.r1.b01"), Reason: "Send the Brook Track point to the beat guard."},
				http.StatusOK, nil)
			r.call(replayGuard, http.MethodGet, "/alerts/"+report.AlertID, nil, http.StatusOK, nil)
		}
		r.step(c.officer, "/incidents/"+report.IncidentID, stepRequest{Step: c.step, ActualAt: &at, Note: c.note})
	}

	burnAt := at.Add(time.Hour)
	burn := r.intake(replayOfficer, "staff", "Training plot burn seen from Pebble Track.", burnAt, 86.298, 16.419)
	record := r.operationsRecord(replayOfficer, "burn", replayHill, "")
	r.operationsEntry(replayOfficer, record, "prescribed-burn-authorisation", map[string]any{
		"authorisation_reference": "DEMO-BURN-04", "site": "Pebble Track training plot",
		"window_start": at.Add(-24 * time.Hour).Format(time.DateOnly), "window_end": cutoff.Add(24 * time.Hour).Format(time.DateOnly),
	})
	r.call(replayOfficer, http.MethodPut, "/operations-records/"+record+"/alerts/"+burn.AlertID, nil, http.StatusNoContent, nil)
	r.step(replayOfficer, "/incidents/"+burn.IncidentID, stepRequest{
		Step: "controlled_burn", Note: "Matched the approved training plot burn.", ActualAt: &burnAt,
	})
}

func (r *operationsReplay) observation(source, ref, instrument string, at time.Time, lon, lat float64) string {
	if r.err != nil {
		return ""
	}
	_, r.err = runIngest(r.ctx, r.tx, ingestRun{
		Source: source, Product: demoProduct, Kind: runImport, WorkspaceID: &r.workspace, WorkspaceOnly: true,
	},
		func(context.Context) ([]firms.Observation, error) {
			return []firms.Observation{{Satellite: "N", Instrument: instrument, Longitude: lon, Latitude: lat,
				ObservedAt: at, Raw: map[string]string{"externalAlertRef": ref}}}, nil
		})
	var id string
	r.read(&id, `SELECT a.id::text FROM alerts a JOIN detections d
ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
WHERE a.workspace_id=$1 AND d.workspace_id=$1 AND d.source=$2 AND d.raw->>'externalAlertRef'=$3`, r.workspace, source, ref)
	if r.err != nil {
		r.err = fmt.Errorf("load replay alert %s: %w", ref, r.err)
	}
	return id
}

func (r *operationsReplay) reserveTransfer(cutoff time.Time) {
	if r.err != nil {
		return
	}
	change := cutoff.Add(-4 * time.Hour)
	// Install the two fictional survey versions directly. The normal layer
	// publisher requires a separate worker and publishes at the current time.
	_, r.err = r.tx.Exec(r.ctx, `
WITH previous AS (
    UPDATE layer_versions v SET status='retired', retired_at=$2
    FROM layers l WHERE l.id=v.layer_id AND l.workspace_id=$1 AND l.slug='protected-areas'
      AND v.status='published' RETURNING v.id, v.layer_id
), replacement AS (
    INSERT INTO layer_versions(layer_id, version, status, source_file, published_at, effective_from, overlay_kind, overlay_from)
    SELECT layer_id, 2, 'published', 'synthetic reserve survey', $2, $2, 'protected_area', $2
    FROM previous RETURNING id
), collected AS (
    SELECT c.name, ST_Buffer(c.geom, 0.001) AS core, ST_Union(c.geom,b.geom) AS reserve
    FROM previous p JOIN layer_features c ON c.layer_version_id=p.id AND c.code='PA01-C'
    JOIN layer_features b ON b.layer_version_id=p.id AND b.code='PA01-B'
)
INSERT INTO layer_features(layer_version_id,code,name,attributes,geom)
SELECT r.id, z.code, c.name, jsonb_build_object('zone',z.zone), z.geom
FROM replacement r CROSS JOIN collected c
CROSS JOIN LATERAL (VALUES ('PA01-C','core',c.core),('PA01-B','buffer',ST_Difference(c.reserve,c.core))) z(code,zone,geom)`,
		r.workspace, change)
	lake, wildlife := r.unit(replayLake), r.unit(replayWildlife)
	for _, rule := range []dispatchRuleInput{
		{Name: "Earlier reserve core", Priority: 20, EffectiveFrom: change.Add(-24 * time.Hour), EffectiveTo: &change,
			OverlayLayer: "protected-areas", OverlayCode: "PA01-C", OverlayZone: "core", Lead: lake},
		{Name: "Revised reserve core", Priority: 20, EffectiveFrom: change,
			OverlayLayer: "protected-areas", OverlayCode: "PA01-C", OverlayZone: "core", Lead: wildlife},
		{Name: "Reserve buffer", Priority: 10, EffectiveFrom: change.Add(-24 * time.Hour),
			OverlayLayer: "protected-areas", OverlayCode: "PA01-B", OverlayZone: "buffer", Lead: lake, Supporting: []string{wildlife}},
	} {
		r.call(replayAdmin, http.MethodPost, "/settings/dispatch-rules", rule, http.StatusCreated, nil)
	}
	const reserveLon, reserveLat = 86.591907, 16.338721
	r.observation(demoSource, "RESERVE-NEW", "VIIRS", change.Add(40*time.Minute), reserveLon, reserveLat)
	late := r.observation(demoSource, "RESERVE-OLD", "VIIRS", change.Add(-40*time.Minute), reserveLon, reserveLat)
	r.call(replayAdmin, http.MethodPost, "/alerts/"+late+"/assignment",
		alertAssignmentRequest{UnitID: wildlife, Reason: "Transfer the earlier reserve alert to the wildlife crew under the new boundary."},
		http.StatusOK, nil)
	r.call(replaySquad, http.MethodGet, "/alerts/"+late, nil, http.StatusOK, nil)
}

func (r *operationsReplay) ridgeFire(now, cutoff time.Time) replayIntake {
	const westLon, eastLon, lat = 86.311, 86.321, 16.4191
	reported := cutoff.Add(-5 * time.Hour)
	hill, valley := r.unit(replayHill), r.unit(replayValley)
	r.call(replayAdmin, http.MethodPost, "/settings/duty", dutyShiftInput{
		Name: "West control room outgoing watch", Kind: "control_room",
		StartsAt: reported.Add(-2 * time.Hour), EndsAt: now.Add(-3 * time.Hour),
		Members: []string{r.user(replayOfficer)}, Units: []string{hill, valley},
	}, http.StatusCreated, nil)
	radio := r.intake(replayOfficer, "radio", "Ridge fire reported by radio from West Lookout.", reported, westLon, lat)
	r.intake(replayCaller, "public", "Smoke and flame seen from the committee footpath.", reported.Add(10*time.Minute), westLon, lat)
	r.observation(demoSource, "RIDGE-VIIRS-1", "VIIRS", reported.Add(time.Hour), westLon, lat)
	r.observation(demoSource, "RIDGE-VIIRS-2", "VIIRS", reported.Add(2*time.Hour), eastLon, lat)
	fsi := r.observation(responseDemoSource, "DEMO-FSI-RIDGE-37", "VIIRS", reported.Add(150*time.Minute), westLon, lat)

	casePath, phoneAlert := "/incidents/"+radio.IncidentID, "/alerts/"+radio.AlertID
	ack := reported.Add(15 * time.Minute)
	r.step(replayOfficer, casePath, stepRequest{Step: "acknowledged", ActualAt: &ack})
	r.call(replayOfficer, http.MethodGet, "/alerts/"+fsi, nil, http.StatusOK, nil)
	r.call(replayDuty, http.MethodGet, phoneAlert, nil, http.StatusNotFound, nil)
	r.call(replayAdmin, http.MethodPost, "/settings/duty", dutyShiftInput{
		Name: "West control room incoming watch", Kind: "control_room",
		StartsAt: now.Add(-3 * time.Hour), EndsAt: now.Add(5 * time.Hour),
		Members: []string{r.user(replayDuty)}, Units: []string{hill, valley},
	}, http.StatusCreated, nil)
	r.call(replayAdmin, http.MethodPost, "/settings/duty", dutyShiftInput{
		Name: "Ridge seasonal squad", Kind: "squad",
		StartsAt: now.Add(-3 * time.Hour), EndsAt: now.Add(5 * time.Hour),
		Members: []string{r.user(replaySquad)}, Units: []string{valley},
	}, http.StatusCreated, nil)
	r.call(replayDuty, http.MethodPost, phoneAlert+"/support",
		alertAssignmentRequest{UnitID: valley, Reason: "Valley crew needed to hold the east side of Ridge Fire."}, http.StatusOK, nil)
	r.call(replayDelegate, http.MethodGet, casePath, nil, http.StatusOK, nil)
	r.call(replaySquad, http.MethodGet, casePath, nil, http.StatusOK, nil)
	r.call(replayLiaison, http.MethodGet, casePath, nil, http.StatusOK, nil)
	r.call(replayLiaison, http.MethodPost, phoneAlert+"/submissions", nil, http.StatusForbidden, nil)
	r.call(replayCaller, http.MethodPost, phoneAlert+"/steps", nil, http.StatusForbidden, nil)

	dispatch := now.Add(-170 * time.Minute)
	arrival := now.Add(-140 * time.Minute)
	r.step(replayDuty, casePath, stepRequest{Step: "assigned", Note: "Incoming watch sends the Hill crew and calls Valley for aid.", ActualAt: &dispatch})
	r.step(replayDuty, casePath, stepRequest{Step: "team_sent", ActualAt: &dispatch, EstimatedAt: &arrival})
	r.step(replayDuty, casePath, stepRequest{Step: "reached_site", Reason: "Crews confirmed arrival by radio.", ActualAt: &arrival})
	flashAt := now.Add(-130 * time.Minute)
	r.step(replayDuty, casePath, stepRequest{Step: "flash_update", ActualAt: &flashAt,
		Response: &responseNote{Status: "Large surface fire across Hill and Valley divisions.",
			EstimatedExtent: "Smoke hides the far edge.", Threats: "Fire approaching the committee footpath.", HelpNeeded: "A second crew on the east side."}})
	r.syncFlash(phoneAlert, flashAt, westLon, lat)
	fsiFlash := r.saveFlash("/alerts/"+fsi, replayAlternate, flashAt, westLon, lat, "")
	r.approve(replayReviewer, fsiFlash, "approved", "")
	updateAt := now.Add(-110 * time.Minute)
	r.step(replayRangeOfficer, casePath, stepRequest{Step: "response_update", ActualAt: &updateAt,
		Response: &responseNote{Status: "The Valley crew is holding the east side."}})
	endAt := now.Add(-90 * time.Minute)
	r.step(replayDuty, casePath, stepRequest{Step: "controlled", ActualAt: &endAt, Reason: "Both crews confirmed the edge by radio."})
	r.step(replayDuty, casePath, stepRequest{Step: "extinguished", ActualAt: &endAt, Reason: "Both crews checked for flames."})
	for _, step := range []string{"watch", "first_report", "final_report"} {
		r.step(replayDuty, casePath, stepRequest{Step: step, ActualAt: &endAt})
	}
	r.assessRidge(phoneAlert, endAt)
	r.call(replayReviewer, http.MethodPost, "/alerts/"+fsi+"/source-feedback",
		feedbackRequest{VerificationID: fsiFlash}, http.StatusOK, nil)
	r.step(replayReviewer, casePath, stepRequest{Step: "verified"})
	r.step(replayReviewer, casePath, stepRequest{Step: "closed"})
	return radio
}

func (r *operationsReplay) syncFlash(alertPath string, captured time.Time, lon, lat float64) {
	// Use the same attachment and submission requests as the phone outbox,
	// including the UUID and the original capture time on retry.
	photo := r.newID()
	data, err := syntheticRidgePhoto()
	if err != nil && r.err == nil {
		r.err = err
	}
	path := alertPath + "/attachments/" + photo + "?name=demo-ridge.png"
	r.request(replayAlternate, http.MethodPut, path, "image/png", data, http.StatusCreated)
	r.request(replayAlternate, http.MethodPut, path, "image/png", data, http.StatusOK)
	r.saveFlash(alertPath, replayAlternate, captured, lon, lat, photo)
}

func syntheticRidgePhoto() ([]byte, error) {
	// A small painted ridge stands in for a field photo. It has no camera
	// metadata or picture of a real site.
	img := image.NewRGBA(image.Rect(0, 0, 160, 96))
	for x := range img.Bounds().Dx() {
		horizon := 40 + ((x/18)%4)*3 + (x%18)/3
		for y := range img.Bounds().Dy() {
			shade := color.RGBA{R: 188, G: 199, B: 201, A: 255}
			if y >= horizon {
				shade = color.RGBA{R: 61 + uint8((x+y)%17), G: 64, B: 45, A: 255}
			}
			if y > 64 && x > 64 && x < 113 {
				shade = color.RGBA{R: 49, G: 42, B: 37, A: 255}
			}
			img.SetRGBA(x, y, shade)
		}
	}
	var out bytes.Buffer
	err := png.Encode(&out, img)
	return out.Bytes(), err
}

func (r *operationsReplay) saveFlash(path, actor string, captured time.Time, lon, lat float64, photo string) string {
	point := map[string]any{"type": "Point", "coordinates": []float64{lon, lat}}
	values := map[string]any{
		"checked_at": captured, "fire_status": "Active surface fire on the ridge.", "site_location": point,
		"fire_seen": "yes", "land_class": "forest", "fire_type": "surface", "fire_cause": "unknown",
	}
	if photo != "" {
		values["site_photo"] = []string{photo}
	}
	id := r.newID()
	input := map[string]any{
		"id": id, "form": starterpack.ResponseFlash, "version": 1, "step": "flash_update", "values": values,
		"deviceTime": captured, "location": map[string]float64{"longitude": lon, "latitude": lat},
	}
	r.call(actor, http.MethodPost, path+"/submissions", input, http.StatusCreated, nil)
	if photo != "" {
		r.call(actor, http.MethodPost, path+"/submissions", input, http.StatusOK, nil)
	}
	return id
}

func (r *operationsReplay) assessRidge(path string, assessed time.Time) {
	provisional, revised := r.newID(), r.newID()
	assessment := func(id string, values map[string]any, parent *string) {
		raw, _ := json.Marshal(values)
		r.call(replayAlternate, http.MethodPost, path+"/submissions",
			submissionRequest{ID: id, Form: starterpack.DamageAssessment, Version: 1, Step: "final_report",
				Values: raw, ProvisionalSubmissionID: parent}, http.StatusCreated, nil)
	}
	assessment(provisional, map[string]any{"assessed_at": assessed, "measurement": "estimated", "area_ha": 84.0,
		"method": "Estimated from the access track.", "uncertainty": "Smoke hid the far side."}, nil)
	r.approve(replayReviewer, provisional, "returned", "Check the east side and record the walked perimeter.")
	assessment(revised, map[string]any{"assessed_at": assessed.Add(30 * time.Minute), "measurement": "surveyed", "area_ha": 62.0,
		"method": "Walked the full perimeter with the Valley crew.", "uncertainty": "The full edge was visible.",
		"state_code": r.stateCode, "state_area_ha": 62.0, "state_land_class": "forest",
		"state_area_method": "field", "state_area_status": "final"}, &provisional)
	r.approve(replayReviewer, revised, "approved", "")
}

func (r *operationsReplay) approve(actor, id, decision, note string) {
	r.call(actor, http.MethodPost, "/submissions/"+id+"/approval",
		approvalRequest{Decision: decision, Note: note}, http.StatusOK, nil)
}

func (r *operationsReplay) operationsRecord(actor, kind, unit, incident string) string {
	id := r.newID()
	r.call(actor, http.MethodPost, "/operations-records",
		operationsRequest{ID: id, Kind: kind, Unit: r.unit(unit), IncidentID: incident}, http.StatusCreated, nil)
	return id
}

func (r *operationsReplay) operationsEntry(actor, record, form string, values map[string]any) string {
	id := r.newID()
	raw, _ := json.Marshal(values)
	r.call(actor, http.MethodPost, "/operations-records/"+record+"/submissions",
		operationsSubmissionRequest{submissionRequest: submissionRequest{ID: id, Form: form, Version: 1, Values: raw}},
		http.StatusCreated, nil)
	r.approve(actor, id, "approved", "")
	return id
}

func (r *operationsReplay) referral(radio replayIntake) {
	r.call(replayAdmin, http.MethodPut, "/referral-grants",
		map[string]string{"kind": "offence", "unit": r.unit(replayHill), "user": r.user(replayOfficer)}, http.StatusNoContent, nil)
	id := r.operationsRecord(replayOfficer, "offence", replayHill, radio.IncidentID)
	entry := r.operationsEntry(replayOfficer, id, "offence-referral", map[string]any{
		"case_reference": "DEMO-ENQUIRY-37", "evidence_reference": "DEMO-RIDGE-REGISTER",
		"public_summary": "The district has an approved summary. A separate enquiry is reviewing the ridge fire.",
	})
	path := "/operations-records/" + id
	full := r.request(replayOfficer, http.MethodGet, "/submissions/"+entry, "", nil, http.StatusOK)
	r.call(replayReviewer, http.MethodGet, "/submissions/"+entry, nil, http.StatusNotFound, nil)
	summary := r.request(replayReviewer, http.MethodGet, path, "", nil, http.StatusOK)
	if r.err == nil && (!bytes.Contains(full, []byte("DEMO-RIDGE-REGISTER")) ||
		!bytes.Contains(summary, []byte("approved summary")) || bytes.Contains(summary, []byte("DEMO-RIDGE-REGISTER"))) {
		r.err = errors.New("the restricted referral did not produce the expected full and summary views")
	}
}

func (r *operationsReplay) returnsAndExtract(cutoff time.Time, cipher *settingsCipher) (string, error) {
	location, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return "", err
	}
	local := cutoff.In(location)
	day := local.Format(time.DateOnly)
	admin := r.prefix + replayAdmin
	// Daily returns and extracts use pool transactions in HTTP so a read can
	// request a consistent snapshot. Reuse their writers in the replay's
	// single transaction.
	if _, err = r.tx.Exec(r.ctx, `INSERT INTO daily_return_policies(workspace_id,hierarchy_id,depth,season,time_zone,cutoff,approver_role)
VALUES($1,$2,2,daterange($3::date,$3::date,'[]'),'Asia/Kolkata',$4::text::time,'state_admin')`,
		r.workspace, r.prefix+"10", day, local.Format("15:04")); err != nil {
		return "", err
	}
	window, err := loadDailyWindow(r.ctx, r.tx, admin, r.slug, day)
	if err != nil {
		return "", err
	}
	for _, item := range []struct {
		path, author, note string
		nilReturn          bool
		received           int
	}{
		{replayHill, replayOfficer, "Ridge fire crossed the division line. The field report was synced after cutoff.", false, 1},
		{replayValley, replayDelegate, "The Valley crew helped at the ridge fire.", false, 0},
		{"ds.c2.d3", replayRiverOfficer, "No fire activity to report in this division.", true, 0},
	} {
		if err := r.approveReturn(window, item.path, item.author, item.note, item.nilReturn, item.received); err != nil {
			return "", err
		}
	}

	if _, err := r.tx.Exec(r.ctx, `INSERT INTO state_extract_authorities VALUES($1,$2,'demo-forest-units')`,
		r.workspace, r.stateCode); err != nil {
		return "", err
	}
	if _, err := generateStateSigningKey(r.ctx, r.tx, cipher, r.workspace, admin); err != nil {
		return "", err
	}
	authority, err := readStateAuthority(r.ctx, r.tx, r.workspace)
	if err != nil {
		return "", err
	}
	preview, revision, chain, err := prepareStateExtract(r.ctx, r.tx, r.workspace, stateExtractRequest{From: day, To: day}, authority)
	if err != nil {
		return "", err
	}
	_, err = storeStateExtract(r.ctx, r.tx, cipher, r.workspace, admin, chain, revision, preview.Documents)
	return day, err
}

func (r *operationsReplay) approveReturn(window dailyWindow, path, author, note string, nilReturn bool, received int) error {
	unit := dailyExpectedUnit{Path: path}
	subject := r.prefix + author
	var name string
	err := r.tx.QueryRow(r.ctx, `SELECT u.code,u.name,u.layer_version_id::text,actor.display_name
FROM daily_return_units($1,$2,$3) u JOIN permanent_alert_grants g
 ON g.workspace_id=$4 AND g.hierarchy_id=$1 AND g.unit_path=u.path
JOIN users actor ON actor.id=g.user_id
WHERE u.path=$5::ltree AND g.oidc_subject=$6`,
		window.HierarchyID, window.Depth, window.Due, r.workspace, path, subject).Scan(&unit.Code, &unit.Name, &unit.BoundaryVersion, &name)
	if err != nil {
		return fmt.Errorf("find producing officer for %s: %w", path, err)
	}
	a := api{}
	_, err = a.changeDailyReturn(r.ctx, r.tx, r.slug, subject, name, unit, window,
		dailyRequest{Action: "submit", Nil: nilReturn, Remarks: note, Received: received}, dailyRevision{}, nil, true, false)
	if err != nil {
		return err
	}
	history, err := readDailyHistory(r.ctx, r.tx, window, path)
	if err != nil {
		return err
	}
	admin := r.prefix + replayAdmin
	r.read(&name, "SELECT display_name FROM users WHERE oidc_subject=$1", admin)
	if r.err != nil {
		return r.err
	}
	_, err = a.changeDailyReturn(r.ctx, r.tx, r.slug, admin, name, unit, window,
		dailyRequest{Action: "approved", Base: history[0].ID}, history[0], history, false, true)
	return err
}
