package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// TestGISLinkIntegration reads the OGC API with each demo user's personal
// access key and compares the answers with the detection tiles and the alert
// queue for the same user. When FIMRA_GIS_OGR_IMAGE names a GDAL image, it
// also reads the service with ogrinfo in that image, on the host network.
func TestGISLinkIntegration(t *testing.T) {
	if os.Getenv("FIMRA_API_INTEGRATION") != "1" {
		t.Skip("Run make api-test-integration with the development stack running and migrated.")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	cfg := loadConfig()
	pool, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := ensureDemoSeed(ctx, pool); err != nil {
		t.Fatalf("load the demo seed: %v", err)
	}
	// The same synthetic detections as make demo-detections, at four ages.
	if _, err := runIngest(ctx, pool, ingestRun{Source: demoSource, Product: demoProduct}, func(context.Context) ([]firms.Observation, error) {
		return readFixture(filepath.Join(fixtureDir, "demo.csv"), demoProduct, true, time.Now())
	}); err != nil {
		t.Fatalf("load demo detections: %v", err)
	}
	provider, err := discoverProvider(ctx, cfg.issuer)
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandler(pool, newAuthenticator(provider, cfg.issuer, cfg.audience), slog.New(slog.NewJSONHandler(io.Discard, nil)), devOrigins, t.TempDir(), nil)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: 20 * time.Second}

	users := []string{"state.admin", "division.officer", "beat.guard", "no.role"}
	tokens := map[string]string{}
	for _, user := range users {
		tokens[user] = demoToken(t, client, provider.Endpoint().TokenURL, user)
	}
	type answer struct {
		code   int
		body   []byte
		header http.Header
	}
	do := func(method, path string, body any, auth func(*http.Request)) answer {
		t.Helper()
		var reader io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(data)
		}
		target := path
		if !strings.HasPrefix(path, "http") {
			target = server.URL + path
		}
		req, err := http.NewRequestWithContext(ctx, method, target, reader)
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if auth != nil {
			auth(req)
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
		return answer{res.StatusCode, data, res.Header}
	}
	bearer := func(user string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tokens[user]) }
	}
	basic := func(key string) func(*http.Request) {
		return func(r *http.Request) { r.SetBasicAuth("qgis", key) }
	}
	header := func(key string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set(accessKeyHeader, key) }
	}

	// Each user makes a key in the same way as the web app.
	keys := map[string]accessKey{}
	for _, user := range users {
		a := do(http.MethodPost, "/api/v1/access-keys", map[string]any{"name": "GIS link test " + user, "expiresInDays": 1}, bearer(user))
		var key accessKey
		if a.code != http.StatusCreated || json.Unmarshal(a.body, &key) != nil || !looksLikeAccessKey(key.Key) ||
			key.ExpiresAt == nil || a.header.Get("Location") != "/api/v1/access-keys/"+key.ID {
			t.Fatalf("%s key: HTTP %d %s", user, a.code, a.body)
		}
		keys[user] = key
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		ids := []string{}
		for _, key := range keys {
			ids = append(ids, key.ID)
		}
		if _, err := pool.Exec(cleanup, `UPDATE personal_access_keys SET revoked_at = now()
			WHERE id = ANY($1::uuid[]) AND revoked_at IS NULL`, ids); err != nil {
			t.Error(err)
		}
	})

	const base = "/ogc/workspaces/demo"
	// ogcIDs follows next links until the last page and returns the feature IDs.
	ogcIDs := func(key, collection, query string) []int64 {
		t.Helper()
		next := base + "/collections/" + collection + "/items?" + query
		var ids []int64
		for pages := 0; next != ""; pages++ {
			if pages > 200 {
				t.Fatalf("%s: too many pages", collection)
			}
			a := do(http.MethodGet, next, nil, basic(key))
			var page struct {
				Type     string `json:"type"`
				Returned int    `json:"numberReturned"`
				Links    []ogcLink
				Features []struct {
					ID         int64          `json:"id"`
					Properties map[string]any `json:"properties"`
				} `json:"features"`
			}
			if a.code != http.StatusOK || a.header.Get("Content-Type") != geoJSONType ||
				json.Unmarshal(a.body, &page) != nil || page.Type != "FeatureCollection" || page.Returned != len(page.Features) {
				t.Fatalf("%s: HTTP %d %s", next, a.code, a.body)
			}
			next = ""
			for _, link := range page.Links {
				if link.Rel == "next" {
					if !strings.HasPrefix(link.Href, server.URL+base) {
						t.Fatalf("next link %s is not on this server", link.Href)
					}
					next = link.Href
				}
			}
			for _, f := range page.Features {
				ids = append(ids, f.ID)
			}
		}
		slices.Sort(ids)
		return ids
	}
	tileIDs := func(user string, hours int, classes string) []int64 {
		t.Helper()
		a := do(http.MethodGet, fmt.Sprintf("/api/v1/workspaces/demo/tiles/detections/0/0/0.mvt?hours=%d&classes=%s", hours, classes), nil, bearer(user))
		if a.code == http.StatusNoContent {
			return nil
		}
		if a.code != http.StatusOK {
			t.Fatalf("%s tile: HTTP %d", user, a.code)
		}
		var ids []int64
		for _, feature := range tileFeatures(t, a.body) {
			ids = append(ids, int64(feature["id"].(uint64)))
		}
		slices.Sort(ids)
		return ids
	}
	alertIDs := func(user, query string) []int64 {
		t.Helper()
		var ids []int64
		cursor := ""
		for {
			q := query + "&limit=200"
			if cursor != "" {
				q += "&cursor=" + url.QueryEscape(cursor)
			}
			a := do(http.MethodGet, "/api/v1/workspaces/demo/alerts?"+q, nil, bearer(user))
			var page alertPage
			if a.code != http.StatusOK || json.Unmarshal(a.body, &page) != nil {
				t.Fatalf("%s alerts: HTTP %d %s", user, a.code, a.body)
			}
			for _, item := range page.Items {
				var row struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(item, &row)
				id, _ := strconv.ParseInt(row.ID, 10, 64)
				ids = append(ids, id)
			}
			if page.NextCursor == "" {
				slices.Sort(ids)
				return ids
			}
			cursor = page.NextCursor
		}
	}
	since := func(hours int) string {
		return url.QueryEscape(time.Now().Add(-time.Duration(hours)*time.Hour).UTC().Format(time.RFC3339Nano) + "/..")
	}

	const allClasses = "inside,near_edge,outside,fixed_heat_source"
	seen := map[string]int{}
	for _, user := range users[:3] {
		t.Run(user, func(t *testing.T) {
			key := keys[user].Key
			for _, hours := range []int{168, 720} {
				want := tileIDs(user, hours, allClasses)
				// Small pages exercise the next links.
				if got := ogcIDs(key, "detections", "limit=3&datetime="+since(hours)); !slices.Equal(got, want) {
					t.Fatalf("%d hours: OGC detections %v, tiles %v", hours, got, want)
				}
				want = tileIDs(user, hours, "inside,near_edge,fixed_heat_source")
				if got := ogcIDs(key, "detections", "forest_class=inside,near_edge,fixed_heat_source&datetime="+since(hours)); !slices.Equal(got, want) {
					t.Fatalf("%d hours, map classes: OGC detections %v, tiles %v", hours, got, want)
				}
				wantAlerts := alertIDs(user, "hours="+strconv.Itoa(hours))
				if got := ogcIDs(key, "alerts", "limit=2&datetime="+since(hours)); !slices.Equal(got, wantAlerts) {
					t.Fatalf("%d hours: OGC alerts %v, queue %v", hours, got, wantAlerts)
				}
				for _, queue := range []string{"unit", "edge_review", "triage"} {
					want := alertIDs(user, "hours="+strconv.Itoa(hours)+"&queue="+queue+"&status=new&class=inside")
					if got := ogcIDs(key, "alerts", "queue="+queue+"&step=new&forest_class=inside&datetime="+since(hours)); !slices.Equal(got, want) {
						t.Fatalf("%s queue: OGC alerts %v, queue %v", queue, got, want)
					}
				}
				if hours == 168 {
					seen[user] = len(want) + len(wantAlerts)
					t.Logf("%s: %d detections and %d alerts in 168 hours match the tiles and queue", user, len(want), len(wantAlerts))
				}
			}
			// The header works as well as HTTP Basic.
			if a := do(http.MethodGet, base+"/collections/alerts/items", nil, header(key)); a.code != http.StatusOK {
				t.Fatalf("X-API-Key: HTTP %d %s", a.code, a.body)
			}
		})
	}
	if t.Failed() {
		return
	}
	if seen["state.admin"] <= seen["division.officer"] || seen["division.officer"] <= seen["beat.guard"] {
		t.Fatalf("grants should narrow what each user sees: %v", seen)
	}

	t.Run("single features follow the grants", func(t *testing.T) {
		admin := ogcIDs(keys["state.admin"].Key, "alerts", "datetime="+since(168))
		officer := ogcIDs(keys["division.officer"].Key, "alerts", "datetime="+since(168))
		hidden := int64(0)
		for _, id := range admin {
			if !slices.Contains(officer, id) {
				hidden = id
				break
			}
		}
		if hidden == 0 || len(officer) == 0 {
			t.Fatalf("need an alert the officer cannot see: admin %v, officer %v", admin, officer)
		}
		path := fmt.Sprintf("%s/collections/alerts/items/%d", base, hidden)
		if a := do(http.MethodGet, path, nil, basic(keys["state.admin"].Key)); a.code != http.StatusOK || !bytes.Contains(a.body, []byte(`"overdue"`)) {
			t.Fatalf("admin feature: HTTP %d %s", a.code, a.body)
		}
		if a := do(http.MethodGet, path, nil, basic(keys["division.officer"].Key)); a.code != http.StatusNotFound {
			t.Fatalf("officer reads a hidden alert: HTTP %d %s", a.code, a.body)
		}
		a := do(http.MethodGet, fmt.Sprintf("%s/collections/alerts/items/%d", base, officer[0]), nil, basic(keys["division.officer"].Key))
		var feature struct {
			ID         int64          `json:"id"`
			Properties map[string]any `json:"properties"`
		}
		if a.code != http.StatusOK || json.Unmarshal(a.body, &feature) != nil || feature.ID != officer[0] ||
			feature.Properties["division_code"] != "D1" {
			t.Fatalf("officer feature: HTTP %d %s", a.code, a.body)
		}
	})

	t.Run("bounding box", func(t *testing.T) {
		key := keys["state.admin"].Key
		var west, south, east, north float64
		var inside []int64
		err := pool.QueryRow(ctx, `
			SELECT ST_X(d.geom) - 0.001, ST_Y(d.geom) - 0.001, ST_X(d.geom) + 0.001, ST_Y(d.geom) + 0.001,
			       ARRAY(SELECT m2.detection_id FROM detection_matches m2 JOIN detections d2
			             ON (d2.id, d2.observed_at) = (m2.detection_id, m2.observed_at)
			             WHERE m2.workspace_id = m.workspace_id AND m2.observed_at > now() - interval '168 hours'
			               AND d2.geom && ST_Expand(d.geom, 0.001) ORDER BY 1)
			FROM detection_matches m JOIN detections d ON (d.id, d.observed_at) = (m.detection_id, m.observed_at)
			JOIN workspaces w ON w.id = m.workspace_id
			WHERE w.slug = 'demo' AND m.observed_at > now() - interval '168 hours' ORDER BY m.detection_id LIMIT 1`,
		).Scan(&west, &south, &east, &north, &inside)
		if err != nil {
			t.Fatal(err)
		}
		bbox := fmt.Sprintf("%f,%f,%f,%f", west, south, east, north)
		if got := ogcIDs(key, "detections", "bbox="+bbox+"&datetime="+since(168)); !slices.Equal(got, inside) || len(got) == 0 {
			t.Fatalf("bbox %s: got %v, want %v", bbox, got, inside)
		}
	})

	t.Run("published layers and units", func(t *testing.T) {
		var forest, exclusions, beats int64
		err := pool.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE l.role = 'forest'), count(*) FILTER (WHERE l.role = 'exclusion'),
			       (SELECT count(*) FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
			        JOIN hierarchy_levels level USING (hierarchy_id, depth)
			        WHERE h.workspace_id = w.id AND level.slug = 'beat' AND u.valid_to IS NULL)
			FROM workspaces w JOIN layers l ON l.workspace_id = w.id
			JOIN layer_versions v ON v.layer_id = l.id AND v.status = 'published'
			JOIN layer_features f ON f.layer_version_id = v.id
			WHERE w.slug = 'demo' GROUP BY w.id`).Scan(&forest, &exclusions, &beats)
		if err != nil {
			t.Fatal(err)
		}
		for _, user := range users[:3] {
			key := keys[user].Key
			for collection, want := range map[string]int64{"layer-forest": forest, "layer-exclusions": exclusions, "units-territorial-beat": beats, "units-territorial-section": 1} {
				if got := ogcIDs(key, collection, "limit=7"); int64(len(got)) != want {
					t.Fatalf("%s %s: %d features, want %d", user, collection, len(got), want)
				}
			}
		}
		a := do(http.MethodGet, base+"/collections", nil, basic(keys["beat.guard"].Key))
		var list struct {
			Collections []struct {
				ID    string    `json:"id"`
				Links []ogcLink `json:"links"`
			} `json:"collections"`
		}
		if a.code != http.StatusOK || json.Unmarshal(a.body, &list) != nil || len(list.Collections) != 10 {
			t.Fatalf("collections: HTTP %d %s", a.code, a.body)
		}
	})

	t.Run("service documents", func(t *testing.T) {
		key := keys["beat.guard"].Key
		for path, want := range map[string]string{
			base: `"service-desc"`, base + "/": `"conformance"`, base + "/conformance": "/conf/geojson",
			base + "/api": `"openapi":"3.0.3"`, base + "/collections/detections": `"items"`,
		} {
			if a := do(http.MethodGet, path, nil, basic(key)); a.code != http.StatusOK || !bytes.Contains(a.body, []byte(want)) {
				t.Fatalf("%s: HTTP %d %s", path, a.code, a.body)
			}
		}
		for path, want := range map[string]int{
			base + "/collections/nothing":                            http.StatusNotFound,
			base + "/collections/detections/items?unknown=1":         http.StatusBadRequest,
			base + "/collections/detections/items?queue=unit":        http.StatusBadRequest,
			base + "/collections/detections/items?bbox=1,2,3":        http.StatusBadRequest,
			base + "/collections/detections/items?datetime=tomorrow": http.StatusBadRequest,
			base + "/collections/detections/items/0":                 http.StatusNotFound,
			base + "/collections/detections/items?limit=5000":        http.StatusOK,
			"/ogc/workspaces/nowhere/collections":                    http.StatusForbidden,
		} {
			if a := do(http.MethodGet, path, nil, basic(key)); a.code != want {
				t.Errorf("%s: HTTP %d %s, want %d", path, a.code, a.body, want)
			}
		}
	})

	t.Run("refused keys", func(t *testing.T) {
		// A person without a role sees nothing, as on the map.
		for _, path := range []string{base, base + "/collections", base + "/collections/detections/items"} {
			if a := do(http.MethodGet, path, nil, basic(keys["no.role"].Key)); a.code != http.StatusForbidden {
				t.Fatalf("no.role %s: HTTP %d", path, a.code)
			}
		}
		if a := do(http.MethodGet, base, nil, nil); a.code != http.StatusUnauthorized || !strings.HasPrefix(a.header.Get("WWW-Authenticate"), "Basic ") {
			t.Fatalf("anonymous: HTTP %d %v", a.code, a.header)
		}
		for name, auth := range map[string]func(*http.Request){
			"malformed key": basic("not-a-key"),
			"unknown key":   header(accessKeyPrefix + strings.Repeat("A", 43)),
			"empty header":  header(""),
		} {
			if a := do(http.MethodGet, base, nil, auth); a.code != http.StatusUnauthorized {
				t.Errorf("%s: HTTP %d", name, a.code)
			}
		}
		// A key is not a sign-in for anything else.
		officer := keys["division.officer"].Key
		for _, path := range []string{"/api/v1/me", "/api/v1/workspaces/demo/alerts", "/api/v1/access-keys"} {
			for _, auth := range []func(*http.Request){basic(officer), header(officer)} {
				if a := do(http.MethodGet, path, nil, auth); a.code != http.StatusUnauthorized {
					t.Fatalf("key on %s: HTTP %d", path, a.code)
				}
			}
		}
		// The signed-in token also works on the GIS link.
		if a := do(http.MethodGet, base+"/collections", nil, bearer("division.officer")); a.code != http.StatusOK {
			t.Fatalf("bearer token: HTTP %d", a.code)
		}

		// An expired key.
		key, hash, hint, err := newAccessKey()
		if err != nil {
			t.Fatal(err)
		}
		var expiredID string
		if err := pool.QueryRow(ctx, `
			INSERT INTO personal_access_keys (user_id, name, key_hash, hint, created_at, expires_at)
			SELECT id, 'Expired test key', $2, $3, now() - interval '2 days', now() - interval '1 second'
			FROM users WHERE oidc_subject = $1 RETURNING id::text`, seedPrefix+"a1", hash, hint).Scan(&expiredID); err != nil {
			t.Fatal(err)
		}
		keys["expired"] = accessKey{ID: expiredID, Key: key}
		if a := do(http.MethodGet, base, nil, basic(key)); a.code != http.StatusUnauthorized {
			t.Fatalf("expired key: HTTP %d", a.code)
		}
		var listed struct {
			Items []accessKey `json:"items"`
		}
		a := do(http.MethodGet, "/api/v1/access-keys", nil, bearer("state.admin"))
		if a.code != http.StatusOK || json.Unmarshal(a.body, &listed) != nil {
			t.Fatalf("list: HTTP %d %s", a.code, a.body)
		}
		found := 0
		for _, k := range listed.Items {
			if k.Key != "" {
				t.Fatalf("the list shows a key: %+v", k)
			}
			if k.ID == expiredID && k.Expired {
				found++
			}
			if k.ID == keys["state.admin"].ID && !k.Expired && k.LastUsedAt != nil && k.Hint == keys["state.admin"].Key[6:12] {
				found++
			}
		}
		if found != 2 {
			t.Fatalf("list does not show the used key and the expired key: %s", a.body)
		}

		// Revoking: only the owner can, and the key stops working at once.
		guard := keys["beat.guard"]
		if a := do(http.MethodDelete, "/api/v1/access-keys/"+guard.ID, nil, bearer("division.officer")); a.code != http.StatusNotFound {
			t.Fatalf("revoking another person's key: HTTP %d", a.code)
		}
		if a := do(http.MethodGet, base, nil, basic(guard.Key)); a.code != http.StatusOK {
			t.Fatalf("key before revoking: HTTP %d", a.code)
		}
		if a := do(http.MethodDelete, "/api/v1/access-keys/"+guard.ID, nil, bearer("beat.guard")); a.code != http.StatusNoContent {
			t.Fatalf("revoke: HTTP %d %s", a.code, a.body)
		}
		if a := do(http.MethodDelete, "/api/v1/access-keys/"+guard.ID, nil, bearer("beat.guard")); a.code != http.StatusNotFound {
			t.Fatalf("second revoke: HTTP %d", a.code)
		}
		for _, auth := range []func(*http.Request){basic(guard.Key), header(guard.Key)} {
			if a := do(http.MethodGet, base+"/collections/detections/items", nil, auth); a.code != http.StatusUnauthorized {
				t.Fatalf("revoked key: HTTP %d", a.code)
			}
		}
		for _, body := range []map[string]any{{"name": ""}, {"name": "x", "expiresInDays": 0}, {"name": "x", "other": 1}} {
			if a := do(http.MethodPost, "/api/v1/access-keys", body, bearer("beat.guard")); a.code != http.StatusBadRequest {
				t.Fatalf("key request %v: HTTP %d", body, a.code)
			}
		}
		if a := do(http.MethodPost, "/api/v1/access-keys", map[string]any{"name": "x"}, header(officer)); a.code != http.StatusUnauthorized {
			t.Fatalf("key made with a key: HTTP %d", a.code)
		}
	})

	t.Run("audit", func(t *testing.T) {
		for _, user := range users[:3] {
			var created, used, revoked int
			err := pool.QueryRow(ctx, `
				SELECT count(*) FILTER (WHERE action = 'access_key.create' AND actor_kind = 'user' AND actor_id = $2),
				       count(*) FILTER (WHERE action = 'access_key.use' AND actor_kind = 'api_key' AND actor_id = $1
				                        AND details->>'owner' = $2 AND workspace_id IS NOT NULL),
				       count(*) FILTER (WHERE action = 'access_key.revoke' AND actor_kind = 'user' AND actor_id = $2)
				FROM audit_log WHERE target_type = 'access_key' AND target_id = $1`,
				keys[user].ID, seedPrefix+map[string]string{"state.admin": "a1", "division.officer": "a2", "beat.guard": "a3"}[user],
			).Scan(&created, &used, &revoked)
			wantRevoked := 0
			if user == "beat.guard" {
				wantRevoked = 1
			}
			// Use is recorded at most once a minute for each key.
			if err != nil || created != 1 || used < 1 || revoked != wantRevoked {
				t.Fatalf("%s audit: %d created, %d used, %d revoked: %v", user, created, used, revoked, err)
			}
		}
	})

	if image := os.Getenv("FIMRA_GIS_OGR_IMAGE"); image != "" {
		t.Run("ogrinfo", func(t *testing.T) {
			// ogrinfo reads every page with no time window, so it is compared
			// with the same collection read in full through the API.
			checkWithOGR(t, ctx, image, server.URL, map[string]string{
				"state.admin": keys["state.admin"].Key, "division.officer": keys["division.officer"].Key,
			}, func(user, collection string) int {
				return len(ogcIDs(keys[user].Key, collection, "limit=1000"))
			})
		})
	}
}

// checkWithOGR reads the test server with ogrinfo in a GDAL container on the
// host network, as a desktop GIS would. The state administrator uses HTTP
// Basic and the division officer the X-API-Key header.
func checkWithOGR(t *testing.T, ctx context.Context, image, serverURL string, keys map[string]string, want func(user, collection string) int) {
	service := "OAPIF:" + serverURL + "/ogc/workspaces/demo"
	script := `set -e
echo "### state.admin"
ogrinfo -ro -so -al -oo PAGE_SIZE=40 --config GDAL_HTTP_AUTH BASIC --config GDAL_HTTP_USERPWD "qgis:$ADMIN_KEY" "$SERVICE"
echo "### division.officer"
printf 'X-API-Key: %s\n' "$OFFICER_KEY" > /tmp/headers
ogrinfo -ro -so -al -oo PAGE_SIZE=40 --config GDAL_HTTP_HEADER_FILE /tmp/headers "$SERVICE"
`
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "host",
		"-e", "ADMIN_KEY", "-e", "OFFICER_KEY", "-e", "SERVICE", "--entrypoint", "sh", image, "-c", script)
	cmd.Env = append(os.Environ(), "ADMIN_KEY="+keys["state.admin"], "OFFICER_KEY="+keys["division.officer"], "SERVICE="+service)
	out, err := cmd.CombinedOutput()
	output := string(out)
	for _, key := range keys {
		output = strings.ReplaceAll(output, key, "<key>")
	}
	t.Logf("ogrinfo output:\n%s", output)
	if err != nil {
		t.Fatalf("ogrinfo: %v", err)
	}
	layer := regexp.MustCompile(`(?m)^Layer name: (\S+)$`)
	count := regexp.MustCompile(`(?m)^Feature Count: (\d+)$`)
	sections := strings.Split(output, "### ")
	if len(sections) != 3 {
		t.Fatalf("ogrinfo printed %d sections", len(sections)-1)
	}
	for _, section := range sections[1:] {
		user, body, _ := strings.Cut(section, "\n")
		names := layer.FindAllStringSubmatch(body, -1)
		counts := count.FindAllStringSubmatch(body, -1)
		if len(names) != 10 || len(counts) != len(names) {
			t.Fatalf("%s: ogrinfo found %d layers and %d counts", user, len(names), len(counts))
		}
		for i, name := range names {
			got, _ := strconv.Atoi(counts[i][1])
			if expected := want(user, name[1]); got != expected {
				t.Errorf("%s %s: ogrinfo counts %d, the API has %d", user, name[1], got, expected)
			}
		}
		for _, field := range []string{"forest_class: String", "beat_code: String", "queue: String", "overdue: Integer(Boolean)", "legal_status: String"} {
			if !strings.Contains(body, field) {
				t.Errorf("%s: ogrinfo shows no field %q", user, field)
			}
		}
	}
}
