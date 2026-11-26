package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	extract "github.com/santhoshbalaji/open-fimra/packages/state-extract"
)

func TestNationalIntegration(t *testing.T) {
	if os.Getenv("FIMRA_NATIONAL_INTEGRATION") != "1" {
		t.Skip("Run make national-test-integration.")
	}
	ctx := t.Context()
	pool, err := openPool(ctx, loadConfig().databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	slug := fmt.Sprintf("national-test-%x", time.Now().UnixNano())
	localSlug := "state-" + slug
	north := "XS-N" + strings.ToUpper(strings.TrimPrefix(slug, "national-test-"))
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := seedNationalWorkspace(ctx, tx, slug); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE national_states s SET state_code=$2 FROM workspaces w
WHERE w.id=s.workspace_id AND w.slug=$1 AND s.state_code='XS-NORTH'`, slug, north)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	handler, token := nationalTestHandler(t, pool)
	const nodal = "state.nodal"
	base := "/api/v1/workspaces/" + slug + "/national"
	call := func(user, method, path string, input any, want int, output any) []byte {
		t.Helper()
		if !strings.HasPrefix(path, "/api/") {
			path = base + path
		}
		var body []byte
		if raw, ok := input.([]byte); ok {
			body = raw
		} else if input != nil {
			body, err = json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if user != "" {
			r.Header.Set("Authorization", "Bearer "+token(user))
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Fatalf("%s %s as %s: got %d, want %d: %s", method, path, user, rec.Code, want, rec.Body.String())
		}
		if output != nil {
			if err := json.Unmarshal(rec.Body.Bytes(), output); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Body.Bytes()
	}

	var me meResponse
	call("public.viewer", "GET", "/api/v1/me", nil, 200, &me)
	found := false
	for _, a := range me.RoleAssignments {
		found = found || (a.Workspace.Slug == slug && a.Workspace.Kind == "national" && a.Role == "national_public")
	}
	if !found {
		t.Fatal("public profile did not include the national workspace")
	}
	call("public.viewer", "GET", "/api/v1/workspaces/"+slug, nil, 403, nil)
	call("", "GET", "/public", nil, 401, nil)
	call("no.role", "GET", "/", nil, 403, nil)
	call(nodal, "GET", "/", nil, 200, nil)

	pub, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(private)
	northPub, northPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(northPrivate)
	for _, key := range []struct {
		state string
		pub   ed25519.PublicKey
	}{{"XS-DEMO", pub}, {north, northPub}} {
		_, err := pool.Exec(ctx, `INSERT INTO national_state_keys(state_id,key_id,public_key)
SELECT s.id,$3,$4 FROM national_states s JOIN workspaces w ON w.id=s.workspace_id WHERE w.slug=$1 AND s.state_code=$2`,
			slug, key.state, extract.KeyID(key.pub), []byte(key.pub))
		if err != nil {
			t.Fatal("could not register fictional signing authority")
		}
	}
	getDoc := func(example string) extract.Document {
		t.Helper()
		doc, err := nationalExample(example)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	sign := func(doc extract.Document, key ed25519.PrivateKey) []byte {
		t.Helper()
		signed, err := extract.Sign(doc, key)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}

	original, correction := getDoc("incident"), getDoc("incident-correction")
	before, after := sign(original, private), sign(correction, private)
	tampered := bytes.Replace(before, []byte(`"records":0`), []byte(`"records":1`), 1)
	call(nodal, "POST", "/upload", tampered, 422, nil)
	call(nodal, "POST", "/upload", sign(original, northPrivate), 422, nil)
	call("national.ministry", "POST", "/push", before, 403, nil)
	call(nodal, "POST", "/push", after, 409, nil)
	var first, replay extract.Receipt
	call(nodal, "POST", "/upload", before, 201, &first)
	call(nodal, "POST", "/push", before, 200, &replay)
	if first != replay || first.ExtractID != original.ID {
		t.Fatal("retry did not return the stored receipt")
	}
	call(nodal, "POST", "/push", sign(getDoc("preparedness-original"), private), 201, nil)
	var ministry []json.RawMessage
	call("national.ministry", "GET", "/ministry", nil, 200, &ministry)
	if len(ministry) != 0 {
		t.Fatal("state figures were shared before approval")
	}
	statePath := "/states/XS-DEMO"
	window := nationalWindow{original.Window.From, original.Window.To}
	windowValues := map[string]any{"from": window.From, "to": window.To, "ministry": true, "survey": true, "preparedness": true, "feedback": true}
	call(nodal, "PUT", statePath+"/sharing", windowValues, 200, nil)
	approval := map[string]any{"from": window.From, "to": window.To, "extractId": original.ID}
	call("public.viewer", "GET", "/public", nil, 200, &ministry)
	if len(ministry) != 0 {
		t.Fatal("unapproved figures were public")
	}
	call("public.viewer", "PUT", statePath+"/publication", approval, 403, nil)
	call(nodal, "PUT", statePath+"/publication", approval, 200, nil)
	call(nodal, "POST", "/push", after, 201, &first)
	call(nodal, "POST", "/push", after, 200, &replay)
	if first != replay {
		t.Fatal("correction retry did not return the stored receipt")
	}
	call(nodal, "POST", "/push", sign(getDoc("preparedness-correction"), private), 201, nil)
	call(nodal, "PUT", statePath+"/publication", approval, 409, nil)
	call("public.viewer", "GET", "/public", nil, 200, &ministry)
	if len(ministry) != 0 {
		t.Fatal("old public release survived a correction")
	}
	approval["extractId"] = correction.ID
	call(nodal, "PUT", statePath+"/publication", approval, 200, nil)

	docNorth := getDoc("incident-north")
	docNorth.Authority.StateCode = north
	docNorth.ID = fmt.Sprintf("acc94e2b-b93b-4fb7-a192-%012x", uint64(time.Now().UnixNano())&0xffffffffffff)
	northRaw := sign(docNorth, northPrivate)
	call(nodal, "POST", "/upload", northRaw, 403, nil)
	seedNationalLocalExport(t, pool, localSlug, docNorth, northRaw, northPub)
	push := "/api/v1/workspaces/" + localSlug + "/state-extracts/" + docNorth.ID + "/incident/push"
	call(nodal, "POST", push, map[string]string{"destination": slug}, 403, nil)
	call("north.nodal", "POST", push, map[string]string{"destination": slug}, 201, &replay)
	if replay.ExtractID != docNorth.ID {
		t.Fatal("local state push did not return its receipt")
	}
	call("north.nodal", "PUT", "/states/"+north+"/sharing", windowValues, 200, nil)
	approval["extractId"] = docNorth.ID
	call("north.nodal", "PUT", "/states/"+north+"/publication", approval, 200, nil)

	var survey []struct {
		State       string
		StateReturn *struct {
			Source     string
			Coverage   struct{ Expected, Reported int }
			Detections map[string]int
		}
		Satellite *struct {
			Source                         string
			SamePeriod                     bool
			Expected, Reported, Detections int
		}
		Feedback *struct{ Accepted int }
	}
	count := int64(84)
	satellite := nationalSatelliteInput{window, "Fictional satellite provider", "Fictional detections", original.Window.StartsAt, original.Window.EndsAt, 6, 5, &count}
	call("national.survey", "PUT", "/states/"+north+"/satellite", satellite, 403, nil)
	call("national.survey", "PUT", statePath+"/satellite", satellite, 200, nil)
	call(nodal, "PUT", statePath+"/feedback", map[string]any{"from": window.From, "to": window.To, "prepared": 1, "submitted": 1, "accepted": 2, "rejected": 1}, 200, nil)
	body := call("national.survey", "GET", "/survey", nil, 200, &survey)
	if len(survey) != 1 || survey[0].State != "XS-DEMO" || survey[0].StateReturn == nil ||
		survey[0].StateReturn.Source != original.Source || survey[0].StateReturn.Coverage.Reported != 2 ||
		survey[0].StateReturn.Detections["records"] != 2 || survey[0].Satellite == nil || !survey[0].Satellite.SamePeriod ||
		survey[0].Satellite.Expected != 6 || survey[0].Satellite.Reported != 5 || survey[0].Satellite.Detections != 84 ||
		survey[0].Feedback == nil || survey[0].Feedback.Accepted != 2 || bytes.Contains(body, []byte(`"totals"`)) {
		t.Fatalf("wrong comparison sources, figures or scope: %s", body)
	}
	type row struct {
		State       string
		StateReturn struct {
			Revision int
			Totals   extract.Totals
		}
		Preparedness json.RawMessage
	}
	var ministryRows []row
	call("national.ministry", "GET", "/ministry", nil, 200, &ministryRows)
	if len(ministryRows) != 2 || ministryRows[0].State != "XS-DEMO" || ministryRows[0].StateReturn.Revision != 2 ||
		ministryRows[0].StateReturn.Totals.Detections["records"] != 2 || len(ministryRows[0].Preparedness) == 0 {
		t.Fatal("ministry received overlapping revisions or incorrect state figures")
	}
	windowValues["preparedness"] = false
	call(nodal, "PUT", statePath+"/sharing", windowValues, 200, nil)
	call("national.ministry", "GET", "/ministry", nil, 200, &ministryRows)
	if string(ministryRows[0].Preparedness) != "null" || ministryRows[0].StateReturn.Revision != 2 {
		t.Fatal("ministry read a scheme return that was independently withdrawn")
	}
	removeNorthGrant := func(role string) {
		t.Helper()
		_, err := pool.Exec(ctx, `DELETE FROM role_assignments a USING national_states s, workspaces w
WHERE w.slug=$1 AND s.workspace_id=w.id AND (a.workspace_id,a.hierarchy_id,a.unit_path)=(w.id,s.hierarchy_id,s.unit_path)
  AND s.state_code=$2 AND a.role_slug=$3`, slug, north, role)
		if err != nil {
			t.Fatal(err)
		}
	}
	removeNorthGrant("national_ministry")
	call("national.ministry", "GET", "/ministry", nil, 200, &ministryRows)
	if len(ministryRows) != 1 || ministryRows[0].State != "XS-DEMO" {
		t.Fatal("ministry results ignored its state grant")
	}

	call("national.ministry", "GET", "/survey", nil, 403, nil)
	call("national.survey", "GET", "/ministry", nil, 403, nil)
	call(nodal, "GET", "/submissions", nil, 200, &ministry)
	if len(ministry) != 1 || bytes.Contains(ministry[0], []byte(north)) {
		t.Fatal("state nodal officer received another state's submissions")
	}
	var public []struct {
		State          string
		ConfirmedFires *int
	}
	body = call("public.viewer", "GET", "/public", nil, 200, &public)
	if len(public) != 2 || public[0].State != "XS-DEMO" || public[0].ConfirmedFires != nil || public[1].State != north ||
		public[1].ConfirmedFires == nil || *public[1].ConfirmedFires != 7 ||
		bytes.Contains(body, []byte(`"totals"`)) || bytes.Contains(body, []byte(`boundaryVersion`)) ||
		bytes.Contains(body, []byte(`latitude`)) {
		t.Fatalf("public geography or suppression was incorrect: %s", body)
	}
	removeNorthGrant("national_public")
	call("public.viewer", "GET", "/public", nil, 200, &public)
	if len(public) != 1 || public[0].State != "XS-DEMO" {
		t.Fatal("public results ignored the state grant")
	}

	for _, view := range []string{"ministry", "survey", "submissions", "incidents"} {
		call("public.viewer", "GET", "/"+view, nil, 403, nil)
	}

	longitude, latitude, precision := 82.74321, 20.23456, 1
	update := nationalIncidentInput{
		SharedEventID: "cdc00000-0000-4000-8000-000000000001", Status: "active", Severity: "major",
		OccurredAt: original.Window.StartsAt, NextUpdateAt: original.Window.EndsAt,
		PrecisionDecimals: &precision, Longitude: &longitude, Latitude: &latitude,
		Threats: "Fictional ridge", AidRequested: "One crew", Shared: true,
	}
	call(nodal, "PUT", "/states/"+north+"/incidents/NORTH-1", update, 403, nil)
	call(nodal, "PUT", statePath+"/incidents/DEMO-1", update, 200, nil)
	call("north.nodal", "PUT", "/states/"+north+"/incidents/NORTH-1", update, 200, nil)
	update.SharedEventID, update.Shared = "cdc00000-0000-4000-8000-000000000002", false
	call(nodal, "PUT", statePath+"/incidents/DEMO-DRAFT", update, 200, nil)
	call(nodal, "PUT", statePath+"/incidents/DEMO-1", update, 409, nil)

	var events struct {
		EventCount int
		Updates    []struct {
			State, IncidentRef  string
			Longitude, Latitude float64
			PrecisionDecimals   int
		}
	}
	call("national.disaster", "GET", "/incidents", nil, 200, &events)
	if events.EventCount != 1 || len(events.Updates) != 2 || events.Updates[0].IncidentRef != "DEMO-1" ||
		events.Updates[0].Longitude != 83 || events.Updates[0].Latitude != 20 || events.Updates[0].PrecisionDecimals != 0 ||
		events.Updates[1].Latitude != 20.2 || events.Updates[1].PrecisionDecimals != 1 {
		t.Fatalf("cross-border incident was duplicated, overprecise or leaked a draft: %+v", events)
	}
	removeNorthGrant("national_disaster")
	call("national.disaster", "GET", "/incidents", nil, 200, &events)
	if events.EventCount != 1 || len(events.Updates) != 1 || events.Updates[0].State != "XS-DEMO" {
		t.Fatal("restricted channel ignored the disaster officer's state grant")
	}

	call(nodal, "GET", "/incidents", nil, 200, &events)
	if events.EventCount != 2 || len(events.Updates) != 2 || events.Updates[0].State != "XS-DEMO" || events.Updates[1].State != "XS-DEMO" {
		t.Fatalf("nodal incident access was incorrect: %+v", events)
	}
	var reads, denied, writes, refusedWrites int
	err = pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE a.action='national.incident_read'),
count(*) FILTER (WHERE a.action='national.incident_list' AND a.details->>'allowed'='false'),
count(*) FILTER (WHERE a.action='national.incident_update'),
count(*) FILTER (WHERE a.action='national.incident_update_refused')
FROM audit_log a JOIN workspaces w ON w.id=a.workspace_id WHERE w.slug=$1`, slug).Scan(&reads, &denied, &writes, &refusedWrites)
	if err != nil || reads != 5 || denied != 1 || writes != 3 || refusedWrites != 2 {
		t.Fatalf("restricted access audit: %d reads, %d denied, %d writes, %d refused writes: %v", reads, denied, writes, refusedWrites, err)
	}
}

func nationalTestHandler(t *testing.T, pool *pgxpool.Pool) (http.Handler, func(string) string) {
	t.Helper()
	key, signer := testSigner(t)
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			base := "http://" + r.Host
			writeJSON(w, 200, map[string]any{"issuer": base, "jwks_uri": base + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			writeJSON(w, 200, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
				Key: &key.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig",
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(issuer.Close)
	provider, err := discoverProvider(t.Context(), issuer.URL)
	if err != nil {
		t.Fatal(err)
	}
	const audience = "open-fimra-api"
	handler := newHandler(pool, newAuthenticator(provider, issuer.URL, audience), slog.New(slog.NewTextHandler(io.Discard, nil)), "", t.TempDir(), nil)
	subjects := map[string]string{
		"national.ministry": "d1000000-0000-4000-8000-0000000000a1",
		"national.survey":   "d1000000-0000-4000-8000-0000000000a2",
		"national.disaster": "d1000000-0000-4000-8000-0000000000a3",
		"state.nodal":       nationalDemoNodal,
		"public.viewer":     "d1000000-0000-4000-8000-0000000000a5",
		"north.nodal":       nationalDemoNorth,
		"no.role":           "synthetic-no-portal-grant",
	}
	return handler, func(user string) string {
		t.Helper()
		claims := jwt.Claims{Issuer: issuer.URL, Subject: subjects[user], Audience: jwt.Audience{audience}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Minute))}
		token, err := jwt.Signed(signer).Claims(claims).Claims(map[string]any{"preferred_username": user, "name": "Fictional officer"}).Serialize()
		if err != nil {
			t.Fatal("could not sign a test token")
		}
		return token
	}
}

func seedNationalLocalExport(t *testing.T, pool *pgxpool.Pool, slug string, doc extract.Document, raw []byte, key ed25519.PublicKey) {
	t.Helper()
	ctx := t.Context()
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var workspace, hierarchy string
		if err := tx.QueryRow(ctx, `WITH w AS (INSERT INTO workspaces(slug,name) VALUES($1,'Fictional source') RETURNING id),
h AS (INSERT INTO hierarchies(workspace_id,slug,name,kind) SELECT id,'territorial','Territorial','territorial' FROM w RETURNING id)
SELECT w.id::text,h.id::text FROM w,h`, slug).Scan(&workspace, &hierarchy); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hierarchy_levels(hierarchy_id,depth,slug,labels) VALUES($1,0,'state','{"en":"State"}');
INSERT INTO units(hierarchy_id,depth,code,name,path) VALUES($1,0,$2,'Fictional source','source');
INSERT INTO role_assignments(user_id,workspace_id,hierarchy_id,unit_path,role_slug)
SELECT id,$3,$1,'source','state_admin' FROM users WHERE oidc_subject=$4;
INSERT INTO state_extract_authorities(workspace_id,state_code,unit_code_system) VALUES($3,$2,$5)`,
			pgx.QueryExecModeSimpleProtocol, hierarchy, doc.Authority.StateCode, workspace, nationalDemoNorth, doc.Authority.UnitCodeSystem); err != nil {
			return err
		}
		var window int64
		if err := tx.QueryRow(ctx, `INSERT INTO state_extract_windows(workspace_id,days) VALUES($1,daterange($2::date,$3::date,'[]')) RETURNING id`,
			workspace, doc.Window.From, doc.Window.To).Scan(&window); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO exports(id,workspace_id,requested_by,kind,format,filters,requester_roles,row_count,expires_at,state_window,state_revision)
VALUES($1,$2,$3,'state_extract','json','{}','[]',2,'infinity',$4,1)`, doc.ID, workspace, nationalDemoNorth, window); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO export_rows(export_id,ordinal,workspace_id,state_document) VALUES($1,1,$2,$3),($1,2,$2,$3)`,
			doc.ID, workspace, raw); err != nil {
			return err
		}
		receipt, err := extract.Validate(raw, key, time.Now())
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE exports SET status='ready',finished_at=now(),state_receipt=$2 WHERE id=$1`, doc.ID, []extract.Receipt{receipt})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
