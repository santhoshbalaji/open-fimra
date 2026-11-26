package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"testing"
)

func testWorkspaceGeography(t *testing.T, base string, client *http.Client, tokenURL string) {
	// A leaf grant sees the same published geography as an administrator.
	token := demoToken(t, client, tokenURL, "beat.guard")
	request := func(path, tag string, statuses ...int) ([]byte, http.Header) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/api/v1/workspaces/demo"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if tag != "" {
			req.Header.Set("If-None-Match", tag)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(statuses, res.StatusCode) {
			t.Fatalf("%s: HTTP %d: %s", path, res.StatusCode, body)
		}
		return body, res.Header
	}
	body, _ := request("", "", 200)
	var info struct {
		Boundary     [4]float64
		Jurisdiction *[4]float64
		Levels       []struct {
			Depth  int
			Slug   string
			Labels map[string]string
		}
	}
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	if info.Boundary != [4]float64{86.195247, 16.293874, 86.686326, 16.548825} || len(info.Levels) != 5 ||
		info.Levels[4].Depth != 4 || info.Levels[4].Slug != "beat" || info.Levels[4].Labels["en"] != "Beat" || info.Levels[4].Labels["hi"] != "बीट" {
		t.Fatalf("unexpected workspace geography: %s", body)
	}
	beat := info.Jurisdiction
	info.Jurisdiction = nil
	token = demoToken(t, client, tokenURL, "division.officer")
	divisionBody, _ := request("", "", 200)
	if err := json.Unmarshal(divisionBody, &info); err != nil {
		t.Fatal(err)
	}
	division := info.Jurisdiction
	area := func(b [4]float64) float64 { return (b[2] - b[0]) * (b[3] - b[1]) }
	if beat == nil || division == nil || area(*beat) <= 0 || area(*beat) >= area(*division) || area(*division) >= area(info.Boundary) {
		t.Fatalf("invalid jurisdiction extents: beat %v, division %v, state %v", beat, division, info.Boundary)
	}
	token = demoToken(t, client, tokenURL, "state.admin")
	stateBody, _ := request("", "", 200)
	if err := json.Unmarshal(stateBody, &info); err != nil || info.Jurisdiction != nil {
		t.Fatalf("root grant should fit the workspace: %s (%v)", stateBody, err)
	}
	token = demoToken(t, client, tokenURL, "beat.guard")
	const fullState = "/tiles/reference/6/47/29.mvt"
	var tag string
	for _, tc := range []struct {
		path   string
		counts map[string]int
	}{
		{fullState, map[string]int{"units": 39, "forest": 5}},
		{"/tiles/reference/10/757/464.mvt", map[string]int{"units": 26, "forest": 2, "exclusions": 1}},
		{fullState + "?outline=1", map[string]int{"units": 1}},
	} {
		body, headers := request(tc.path, "", 200)
		if headers.Get("Content-Type") != tileType || headers.Get("Cache-Control") != "private, no-cache" || headers.Get("ETag") == "" {
			t.Fatalf("unexpected tile headers: %v", headers)
		}
		layers := decodeTileLayers(t, body)
		counts := make(map[string]int)
		for name, features := range layers {
			counts[name] = len(features)
		}
		if !reflect.DeepEqual(counts, tc.counts) {
			t.Fatalf("%s: got %v, want %v", tc.path, counts, tc.counts)
		}
		if tc.path == fullState {
			tag = headers.Get("ETag")
		}
		request(tc.path, headers.Get("ETag"), 304)
	}
	// This tile grid covers the demo workspace, including polygons clipped into
	// several tiles. Each beat's label anchor must appear exactly once.
	labels := make(map[string]bool)
	for x := 1514; x <= 1517; x++ {
		for y := 928; y <= 930; y++ {
			body, _ := request(fmt.Sprintf("/tiles/reference/11/%d/%d.mvt", x, y), "", 200, 204)
			for _, feature := range decodeTileLayers(t, body)["unit_labels"] {
				if feature["depth"] != uint64(4) {
					continue
				}
				code := feature["code"].(string)
				if labels[code] {
					t.Fatalf("beat label %q appeared in more than one tile", code)
				}
				labels[code] = true
			}
		}
	}
	if len(labels) != 24 {
		t.Fatalf("got %d beat labels, want 24", len(labels))
	}
	offline, headers := request("/offline-reference", "", 200)
	var reference struct {
		Type     string
		Features []struct {
			Geometry   json.RawMessage
			Properties struct{ Kind, Code string }
		}
	}
	if err := json.Unmarshal(offline, &reference); err != nil || reference.Type != "FeatureCollection" ||
		headers.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("offline map: %s (%v)", offline, err)
	}
	units := 0
	for _, feature := range reference.Features {
		if len(feature.Geometry) == 0 {
			t.Fatalf("offline feature has no geometry: %+v", feature)
		}
		if feature.Properties.Kind == "units" {
			units++
			if feature.Properties.Code != "B01" {
				t.Fatalf("offline map contains a unit outside the beat grant: %+v", feature.Properties)
			}
		}
	}
	if units != 1 {
		t.Fatalf("offline map should include the assigned beat: %s", offline)
	}
	token = demoToken(t, client, tokenURL, "no.role")
	request(fullState, tag, 403)
	request("", "", 403)
	request("/offline-reference", "", 403)
}
