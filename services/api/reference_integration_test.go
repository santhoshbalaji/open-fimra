package main

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
)

func testWorkspaceGeography(t *testing.T, base string, client *http.Client, tokenURL string) {
	// A leaf grant sees the same published geography as an administrator.
	token := demoToken(t, client, tokenURL, "beat.guard")
	request := func(path, tag string, status int) ([]byte, http.Header) {
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
		if res.StatusCode != status {
			t.Fatalf("%s: HTTP %d: %s", path, res.StatusCode, body)
		}
		return body, res.Header
	}
	body, _ := request("", "", 200)
	var info struct {
		Boundary [4]float64
		Levels   []struct {
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
	token = demoToken(t, client, tokenURL, "no.role")
	request(fullState, tag, 403)
	request("", "", 403)
}
