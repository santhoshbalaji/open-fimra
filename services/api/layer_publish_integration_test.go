package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

type previewChange struct {
	DetectionID string   `json:"detectionId"`
	OldBeats    []string `json:"oldBeats"`
	NewBeats    []string `json:"newBeats"`
}

type testPreview struct {
	Scanned           int
	Blocked           string
	BrokenGrants      []brokenGrant
	GrantConfirmation string
	Previous          []struct {
		LayerName string
		Version   int
	}
	Changes map[string]struct {
		Count int
		Items []previewChange
	}
}

func TestLayerPublishIntegration(t *testing.T) {
	if os.Getenv("FIMRA_GEO_INTEGRATION") != "1" {
		t.Skip("Run make geo-test-integration with the worker and migrated development stack running.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cfg := loadConfig()
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%08x", rand.Uint32())
	prefix := suffix + seedPrefix[8:]
	slug, source := "publish-test-"+suffix, "publish_"+suffix
	offset := seedPublishingWorkspace(t, ctx, pool, prefix, slug, cfg.uploadDir)

	logger := slog.Default()
	jobs, err := newJobClient(pool, logger, nil, nil, pushSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := jobs.Stop(stop); err != nil {
			t.Error(err)
		}
	}()
	provider, err := discoverProvider(ctx, cfg.issuer)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandler(pool, newAuthenticator(provider, cfg.issuer, cfg.audience),
		logger, devOrigins, cfg.uploadDir, jobs, withExports(cfg.exportDir, cfg.exportTTL)))
	defer server.Close()
	client := &http.Client{Timeout: 15 * time.Second}
	admin := demoToken(t, client, provider.Endpoint().TokenURL, "state.admin")
	guard := demoToken(t, client, provider.Endpoint().TokenURL, "beat.guard")
	base := "/api/v1/workspaces/" + slug
	endpoint := base + "/layer-imports"
	request := func(method, path, token, contentType string, body io.Reader, want int, result any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != want {
			t.Fatalf("%s %s got %d (%s), want %d: %v", method, path, res.StatusCode, data, want, err)
		}
		if file, ok := result.(*[]byte); ok {
			*file = data
		} else if result != nil {
			if err := json.Unmarshal(data, result); err != nil {
				t.Fatalf("decode %s: %v", data, err)
			}
		}
	}
	get := func(path, token string, want int, out any) {
		request(http.MethodGet, path, token, "", nil, want, out)
	}
	publish := func(path, token, confirmation string, want int, out any) {
		body, _ := json.Marshal(layerPublishRequest{Days: 30, GrantConfirmation: confirmation})
		request(http.MethodPost, path+"/publish", token, "application/json", bytes.NewReader(body), want, out)
	}
	waitForDraft := func(path string, done func(draftResponse) bool) draftResponse {
		t.Helper()
		for {
			var draft draftResponse
			get(path, admin, http.StatusOK, &draft)
			if done(draft) {
				return draft
			}
			select {
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %s. Last state: %+v", path, draft)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	upload := func(layer, name, role, settings string, data []byte) (string, draftResponse) {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		for key, value := range map[string]string{
			"layerSlug": layer, "layerName": name, "role": role, "settings": settings,
		} {
			if err := form.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
		if role == "hierarchy" {
			if err := form.WriteField("hierarchy", "territorial"); err != nil {
				t.Fatal(err)
			}
		}
		part, err := form.CreateFormFile("file", layer+".geojson")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		var queued draftResponse
		request(http.MethodPost, endpoint, admin, form.FormDataContentType(), &body, http.StatusAccepted, &queued)
		path := endpoint + "/" + queued.VersionID.String()
		return path, waitForDraft(path, func(d draftResponse) bool { return d.Status != "pending" })
	}
	waitForReplay := func(path string) rematchProgress {
		t.Helper()
		draft := waitForDraft(path, func(d draftResponse) bool {
			return d.Rematch != nil && (d.Rematch.Status == "completed" || d.Rematch.Status == "failed")
		})
		if draft.Status != "published" || draft.Rematch.Status != "completed" {
			t.Fatalf("publish did not replay detections: %+v", draft)
		}
		return *draft.Rematch
	}

	get(endpoint, admin, http.StatusOK, new(json.RawMessage))
	get(endpoint, guard, http.StatusForbidden, nil)

	// Publish the complete optional section layer with the current paths.
	// Its gaps are intentional, and existing beats keep their parents.
	var section, partialSection json.RawMessage
	if err := pool.QueryRow(ctx, `
		WITH features AS (
			SELECT s.code, jsonb_build_object('type', 'Feature', 'geometry', ST_AsGeoJSON(s.geom, 9)::jsonb,
				'properties', jsonb_build_object('code', s.code, 'name', s.name, 'parent_code', p.code)) AS feature
			FROM units s JOIN units p ON p.hierarchy_id = s.hierarchy_id AND p.current_path = subpath(s.path, 0, -1)
			WHERE s.hierarchy_id = $1 AND s.depth = 4 AND s.current_path IS NOT NULL
		)
		SELECT jsonb_build_object('type', 'FeatureCollection', 'features', jsonb_agg(feature ORDER BY code)),
		       jsonb_build_object('type', 'FeatureCollection', 'features', jsonb_agg(feature) FILTER (WHERE code = 'S01'))
		FROM features
	`, prefix+"10").Scan(&section, &partialSection); err != nil {
		t.Fatal(err)
	}
	const sectionSettings = `{"depth":4,"nameField":"name","codeField":"code","parentCodeField":"parent_code"}`
	partialPath, _ := upload("sections", "Sections", "hierarchy", sectionSettings, partialSection)
	var orphaned map[string]string
	publish(partialPath, admin, "", http.StatusConflict, &orphaned)
	if !strings.Contains(orphaned["error"], "without a parent") {
		t.Fatalf("publishing an incomplete section layer must reject orphaned beats: %v", orphaned)
	}
	sectionPath, sectionDraft := upload("sections", "Sections", "hierarchy", sectionSettings, section)
	checkImportedDraft(t, ctx, pool, sectionDraft, "hierarchy")
	publish(sectionPath, admin, "", http.StatusAccepted, nil)
	waitForReplay(sectionPath)

	const beatSettings = `{"depth":5,"nameField":"name","codeField":"code","parentCodeField":"parent_code"}`
	bad := publishingBeatFixture(t, offset, nil)
	badPath, draft := upload("territorial-units", "Territorial units", "hierarchy", beatSettings, bad)
	expectReview(t, draft, "missing_code", "feature:16", "B16", true)
	blocked := map[string]string{}
	publish(badPath, admin, "", http.StatusConflict, &blocked)
	if !strings.Contains(blocked["error"], "blocking problems") {
		t.Fatalf("unexpected publish refusal: %v", blocked)
	}

	var references struct {
		Features []struct {
			Properties struct{ ID string }
			Geometry   struct{ Coordinates [2]float64 }
		}
	}
	if err := json.Unmarshal(readLayerFixture(t, "reference-detections"), &references); err != nil {
		t.Fatal(err)
	}
	overlayPath := endpoint + "/" + prefix + "31" + "/overlay"
	markDistrict := func(token string, code int) {
		request(http.MethodPut, overlayPath, token, "application/json",
			strings.NewReader(`{"kind":"civil_district"}`), code, nil)
	}
	markDistrict(guard, http.StatusForbidden)
	markDistrict(admin, http.StatusNoContent)
	markDistrict(admin, http.StatusNoContent)

	now := time.Now().UTC()
	var holeLon, holeLat float64
	var observations []firms.Observation
	var inside firms.Observation
	for _, f := range references.Features {
		lon := f.Geometry.Coordinates[0] + offset[0]
		lat := f.Geometry.Coordinates[1] + offset[1]
		switch f.Properties.ID {
		case "hole":
			holeLon, holeLat = lon, lat
		case "inside", "shared-boundary":
		default:
			continue
		}
		observation := firms.Observation{
			Satellite: "N", Instrument: "VIIRS", ObservedAt: now, Longitude: lon, Latitude: lat,
		}
		if f.Properties.ID == "inside" {
			inside = observation
		}
		observations = append(observations, observation)
	}
	ingest := func(product string) {
		t.Helper()
		_, err := runIngest(ctx, pool, ingestRun{Source: source, Product: product},
			func(context.Context) ([]firms.Observation, error) { return observations, nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	ingest("VIIRS_SNPP_NRT")

	var detection, alert string
	if err := pool.QueryRow(ctx, `
		SELECT d.id::text, a.id::text FROM detections d JOIN alerts a
		  ON (a.detection_id, a.observed_at) = (d.id, d.observed_at)
		WHERE d.source = $1 AND a.workspace_id = $2 AND a.unit_path = 'ds.c1.d1.r2.b05'
	`, source, prefix+"01").Scan(&detection, &alert); err != nil {
		t.Fatal(err)
	}
	type overlayMatch struct {
		Kind      string
		Name      string
		VersionID string
	}
	var detail struct {
		Overlays []overlayMatch `json:"overlay_matches"`
	}
	get(base+"/alerts/"+alert, guard, http.StatusOK, &detail)
	if !slices.Contains(detail.Overlays, overlayMatch{Kind: "civil_district", Name: "Demo Reserve One", VersionID: prefix + "31"}) {
		t.Fatalf("unexpected alert overlays: %+v", detail.Overlays)
	}
	history := base + "/alerts/" + alert + "/history"
	request(http.MethodPost, base+"/alerts/"+alert+"/steps", guard, "application/json",
		strings.NewReader(`{"step":"acknowledged"}`), http.StatusOK, nil)

	// A standard-processing copy is outside of the preview and replay, just
	// as it is outside of initial matching.
	ingest("VIIRS_SNPP_SP")
	var linked int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM detection_links l JOIN detections d
			ON (d.id, d.observed_at) = (l.detection_id, l.observed_at) WHERE d.source = $1
	`, source).Scan(&linked); err != nil || linked != len(observations) {
		t.Fatalf("expected 3 linked detections, got %d: %v", linked, err)
	}

	var parents map[string]string
	if err := pool.QueryRow(ctx, `
		SELECT jsonb_object_agg(b.code, p.code)
		FROM units b JOIN units p ON p.hierarchy_id = b.hierarchy_id AND p.current_path = subpath(b.path, 0, -1)
		WHERE b.hierarchy_id = $1 AND b.depth = 5 AND b.current_path IS NOT NULL
	`, prefix+"10").Scan(&parents); err != nil {
		t.Fatal(err)
	}
	beats := publishingBeatFixture(t, offset, parents)
	draftPath, draft := upload("territorial-units", "Territorial units", "hierarchy", beatSettings, beats)
	version := draft.VersionID.String()
	checkImportedDraft(t, ctx, pool, draft, "hierarchy")
	get(draftPath+"/preview", guard, http.StatusForbidden, nil)
	get(draftPath+"/map", guard, http.StatusForbidden, nil)
	get("/api/v1/workspaces/demo/layer-imports/"+version+"/map", admin, http.StatusNotFound, nil)
	var previewMap struct {
		Boundary json.RawMessage
		Data     struct {
			Type     string
			Features []json.RawMessage
		}
		Truncated bool
	}
	get(draftPath+"/map", admin, http.StatusOK, &previewMap)
	if len(previewMap.Boundary) == 0 || previewMap.Data.Type != "FeatureCollection" ||
		len(previewMap.Data.Features) != int(draft.UnitCount) || previewMap.Truncated {
		t.Fatalf("expected map with the checked draft units, got %d features, type %q, truncated %t",
			len(previewMap.Data.Features), previewMap.Data.Type, previewMap.Truncated)
	}
	publish(draftPath, guard, "", http.StatusForbidden, nil)
	var preview testPreview
	get(draftPath+"/preview", admin, http.StatusOK, &preview)
	if preview.Blocked != "" || len(preview.BrokenGrants) != 0 || preview.Changes["reassigned"].Count != 2 ||
		preview.Changes["triage"].Count != 1 {
		t.Fatalf("unexpected beat preview: %+v, import problems: %+v", preview, draft.Problems)
	}
	if len(preview.Previous) != 1 || preview.Previous[0].LayerName != "Territorial units" ||
		preview.Previous[0].Version != 1 {
		t.Fatalf("preview lost the previous version: %+v", preview.Previous)
	}
	reassigned := preview.Changes["reassigned"].Items
	index := slices.IndexFunc(reassigned, func(change previewChange) bool { return change.DetectionID == detection })
	if index < 0 {
		t.Fatalf("preview did not report the expected detection: %+v", reassigned)
	}
	change := reassigned[index]
	if change.DetectionID != detection || !slices.Equal(change.OldBeats, []string{"territorial/ds.c1.d1.r2.b05"}) ||
		!slices.Equal(change.NewBeats, []string{"territorial/ds.c1.d2.r3.b08"}) {
		t.Fatalf("unexpected reassignment: %+v", change)
	}
	t.Logf("preview detection %s: %v -> %v", detection, change.OldBeats, change.NewBeats)

	var matchesBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM detection_matches WHERE workspace_id = $1
		AND detection_id = $2 AND hierarchy #>> '{territorial,beat,code}' = 'B05'
	`, prefix+"01", detection).Scan(&matchesBefore); err != nil || matchesBefore != 1 {
		t.Fatalf("preview changed the original match: %d, %v", matchesBefore, err)
	}
	publish(draftPath, admin, "", http.StatusAccepted, nil)
	get(draftPath+"/map", admin, http.StatusNotFound, nil)
	replay := waitForReplay(draftPath)
	if replay.Processed != int64(preview.Scanned) || replay.MatchesWritten != replay.Processed || replay.AlertsCreated != 0 {
		t.Fatalf("unexpected replay progress: %+v", replay)
	}

	var moved, alertIntact, datesOK, grantsIntact, audited bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM detection_matches m WHERE m.workspace_id = $1 AND m.detection_id = $2
			AND m.hierarchy #>> '{territorial,beat,code}' = 'B08' AND m.triage_reason IS NULL
			AND m.layer_version_ids @> ARRAY[$3::uuid, $4::uuid]),
		EXISTS (SELECT 1 FROM alerts a WHERE id = $5 AND a.unit_path = 'ds.c1.d1.r2.b05'
			AND a.hierarchy #>> '{territorial,beat,code}' = 'B05' AND a.queue = 'unit'
			AND $3::uuid <> ALL(a.layer_version_ids)
			AND EXISTS (SELECT 1 FROM workflow_instances i WHERE i.alert_id = a.id AND i.current_step = 'acknowledged')),
		EXISTS (SELECT 1 FROM units old JOIN units new USING (hierarchy_id, path)
			JOIN layer_versions v ON v.id = new.layer_version_id
			JOIN layer_versions previous ON previous.id = old.layer_version_id
			WHERE v.id = $3 AND old.path = 'ds.c1.d1.r2.b05' AND old.layer_version_id = $4
			AND old.valid_from < old.valid_to AND old.valid_to = new.valid_from AND new.valid_to IS NULL
			AND old.valid_to = v.published_at AND previous.retired_at = v.published_at AND previous.status = 'retired'),
		(SELECT count(*) = 3 AND bool_and(granted_at = $6) FROM role_assignments WHERE workspace_id = $1),
		EXISTS (SELECT 1 FROM audit_log WHERE workspace_id = $1 AND action = 'layer.publish' AND target_id = $3::text)
	`, prefix+"01", detection, version, prefix+"21", alert, testGrantTime).Scan(
		&moved, &alertIntact, &datesOK, &grantsIntact, &audited)
	if err != nil || !moved || !alertIntact || !datesOK || !grantsIntact || !audited {
		t.Fatalf("publish invariants: moved=%t alert=%t dates=%t grants=%t audited=%t: %v", moved, alertIntact, datesOK, grantsIntact, audited, err)
	}
	var mixedBranch bool
	err = pool.QueryRow(ctx, `
		SELECT array_agg(path::text ORDER BY code) = ARRAY['ds.c2.d4.r8.b23', 'ds.c2.d4.r8.s01.b24']
		FROM units WHERE hierarchy_id = $1 AND current_path IS NOT NULL AND code IN ('B23', 'B24')
	`, prefix+"10").Scan(&mixedBranch)
	if err != nil || !mixedBranch {
		t.Fatalf("the published beat layer must support both paths through Range 8: %v, %v", mixedBranch, err)
	}
	get(base+"/alerts/"+alert, guard, http.StatusOK, nil)
	get(history, guard, http.StatusOK, nil)
	t.Log("first replay moved the match; the original unit alert, grant dates, and unit validity history are intact")

	renamed := bytes.ReplaceAll(beats, []byte(`"B05"`), []byte(`"B05N"`))
	renamePath, _ := upload("territorial-units", "Territorial units", "hierarchy", beatSettings, renamed)
	publish(renamePath, admin, "", http.StatusConflict, &blocked)
	if !strings.Contains(blocked["error"], "confirm") {
		t.Fatalf("missing-grant refusal: %v", blocked)
	}
	get(renamePath+"/preview", admin, http.StatusOK, &preview)
	if len(preview.BrokenGrants) != 1 || preview.BrokenGrants[0].UnitPath != "ds.c1.d1.r2.b05" ||
		preview.BrokenGrants[0].Username != "beat.guard" || preview.GrantConfirmation == "" {
		t.Fatalf("preview did not report the disappearing beat grant: %+v", preview)
	}
	publish(renamePath, admin, "obsolete-confirmation", http.StatusConflict, nil)
	publish(renamePath, admin, preview.GrantConfirmation, http.StatusAccepted, nil)
	waitForReplay(renamePath)
	var grantsLeft int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM role_assignments WHERE workspace_id = $1", prefix+"01").Scan(&grantsLeft); err != nil || grantsLeft != 2 {
		t.Fatalf("expected only confirmed grant to be removed: %d remain, %v", grantsLeft, err)
	}
	t.Log("publish refused the blocking upload and unconfirmed grant removal; confirmation removed only the named grant")

	// Expand eligibility near the old forest hole while dropping the first
	// point from eligibility. Only the hole should get a new alert.
	forest := []byte(fmt.Sprintf(`{"type":"FeatureCollection","features":[{"type":"Feature","properties":{},
	  "geometry":{"type":"Polygon","coordinates":[[[%f,%f],[%f,%f],[%f,%f],[%f,%f],[%f,%f]]]}}]}`,
		holeLon-.002, holeLat-.002, holeLon+.002, holeLat-.002, holeLon+.002, holeLat+.002,
		holeLon-.002, holeLat+.002, holeLon-.002, holeLat-.002))
	forestPath, _ := upload("forest", "Forest", "forest", "{}", forest)
	preview = testPreview{}
	get(forestPath+"/preview", admin, http.StatusOK, &preview)
	if preview.Changes["included"].Count != 1 || preview.Changes["excluded"].Count != 2 {
		t.Fatalf("unexpected forest preview: %+v", preview)
	}
	publish(forestPath, admin, "", http.StatusAccepted, nil)
	if replay := waitForReplay(forestPath); replay.AlertsCreated != 1 {
		t.Fatalf("expected one new alert: %+v", replay)
	}
	get(base+"/alerts/"+alert+"?class=inside", admin, http.StatusOK, nil)
	var opened int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM alerts a JOIN workflow_instances i ON i.alert_id = a.id
		JOIN detections d ON (d.id, d.observed_at) = (a.detection_id, a.observed_at)
		WHERE a.workspace_id = $1 AND d.source = $2 AND i.current_step = 'new'
			AND ST_X(d.geom) = $3 AND ST_Y(d.geom) = $4
	`, prefix+"01", source, holeLon, holeLat).Scan(&opened); err != nil || opened != 1 {
		t.Fatalf("expected one workflow for a newly eligible alert, got %d: %v", opened, err)
	}
	t.Log("forest replay created one newly eligible alert and kept the original alert after its detection was excluded")

	// Add a late observation at the same location and acquisition time as the
	// original alert, from another satellite. Both arrived under different
	// published boundaries, and both should export the original beat and forest.
	late := inside
	late.Satellite = "L"
	observations = []firms.Observation{late}
	ingest("VIIRS_SNPP_NRT")
	for _, kind := range []string{"detections", "alerts"} {
		var export exportStatus
		request(http.MethodPost, base+"/exports", admin, "application/json",
			strings.NewReader(fmt.Sprintf(`{"kind":%q,"format":"csv_lonlat","hours":24,"forestClasses":["inside","near_edge","fixed_heat_source","outside"]}`, kind)), http.StatusAccepted, &export)
		var ids []string
		err := pool.QueryRow(ctx, `
			SELECT array_agg(r.detection_id::text) FROM export_rows r
			JOIN detections d ON (d.id, d.observed_at) = (r.detection_id, r.observed_at)
			WHERE r.export_id = $1 AND d.source = $2 AND d.product = 'VIIRS_SNPP_NRT'
			  AND d.satellite IN ('N', 'L') AND ST_X(d.geom) = $3 AND ST_Y(d.geom) = $4
			  AND r.boundary_snapshot #>> '{hierarchy,territorial,beat,code}' = 'B05'
			  AND r.boundary_snapshot->'layerVersionIds' @> jsonb_build_array($5::text, $6::text)
			  AND NOT (r.boundary_snapshot->'layerVersionIds' ? $7)
			  AND r.boundary_snapshot->'overlays' @> '[{"kind":"civil_district","name":"Demo Reserve One"}]'
			  AND (r.dispatch->>'unitPath' = 'ds.c1.d1.r2.b05' OR ($8 = 'detections' AND r.dispatch IS NULL))
		`, export.ID, source, late.Longitude, late.Latitude, prefix+"21", prefix+"31", version, kind).Scan(&ids)
		if err != nil || len(ids) != 2 {
			t.Fatalf("%s export must freeze both original and late observations with the old boundaries: %v, %v", kind, ids, err)
		}

		exportPath := base + "/exports/" + export.ID
		for export.Status == "pending" {
			select {
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %s", exportPath)
			case <-time.After(100 * time.Millisecond):
				get(exportPath, admin, http.StatusOK, &export)
			}
		}
		if export.Status != "ready" {
			t.Fatalf("%s export finished as %+v", kind, export)
		}
		var file []byte
		get(exportPath+"/file", admin, http.StatusOK, &file)
		records, err := csv.NewReader(bytes.NewReader(unzipMember(t, file, ".csv"))).ReadAll()
		if err != nil || len(records) < 3 {
			t.Fatalf("read %s export: %v (%d records)", kind, err, len(records))
		}
		idIdx, beatIdx := slices.Index(records[0], "detection_id"), slices.Index(records[0], "beat_code")
		versionIdx := slices.Index(records[0], "boundary_version_ids")
		if idIdx < 0 || beatIdx < 0 || versionIdx < 0 {
			t.Fatalf("%s export missing boundary columns: %v", kind, records[0])
		}
		found := 0
		for _, row := range records[1:] {
			if slices.Contains(ids, row[idIdx]) {
				var versions []string
				if err := json.Unmarshal([]byte(row[versionIdx]), &versions); err != nil ||
					row[beatIdx] != "B05" || !slices.Contains(versions, prefix+"31") || slices.Contains(versions, version) {
					t.Fatalf("incorrect boundaries for %s in %s export: %v, %v", row[idIdx], kind, row, err)
				}
				found++
			}
		}
		if found != len(ids) {
			t.Fatalf("%s export file contains %d of %d expected observations", kind, found, len(ids))
		}
	}
}

var testGrantTime = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

func readLayerFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../../seed/demo-state", name+".geojson"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func seedPublishingWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, prefix, slug, uploads string) [2]float64 {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// Alerts and workflow history are retained, so each run needs a fresh
	// location away from the demo detections and previous publishing tests.
	var run int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM workspaces WHERE slug LIKE 'publish-test-%'").Scan(&run); err != nil {
		t.Fatal(err)
	}
	offset := [2]float64{1 + float64(run%8)*.75, float64(run/8) * .45}
	for _, file := range []string{"load.sql", "roles.sql"} {
		body, err := os.ReadFile(filepath.Join("../../seed/demo-state", file))
		if err != nil {
			t.Fatal(err)
		}
		seed := strings.ReplaceAll(string(body), seedPrefix, prefix)
		seed = strings.ReplaceAll(seed, "'demo'", "'"+slug+"'")
		for _, id := range []string{"a1", "a2", "a3"} {
			seed = strings.ReplaceAll(seed, prefix+id, seedPrefix+id)
		}
		if _, err := tx.Exec(ctx, seed); err != nil {
			t.Fatalf("load publishing test seed %s: %v", file, err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE role_assignments SET granted_at = $2,
			unit_path = CASE WHEN role_slug = 'field_staff' THEN 'ds.c1.d1.r2.b05'::ltree ELSE unit_path END
		WHERE workspace_id = $1`, prefix+"01", testGrantTime); err != nil {
		t.Fatal(err)
	}
	for _, update := range []struct{ sql, id string }{
		{"UPDATE workspaces SET boundary = ST_Translate(boundary, $2, $3) WHERE id = $1", prefix + "01"},
		{`UPDATE units u SET geom = ST_Translate(geom, $2, $3) FROM hierarchies h
		  WHERE u.hierarchy_id = h.id AND h.workspace_id = $1`, prefix + "01"},
		{`UPDATE layer_features f SET geom = ST_Translate(geom, $2, $3)
		  FROM layer_versions v JOIN layers l ON l.id = v.layer_id
		  WHERE f.layer_version_id = v.id AND l.workspace_id = $1`, prefix + "01"},
	} {
		if _, err := tx.Exec(ctx, update.sql, update.id, offset[0], offset[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Append-only history keeps the test workspace and detections.
		// Disable future matching and remove its temporary grants and uploads.
		rows, err := pool.Query(ctx, `SELECT j.file_key FROM geo_jobs j JOIN layer_versions v ON v.id=j.layer_version_id
			JOIN layers l ON l.id=v.layer_id WHERE l.workspace_id=$1`, prefix+"01")
		if err == nil {
			var keys []string
			keys, err = pgx.CollectRows(rows, pgx.RowTo[string])
			for _, key := range keys {
				if err := os.Remove(filepath.Join(uploads, key)); err != nil {
					t.Error(err)
				}
			}
		}
		if err != nil {
			t.Error(err)
		}
		for _, cleanup := range []struct{ query, arg string }{
			{"DELETE FROM role_assignments WHERE workspace_id = $1", prefix + "01"},
			{"UPDATE workspaces SET boundary = NULL WHERE id = $1", prefix + "01"},
		} {
			if _, err := pool.Exec(ctx, cleanup.query, cleanup.arg); err != nil {
				t.Error(err)
			}
		}
	})
	return offset
}

func publishingBeatFixture(t *testing.T, offset [2]float64, parents map[string]string) []byte {
	t.Helper()
	type feature struct {
		Type       string            `json:"type"`
		Properties map[string]string `json:"properties"`
		Geometry   struct {
			Type        string         `json:"type"`
			Coordinates [][][2]float64 `json:"coordinates"`
		} `json:"geometry"`
	}
	var collection struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}
	if err := json.Unmarshal(readLayerFixture(t, "beats-with-sections"), &collection); err != nil {
		t.Fatal(err)
	}
	if parents != nil {
		rings := map[string][][2]float64{}
		for i, f := range collection.Features {
			if f.Properties["code"] == "" {
				f.Properties["code"] = fmt.Sprintf("B%02d", i+1)
			}
			if f.Properties["parent_code"] == "" {
				f.Properties["parent_code"] = parents[f.Properties["code"]]
			}
			rings[f.Properties["code"]] = f.Geometry.Coordinates[0]
		}

		// Repair the demo overlap by moving the bulging B17 vertices back to
		// the undisturbed B20 edge. The two shapes then share the same segments.
		for i, a := range rings["B17"] {
			for _, b := range rings["B20"] {
				if a[1] == b[1] && a[0]-b[0] > .00399 && a[0]-b[0] < .00401 {
					rings["B17"][i] = b
				}
			}
		}

		// Move only the interior vertices on the common B05/B08 edge westward.
		// Keeping the endpoints leaves the footprint of the two beats intact.
		west, east := rings["B05"], rings["B08"]
		var shared [][2]float64
		for _, point := range west {
			if slices.Contains(east, point) && !slices.Contains(shared, point) {
				shared = append(shared, point)
			}
		}
		if len(shared) != 5 {
			t.Fatalf("expected five vertices on the common beat edge, got %v", shared)
		}
		slices.SortFunc(shared, func(a, b [2]float64) int { return cmp.Compare(a[1], b[1]) })
		for _, ring := range [][][2]float64{west, east} {
			for i, point := range ring {
				if slices.Contains(shared[1:len(shared)-1], point) {
					ring[i][0] -= .025
				}
			}
		}
	}
	for _, f := range collection.Features {
		for _, ring := range f.Geometry.Coordinates {
			for i := range ring {
				ring[i][0] += offset[0]
				ring[i][1] += offset[1]
			}
		}
	}
	encoded, err := json.Marshal(collection)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
