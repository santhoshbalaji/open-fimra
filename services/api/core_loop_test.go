package main

import (
	"context"
	"crypto"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

const (
	fixtureDir = "../../packages/connectors/firms/fixtures"
	seedPrefix = "d0000000-0000-4000-8000-0000000000"
)

func TestCoreLoopIntegration(t *testing.T) {
	if os.Getenv("FIMRA_API_INTEGRATION") != "1" {
		t.Skip("Run make api-test-integration with the development stack running and migrated.")
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
	now := time.Now().UTC()
	t.Run("reference matching", func(t *testing.T) {
		checkReferenceMatches(t, ctx, tx, slug, "reference_"+suffix)
	})
	t.Run("multi-month feed replay", func(t *testing.T) {
		checkReplay(t, ctx, tx, prefix+"01", "replay_"+suffix, now)
	})
	t.Run("tile permissions and filters", func(t *testing.T) {
		checkTiles(t, ctx, tx, slug, prefix, "demo_"+suffix, now)
	})
}

// loadSeedCopy loads a copy of the demo seed inside tx, with its own slug and
// IDs. A shared database may already contain the demo workspace, its role
// grants and map detections. IDs start with prefix, and the workspace ID is
// prefix + "01".
func loadSeedCopy(t *testing.T, ctx context.Context, tx pgx.Tx) (suffix, slug, prefix string) {
	t.Helper()
	suffix = fmt.Sprintf("%08x", rand.Uint32())
	slug = "demo-test-" + suffix
	prefix = suffix + seedPrefix[8:]
	for _, file := range []string{"load.sql", "roles.sql"} {
		body, err := os.ReadFile(filepath.Join("..", "..", "seed", "demo-state", file))
		if err != nil {
			t.Fatal(err)
		}
		seed := strings.ReplaceAll(string(body), seedPrefix, prefix)
		seed = strings.ReplaceAll(seed, "'demo'", "'"+slug+"'")
		if _, err := tx.Exec(ctx, seed); err != nil {
			t.Fatalf("load test seed %s: %v", file, err)
		}
	}
	return suffix, slug, prefix
}

func checkReferenceMatches(t *testing.T, ctx context.Context, tx pgx.Tx, workspace, source string) {
	t.Helper()
	if err := execSeed(ctx, tx, "reference.sql"); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, "SELECT id, ST_X(geom), ST_Y(geom) FROM reference_detections")
	if err != nil {
		t.Fatal(err)
	}
	observations, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (firms.Observation, error) {
		o := firms.Observation{Satellite: "N", Instrument: "VIIRS", ObservedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}
		var id string
		err := row.Scan(&id, &o.Longitude, &o.Latitude)
		o.Raw = map[string]string{"reference_id": id}
		return o, err
	})
	if err != nil {
		t.Fatal(err)
	}
	read := func(context.Context) ([]firms.Observation, error) { return observations, nil }
	run := ingestRun{Source: source, Product: demoProduct}
	counts, err := runIngest(ctx, tx, run, read)
	if err != nil || counts.Inserted != 11 {
		t.Fatalf("insert reference detections: %+v, %v", counts, err)
	}

	// A replay must keep the original alert IDs, even if routing settings change.
	alertIDs := func() []int64 {
		var ids []int64
		err := tx.QueryRow(ctx, `SELECT array_agg(a.id ORDER BY a.id) FROM alerts a
			JOIN workspaces w ON w.id = a.workspace_id
			JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
			WHERE w.slug=$1 AND d.source=$2`, workspace, source).Scan(&ids)
		if err != nil || len(ids) != 7 {
			t.Fatalf("expected seven alert IDs, got %v: %v", ids, err)
		}
		return ids
	}
	original := alertIDs()
	if _, err := tx.Exec(ctx, "UPDATE workspaces SET review_near_edge=false WHERE slug=$1", workspace); err != nil {
		t.Fatal(err)
	}
	replay, err := runIngest(ctx, tx, run, read)
	if err != nil || replay.Duplicates != 11 || !slices.Equal(original, alertIDs()) {
		t.Fatalf("reference alert replay: %+v, %v", replay, err)
	}

	rows, err = tx.Query(ctx, `
	    SELECT r.id
	    FROM reference_detections r
	    JOIN workspaces w ON w.slug = $1
	    LEFT JOIN detections d ON d.source = $2 AND d.raw->>'reference_id' = r.id
	    LEFT JOIN detection_matches m ON m.workspace_id = w.id AND (m.detection_id, m.observed_at) = (d.id, d.observed_at)
	    LEFT JOIN alerts a ON a.workspace_id=w.id AND a.detection_id=d.id
	    LEFT JOIN layer_features f ON f.id = m.forest_feature_id
	    WHERE d.id IS NULL OR m.forest_class IS DISTINCT FROM r.expected_forest_class
	       OR a.queue IS DISTINCT FROM r.expected_queue
	       OR (a.id IS NOT NULL AND a.hierarchy IS DISTINCT FROM m.hierarchy)
	       OR (a.queue IN ('edge_review','unit') AND (a.unit_path IS DISTINCT FROM r.expected_beats[1]
	            OR NOT (m.hierarchy_unit_paths ? a.hierarchy_id::text)))
	       OR (a.queue='triage' AND a.unit_path IS NOT NULL)
	       OR m.triage_reason IS DISTINCT FROM r.expected_triage
	       OR f.code IS DISTINCT FROM r.expected_forest_code
	       OR ARRAY(SELECT p FROM unnest(m.unit_paths) p WHERE nlevel(p) = 5 ORDER BY p)
	        <> ARRAY(SELECT p FROM unnest(r.expected_beats) p ORDER BY p)
	       OR (m.forest_class = 'inside' AND (m.forest_feature_id IS NULL OR m.forest_distance_m IS DISTINCT FROM 0::real))
	       OR (m.forest_class = 'near_edge' AND (m.forest_distance_m IS NULL OR m.forest_distance_m NOT BETWEEN 1 AND 500))
	       OR (m.forest_class IS NOT NULL AND cardinality(m.layer_version_ids) <> 3)
	       OR (r.id = 'inside' AND (cardinality(m.unit_paths) <> 5 OR
	            m.hierarchy #>> '{territorial,beat,code}' IS DISTINCT FROM 'B05'))
	`, workspace, source)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(bad) != 0 {
		t.Fatalf("reference cases that did not match: %v (%v)", bad, err)
	}
	t.Log("11 reference detections matched every expected class, beat, triage reason, forest code and queue; replay kept all 7 alert IDs")
	run.Source += "_direct"
	if _, err := runIngest(ctx, tx, run, read); err != nil {
		t.Fatal(err)
	}
	var queue, path string
	err = tx.QueryRow(ctx, `SELECT a.queue, a.unit_path::text FROM alerts a JOIN workspaces w ON w.id=a.workspace_id
	    JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
	    WHERE w.slug=$1 AND d.source=$2 AND d.raw->>'reference_id'='near-edge'`, workspace, source+"_direct").Scan(&queue, &path)
	if err != nil || queue != "unit" || path != "ds.c2.d3.r5.b14" {
		t.Fatalf("direct edge route: %s %s %v", queue, path, err)
	}
	if _, err := tx.Exec(ctx, "UPDATE workspaces SET review_near_edge=true WHERE slug=$1", workspace); err != nil {
		t.Fatal(err)
	}
}

func checkReplay(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, source string, now time.Time) {
	t.Helper()
	run := ingestRun{Source: source, Product: demoProduct, WorkspaceID: &workspaceID}
	read := func(context.Context) ([]firms.Observation, error) {
		return readFixture(filepath.Join(fixtureDir, "viirs.csv"), run.Product, false, now)
	}
	for i, want := range []ingestCounts{{Fetched: 2, Inserted: 2}, {Fetched: 2, Duplicates: 2}} {
		got, err := runIngest(ctx, tx, run, read)
		if err != nil || got.Fetched != want.Fetched || got.Inserted != want.Inserted || got.Duplicates != want.Duplicates ||
			(i == 0 && got.Matched < 2) || (i == 1 && got.Matched != 0) {
			t.Fatalf("ingest %d: counts %+v, %v", i+1, got, err)
		}
	}
	var detections, matches, history, partitions int
	err := tx.QueryRow(ctx, `
	    WITH feed AS (SELECT id, observed_at, tableoid FROM detections WHERE source = $1)
	    SELECT (SELECT count(*) FROM feed),
	           (SELECT count(*) FROM feed JOIN detection_matches m USING (observed_at)
	            WHERE feed.id = m.detection_id AND m.workspace_id = $2::uuid),
	           (SELECT count(*) FROM ingest_runs WHERE source = $1 AND workspace_id = $2::uuid
	            AND status = 'succeeded' AND finished_at >= started_at AND fetched = 2
	            AND window_start = '2000-01-06 21:56+00' AND window_end = '2000-02-01 04:02+00'
	            AND ((inserted = 2 AND duplicates = 0) OR (inserted = 0 AND duplicates = 2))),
	           (SELECT count(DISTINCT tableoid) FROM feed)
	`, source, workspaceID).Scan(&detections, &matches, &history, &partitions)
	if err != nil || detections != 2 || matches != 2 || history != 2 || partitions != 2 {
		t.Fatalf("replay left %d detections, %d matches, %d recorded runs and %d partitions: %v",
			detections, matches, history, partitions, err)
	}
	t.Log("replay fetched 2, inserted 0, duplicated 2, created 0 matches")
	t.Log("two monthly partitions and two successful runs recorded")

	run.Source += "_fail"
	_, err = runIngest(ctx, tx, run, func(ctx context.Context) ([]firms.Observation, error) {
		observations, err := read(ctx)
		if err == nil {
			observations[1].DayNight = "X"
		}
		return observations, err
	})
	if err == nil {
		t.Fatal("invalid batch was accepted")
	}
	err = tx.QueryRow(ctx, `
	    SELECT (SELECT count(*) FROM detections WHERE source = $1),
	           (SELECT count(*) FROM ingest_runs WHERE source = $1 AND status = 'failed'
	            AND fetched = 2 AND inserted = 0 AND error_message LIKE '%check constraint%')
	`, run.Source).Scan(&detections, &history)
	if err != nil || detections != 0 || history != 1 {
		t.Fatalf("failed batch left %d detections and %d recorded failures: %v", detections, history, err)
	}
}

func checkTiles(t *testing.T, ctx context.Context, tx pgx.Tx, slug, prefix, source string, now time.Time) {
	t.Helper()
	// A different hierarchy has paths spelled like D1, but covers a D3 point.
	// The division officer's grant must stay in the original hierarchy.
	const addHierarchy = `
	    INSERT INTO hierarchies (id, workspace_id, slug, name, kind)
	    VALUES ($1, $2, 'z-alternate', 'Alternate', 'other');
	    INSERT INTO hierarchy_levels (hierarchy_id, depth, slug, labels)
	    SELECT $1, depth, slug, '{"en":"Alternate"}' FROM hierarchy_levels WHERE hierarchy_id = $3 AND depth < 3;
	    INSERT INTO units (hierarchy_id, depth, code, name, path, geom)
	    SELECT $1, depth, code, name, path, ST_Multi(ST_Buffer(ST_SetSRID(ST_Point(86.457995, 16.418498), 4326), 0.001))
	    FROM units WHERE hierarchy_id = $3 AND path @> 'ds.c1.d1'::ltree;
	`
	if _, err := tx.Exec(ctx, addHierarchy, pgx.QueryExecModeSimpleProtocol, prefix+"50", prefix+"01", prefix+"10"); err != nil {
		t.Fatal(err)
	}

	counts, err := runIngest(ctx, tx, ingestRun{Source: source, Product: demoProduct}, func(context.Context) ([]firms.Observation, error) {
		return readFixture(filepath.Join(fixtureDir, "demo.csv"), demoProduct, true, now)
	})
	if err != nil || counts.Inserted != 11 {
		t.Fatalf("load demo detections: %+v, %v", counts, err)
	}
	key, signer := testSigner(t)
	const issuer, audience = "https://issuer.invalid", "open-fimra-api"
	auth := authenticator{issuer: issuer, verifier: oidc.NewVerifier(issuer,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{ClientID: audience})}
	handler := api{reader: tx, logger: slog.Default()}.handler(auth, devOrigins)
	const origin = "http://localhost:4200"
	path := "/api/v1/workspaces/" + slug + "/tiles/detections/8/189/116.mvt"
	request := func(method, path, subject string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Origin", origin)
		if method == http.MethodOptions {
			r.Header.Set("Access-Control-Request-Method", "GET")
			r.Header.Set("Access-Control-Request-Headers", "authorization")
		}
		if subject != "" {
			token := signedToken(t, signer, jwt.Claims{Issuer: issuer, Subject: prefix + subject,
				Audience: jwt.Audience{audience}, Expiry: jwt.NewNumericDate(now.Add(time.Minute))})
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	t.Run("alert permissions, counts, filters and pagination", func(t *testing.T) {
		checkAlertAccess(t, ctx, tx, slug, prefix, request)
	})

	if w := request(http.MethodOptions, path, ""); w.Code != http.StatusNoContent ||
		w.Header().Get("Access-Control-Allow-Origin") != origin ||
		w.Header().Get("Access-Control-Allow-Headers") != "Authorization" {
		t.Fatalf("preflight got HTTP %d, headers %v", w.Code, w.Header())
	}
	assertUnauthorized(t, request(http.MethodGet, path, ""), "Bearer")
	const all = "?classes=inside,near_edge,outside,fixed_heat_source"
	for _, tc := range []struct {
		user, subject string
		all, defaults int
	}{
		{"state.admin", "a1", 11, 8},
		{"division.officer", "a2", 4, 2},
		{"beat.guard", "a3", 1, 0},
		{"no.role", "a4", 0, 0},
	} {
		for _, view := range []struct {
			filter string
			count  int
		}{{all, tc.all}, {"", tc.defaults}} {
			w := request(http.MethodGet, path+view.filter, tc.subject)
			status := http.StatusOK
			switch {
			case tc.subject == "a4":
				status = http.StatusForbidden
			case view.count == 0:
				status = http.StatusNoContent
			}
			if w.Code != status {
				t.Fatalf("%s %s got HTTP %d, want %d: %s", tc.user, view.filter, w.Code, status, w.Body)
			}
			if status != http.StatusForbidden && w.Header().Get("Content-Type") != tileType {
				t.Fatalf("%s got content type %q, want %s", tc.user, w.Header().Get("Content-Type"), tileType)
			}
			if w.Header().Get("Access-Control-Allow-Origin") != origin {
				t.Fatalf("%s missing access control header", tc.user)
			}
			if status != http.StatusOK {
				t.Logf("%s %s HTTP %d, 0 features", tc.user, view.filter, w.Code)
				continue
			}
			features := tileFeatures(t, w.Body.Bytes())
			if len(features) != view.count {
				t.Fatalf("%s %s got %d features, want %d", tc.user, view.filter, len(features), view.count)
			}
			t.Logf("%s %s HTTP %d, %d features", tc.user, view.filter, w.Code, len(features))
			ages := make([]uint64, 0, 4)
			var noUnit bool
			for _, f := range features {
				if f["id"] == uint64(0) || f["id"] == nil || f["observed_at"] == nil || f["source"] != source ||
					f["product"] != demoProduct || f["confidence"] == nil || f["frp_mw"] == nil {
					t.Fatalf("incomplete tile feature: %v", f)
				}
				age, ok := f["age"].(uint64)
				if !ok || age > 3 {
					t.Fatalf("invalid age: %v", f)
				}
				if !slices.Contains(ages, age) {
					ages = append(ages, age)
				}
				noUnit = noUnit || f["triage"] == "no_unit"
				if tc.subject == "a2" && f["division_name"] != "Hill Division" {
					t.Fatalf("division officer can see a detection in %v", f["division_name"])
				}
				if tc.subject == "a3" && f["beat_code"] != "B01" {
					t.Fatalf("beat guard can see a detection in %v", f["beat_code"])
				}
			}
			if tc.subject == "a1" && (len(ages) != 4 || !noUnit) {
				t.Fatalf("state admin tile ages %v, no_unit visible = %t", ages, noUnit)
			}
		}
	}
	if w := request(http.MethodGet, path+"?hours=5&classes=inside", "a1"); w.Code != http.StatusOK ||
		len(tileFeatures(t, w.Body.Bytes())) != 1 {
		t.Fatalf("hours and class filter returned HTTP %d", w.Code)
	}
	for _, bad := range []string{"?hours=721", "?classes=unknown"} {
		if w := request(http.MethodGet, path+bad, "a1"); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid tile filter returned HTTP %d", w.Code)
		}
	}
}
