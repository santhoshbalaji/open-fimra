package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLayerImportIntegration(t *testing.T) {
	if os.Getenv("FIMRA_GEO_INTEGRATION") != "1" {
		t.Skip("Run make geo-test-integration with the worker and the migrated development stack running.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	cfg := loadConfig()
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := ensureDemoSeed(ctx, pool); err != nil {
		t.Fatalf("load demo SQL: %v", err)
	}

	// Use a copy of the level definitions so that parents must come from drafts.
	prefix := fmt.Sprintf("geo-test-%d", time.Now().UnixNano())
	const seedHierarchy = "d0000000-0000-4000-8000-000000000010"
	const workspaceID = "d0000000-0000-4000-8000-000000000001"
	var hierarchy string
	if err := pool.QueryRow(ctx, `
		WITH new_hierarchy AS (
			INSERT INTO hierarchies (workspace_id, slug, name, kind)
			VALUES ($1, $2, 'Draft test hierarchy', 'territorial') RETURNING id
		)
		INSERT INTO hierarchy_levels (hierarchy_id, depth, slug, labels)
		SELECT n.id, hl.depth, hl.slug, hl.labels FROM new_hierarchy n
		CROSS JOIN hierarchy_levels hl WHERE hl.hierarchy_id = $3 RETURNING hierarchy_id::text
	`, workspaceID, prefix, seedHierarchy).Scan(&hierarchy); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		rows, err := pool.Query(cleanup, `
			SELECT file_key FROM geo_jobs j JOIN layer_versions v ON v.id = j.layer_version_id
			JOIN layers l ON l.id = v.layer_id WHERE l.workspace_id = $1 AND l.slug LIKE $2
		`, workspaceID, prefix+"-%")
		if err != nil {
			t.Error(err)
			return
		}
		var keys []string
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				t.Error(err)
				return
			}
			keys = append(keys, key)
		}
		if err := rows.Err(); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(cleanup, "DELETE FROM layers WHERE workspace_id = $1 AND slug LIKE $2", workspaceID, prefix+"-%"); err != nil {
			t.Error(err)
			return
		}
		if _, err := pool.Exec(cleanup, "DELETE FROM hierarchies WHERE id = $1", hierarchy); err != nil {
			t.Error(err)
		}
		for _, key := range keys {
			if err := os.Remove(filepath.Join(cfg.uploadDir, key)); err != nil {
				t.Error(err)
			}
		}
	})

	// Give the officer the same role slug below the root to test the scope check.
	const addNarrowGrant = `
		INSERT INTO role_assignments (user_id, workspace_id, hierarchy_id, unit_path, role_slug)
		SELECT a.user_id, a.workspace_id, a.hierarchy_id, a.unit_path, 'state_admin'
		FROM role_assignments a JOIN users u ON a.user_id = u.id
		WHERE a.workspace_id = $1 AND a.role_slug = 'division_officer' AND u.username = 'division.officer'
		RETURNING user_id::text, unit_path::text
	`
	var officerID, officerPath string
	if err := pool.QueryRow(ctx, addNarrowGrant, workspaceID).Scan(&officerID, &officerPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `
			DELETE FROM role_assignments WHERE user_id = $1 AND workspace_id = $2
			  AND hierarchy_id = $3 AND unit_path = $4 AND role_slug = 'state_admin'
		`, officerID, workspaceID, seedHierarchy, officerPath)
		if err != nil {
			t.Error(err)
		}
	})

	provider, err := discoverProvider(ctx, cfg.issuer)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandler(pool, newAuthenticator(provider, cfg.issuer, cfg.audience),
		slog.Default(), devOrigins, cfg.uploadDir))
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 10 * time.Second}
	admin := demoToken(t, client, provider.Endpoint().TokenURL, "state.admin")
	officer := demoToken(t, client, provider.Endpoint().TokenURL, "division.officer")
	const endpoint = "/api/v1/workspaces/demo/layer-imports"

	request := func(method, path, token, contentType string, body io.Reader, want int) (draftResponse, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
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
			t.Fatalf("%s %s got HTTP %d (%s), want %d, read error %v", method, path, res.StatusCode, data, want, err)
		}
		var draft draftResponse
		if want == http.StatusAccepted || want == http.StatusOK {
			if err := json.Unmarshal(data, &draft); err != nil {
				t.Fatal(err)
			}
		}
		return draft, res.Header.Get("Location")
	}
	request(http.MethodPost, endpoint, "", "", nil, http.StatusUnauthorized)
	request(http.MethodPost, endpoint, officer, "", nil, http.StatusForbidden)

	importFile := func(stem, role string, settings map[string]any) draftResponse {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		settingsJSON, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		fields := map[string]string{"layerSlug": prefix + "-" + stem, "layerName": stem,
			"role": role, "settings": string(settingsJSON)}
		if role == "hierarchy" {
			fields["hierarchy"] = prefix
		}
		for field, value := range fields {
			if err := form.WriteField(field, value); err != nil {
				t.Fatal(err)
			}
		}
		name := stem + ".geojson"
		part, err := form.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join("../../seed/demo-state", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}

		queued, location := request(http.MethodPost, endpoint, admin, form.FormDataContentType(), &body, http.StatusAccepted)
		if queued.Status != "pending" || !queued.VersionID.Valid || location != endpoint+"/"+queued.VersionID.String() {
			t.Fatalf("unexpected upload response: %+v, location %q", queued, location)
		}
		request(http.MethodGet, location, officer, "", nil, http.StatusForbidden)
		for {
			draft, _ := request(http.MethodGet, location, admin, "", nil, http.StatusOK)
			if draft.Status != "pending" {
				t.Logf("%s: %s, %d units, %d features, %d review items", stem,
					draft.Status, draft.UnitCount, draft.FeatureCount, len(draft.Problems))
				return draft
			}
			select {
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %s", stem)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}

	var previousVersion string
	for depth, stem := range []string{"state", "circles", "divisions", "ranges", "beats"} {
		settings := map[string]any{"depth": depth, "nameField": "name", "codeField": "code", "parentCodeField": "parent_code"}
		if depth > 0 {
			settings["parentVersionId"] = previousVersion
		}
		draft := importFile(stem, "hierarchy", settings)
		previousVersion = draft.VersionID.String()
		checkImportedDraft(t, ctx, pool, draft, "hierarchy")
		var expected, exact int64
		err := pool.QueryRow(ctx, `
			WITH reference_units AS (
				SELECT u.*, p.code AS parent_code, p.path AS parent_path
				FROM units u LEFT JOIN units p ON p.hierarchy_id = u.hierarchy_id
				  AND p.depth = u.depth - 1 AND p.path @> u.path AND p.valid_to IS NULL
				WHERE u.hierarchy_id = $1 AND u.depth = $2 AND u.valid_to IS NULL
			)
			SELECT count(*), count(d.feature_number) FROM reference_units u
			LEFT JOIN draft_units d ON d.layer_version_id = $3 AND d.depth = u.depth
			  AND d.code = u.code AND d.name = u.name AND d.path = u.path
			  AND d.parent_code IS NOT DISTINCT FROM u.parent_code AND d.parent_path IS NOT DISTINCT FROM u.parent_path
			  AND ST_HausdorffDistance(d.geom, u.geom) < 1e-8
			  AND ST_Area(ST_Transform(ST_SymDifference(d.geom, u.geom), 7755)) < 0.1
			  AND abs(d.area_m2 - ST_Area(ST_Transform(u.geom, 7755))) < 1
		`, seedHierarchy, depth, draft.VersionID).Scan(&expected, &exact)
		if err != nil || expected == 0 || draft.UnitCount != expected || exact != expected || draft.FeatureCount != 0 {
			t.Fatalf("%s: expected %d units, found %d with matching data out of %d: %v", stem, expected, exact, draft.UnitCount, err)
		}
		if stem == "beats" {
			expectReview(t, draft, "missing_code", "feature:16", "B16", true)
			expectReview(t, draft, "missing_parent_code", "feature:22", "R8", true)
			expectReview(t, draft, "overlapping_units", "feature:17", "B20", true)
		}
	}

	for _, role := range []string{"forest", "exclusion"} {
		stem := role
		settings := map[string]any{"nameField": "name", "codeField": "code"}
		if role == "forest" {
			settings["coverageVersionId"] = previousVersion
		} else {
			stem = "exclusions"
		}
		draft := importFile(stem, role, settings)
		checkImportedDraft(t, ctx, pool, draft, role)
		var expected, exact int64
		err := pool.QueryRow(ctx, `
			SELECT count(*), count(d.id) FROM layer_features f
			JOIN layer_versions v ON v.id = f.layer_version_id
			JOIN layers l ON l.id = v.layer_id
			LEFT JOIN layer_features d ON d.layer_version_id = $3 AND d.name = f.name
			  AND d.code IS NOT DISTINCT FROM f.code AND d.attributes = f.attributes
			  AND ST_HausdorffDistance(d.geom, f.geom) < 1e-8
			  AND ST_Area(ST_Transform(ST_SymDifference(d.geom, f.geom), 7755)) < 0.1
			  AND abs(d.area_m2 - ST_Area(ST_Transform(f.geom, 7755))) < 1
			WHERE l.workspace_id = $1 AND l.slug = $2 AND v.status = 'published'
		`, workspaceID, stem, draft.VersionID).Scan(&expected, &exact)
		if err != nil || expected == 0 || draft.FeatureCount != expected || exact != expected || draft.UnitCount != 0 {
			t.Fatalf("%s: expected %d features, found %d with matching data out of %d: %v", stem, expected, exact, draft.FeatureCount, err)
		}
		if role == "forest" {
			expectReview(t, draft, "forest_hole", "feature:2", "hole", false)
			expectReview(t, draft, "forest_outside_units", "feature:3", "outside", false)
			expectReview(t, draft, "missing_code", "feature:4", "no code", false)
		}
	}

	failed := importFile("forest", "forest", map[string]any{"codeField": "absent"})
	if failed.Status != "failed" || failed.FeatureCount != 0 || failed.UnitCount != 0 ||
		len(failed.Problems) != 1 || failed.Problems[0].Kind != "unknown_field" || !failed.Problems[0].BlocksPublish {
		t.Fatalf("invalid mapping should leave a failed draft with a blocking problem: %+v", failed)
	}

	var liveUnits int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM units WHERE hierarchy_id = $1", hierarchy).Scan(&liveUnits); err != nil || liveUnits != 0 {
		t.Fatalf("draft imports changed live units: %d (%v)", liveUnits, err)
	}
}

func checkImportedDraft(t *testing.T, ctx context.Context, pool *pgxpool.Pool, draft draftResponse, role string) {
	t.Helper()
	if draft.Status != "ready" || draft.Role != role || draft.SourceCRS == nil || *draft.SourceCRS != "EPSG:4326" {
		t.Fatalf("unexpected draft: %+v, problems: %+v", draft, draft.Problems)
	}
	var audited bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT FROM audit_log a JOIN layer_versions v ON v.id::text = a.target_id
			JOIN layers l ON l.id = v.layer_id AND l.workspace_id = a.workspace_id
			WHERE v.id = $1 AND v.status = 'draft'
			  AND a.action = 'layer.upload' AND a.target_type = 'layer_version'
			  AND a.actor_kind = 'user' AND a.actor_id = 'd0000000-0000-4000-8000-0000000000a1'
		)
	`, draft.VersionID).Scan(&audited); err != nil || !audited {
		t.Fatalf("upload %s did not create a draft and audit entry: %v", draft.VersionID.String(), err)
	}
}

func expectReview(t *testing.T, draft draftResponse, kind, ref, messagePart string, blocks bool) {
	t.Helper()
	for _, problem := range draft.Problems {
		if problem.Kind == kind && problem.FeatureRef != nil && *problem.FeatureRef == ref &&
			strings.Contains(problem.Message, messagePart) && problem.BlocksPublish == blocks {
			return
		}
	}
	t.Errorf("missing review item %s for %s: %+v", kind, ref, draft.Problems)
}
