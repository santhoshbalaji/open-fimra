package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

func checkAlertAccess(t *testing.T, ctx context.Context, tx pgx.Tx, slug, prefix string, request func(string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	base := "/api/v1/workspaces/" + slug + "/alerts"
	list := func(subject, query string) alertPage {
		t.Helper()
		w := request(http.MethodGet, base+query, subject)
		var page alertPage
		if w.Code != 200 {
			t.Fatalf("%s %s returned %d: %s", subject, query, w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	for _, tc := range []struct {
		subject            string
		unit, edge, triage int
		review             bool
	}{
		{"a1", 3, 1, 3, true}, {"a2", 1, 0, 1, true}, {"a3", 0, 0, 0, false},
	} {
		page := list(tc.subject, "")
		if len(page.Items) != tc.unit+tc.edge+tc.triage || page.Counts["unit"] != tc.unit || page.Counts["edge_review"] != tc.edge || page.Counts["triage"] != tc.triage || page.CanReview != tc.review {
			t.Fatalf("%s got %+v", tc.subject, page)
		}
		t.Logf("%s: unit %d, edge review %d, triage %d", tc.subject, tc.unit, tc.edge, tc.triage)
	}
	if w := request(http.MethodGet, base, "a4"); w.Code != 403 {
		t.Fatalf("no.role got %d", w.Code)
	}
	assertUnauthorized(t, request(http.MethodGet, base, ""), "Bearer")
	if page := list("a1", "?queue=unit&status=new&hours=24&class=inside&unitPath=ds.c1.d1"); len(page.Items) != 1 {
		t.Fatalf("combined filters returned %+v", page)
	}
	if page := list("a1", "?hours=1"); len(page.Items) != 0 {
		t.Fatal("hour filter ignored")
	}
	page := list("a1", "?limit=2")
	seen := map[string]bool{}
	var newest []string
	for {
		for _, row := range page.Items {
			var alert struct {
				ID       string `json:"id"`
				Queue    string `json:"queue"`
				BeatCode string `json:"beat_code"`
				Division string `json:"division_name"`
				Line     string `json:"place_hierarchy"`
				Places   map[string]struct{ Code, Name string }
			}
			if err := json.Unmarshal(row, &alert); err != nil {
				t.Fatal(err)
			}
			if alert.BeatCode != "" && (alert.Line != "territorial" || alert.Places["beat"].Code != alert.BeatCode ||
				alert.Places["division"].Name != alert.Division) {
				t.Fatalf("alert places do not match the levels: %s", row)
			}
			if seen[alert.ID] {
				t.Fatal("duplicate cursor row", alert.ID)
			}
			seen[alert.ID] = true
			newest = append(newest, alert.ID)
			if w := request(http.MethodGet, base+"/"+alert.ID, "a1"); w.Code != 200 {
				t.Fatalf("detail returned %d", w.Code)
			}
			if alert.Queue != "unit" {
				if w := request(http.MethodGet, base+"/"+alert.ID, "a3"); w.Code != 404 {
					t.Fatalf("beat read review detail: %d", w.Code)
				}
			}
		}
		if page.NextCursor == "" {
			break
		}
		page = list("a1", "?limit=2&cursor="+url.QueryEscape(page.NextCursor))
	}
	if len(seen) != 7 {
		t.Fatalf("cursor read %d alerts", len(seen))
	}
	var oldest []string
	for query := "?sort=oldest&limit=2"; ; {
		page = list("a1", query)
		for _, row := range page.Items {
			var item struct {
				ID         string `json:"id"`
				NextAction string `json:"next_action"`
			}
			if err := json.Unmarshal(row, &item); err != nil || item.NextAction == "" {
				t.Fatalf("alert lacks a next action: %s (%v)", row, err)
			}
			oldest = append(oldest, item.ID)
		}
		if page.NextCursor == "" {
			break
		}
		query = "?sort=oldest&limit=2&cursor=" + url.QueryEscape(page.NextCursor)
	}
	slices.Reverse(newest)
	if !slices.Equal(newest, oldest) {
		t.Fatalf("oldest pages = %v, want %v", oldest, newest)
	}
	for _, query := range []string{"?sort=invalid", "?queue=invalid", "?status=old", "?class=bad", "?unitPath=a..b", "?cursor=x", "?hours=0", "?limit=201", "/bad"} {
		if w := request(http.MethodGet, base+query, "a1"); w.Code != 400 {
			t.Fatalf("%s returned %d", query, w.Code)
		}
	}
	// Move the beat grant onto the shared boundary. The unit alert in B05
	// should appear but its shared-boundary neighbour needs a reviewer.
	beatTx, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := beatTx.Exec(ctx, `UPDATE role_assignments SET unit_path='ds.c1.d1.r2.b05'
	    WHERE workspace_id=$1 AND role_slug='field_staff'`, prefix+"01"); err != nil {
		t.Fatal(err)
	}
	beatPage := list("a3", "")
	if len(beatPage.Items) != 1 || beatPage.Counts["unit"] != 1 || beatPage.CanReview {
		t.Fatalf("B05 guard got %+v", beatPage)
	}
	if err := beatTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// The officer also serves as a field worker at the near-edge beat.
	// The unrelated division grant cannot grant review access here.
	if _, err := tx.Exec(ctx, `INSERT INTO role_assignments(user_id,workspace_id,hierarchy_id,unit_path,role_slug)
			SELECT user_id,workspace_id,hierarchy_id,'ds.c2.d3.r5.b14','field_staff' FROM role_assignments
			WHERE workspace_id=$1 AND role_slug='division_officer'`, prefix+"01"); err != nil {
		t.Fatal(err)
	}
	if len(list("a2", "?queue=edge_review").Items) != 0 {
		t.Fatal("review leaked across grants")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM role_assignments WHERE workspace_id=$1 AND unit_path='ds.c2.d3.r5.b14'`, prefix+"01"); err != nil {
		t.Fatal(err)
	}
}
