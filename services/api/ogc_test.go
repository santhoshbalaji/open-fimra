package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestReadOGCQuery(t *testing.T) {
	detections := ogcCollection{ID: "detections", kind: ogcDetections, timed: true, params: []string{"forest_class"}}
	alerts := ogcCollection{ID: "alerts", kind: ogcAlerts, timed: true, params: []string{"forest_class", "queue", "step"}}
	units := ogcCollection{ID: "units-territorial-beat", kind: ogcUnits}
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 31, 23, 59, 59, 0, time.UTC)
	for _, tc := range []struct {
		query string
		c     ogcCollection
		ok    bool
		check func(ogcQuery) bool
	}{
		{"", detections, true, func(q ogcQuery) bool { return q.limit == ogcDefaultLimit && q.bbox == nil && q.start == nil }},
		{"limit=5000", detections, true, func(q ogcQuery) bool { return q.limit == ogcMaxLimit }},
		{"limit=0", detections, false, nil},
		{"bbox=86,16,87,17", detections, true, func(q ogcQuery) bool { return len(q.bbox) == 4 && q.bbox[2] == 87 }},
		{"bbox=86,16,0,87,17,100", detections, true, func(q ogcQuery) bool { return q.bbox[1] == 16 && q.bbox[3] == 17 }},
		{"bbox=87,16,86,17", detections, false, nil},
		{"bbox=86,16,87", detections, false, nil},
		{"bbox=86,-91,87,17", detections, false, nil},
		{"datetime=2026-03-01T00:00:00Z", detections, true, func(q ogcQuery) bool { return q.start.Equal(start) && q.end.Equal(start) }},
		{"datetime=2026-03-01T00:00:00Z/..", detections, true, func(q ogcQuery) bool { return q.start.Equal(start) && q.end == nil }},
		{"datetime=../2026-03-31T23:59:59Z", detections, true, func(q ogcQuery) bool { return q.start == nil && q.end.Equal(end) }},
		{"datetime=/2026-03-31T23:59:59Z", detections, true, func(q ogcQuery) bool { return q.start == nil && q.end.Equal(end) }},
		{"datetime=2026-03-31T23:59:59Z/2026-03-01T00:00:00Z", detections, false, nil},
		{"datetime=..", detections, false, nil},
		{"datetime=2026-03-01", detections, false, nil},
		{"forest_class=inside,near_edge", detections, true, func(q ogcQuery) bool { return len(q.classes) == 2 }},
		{"forest_class=burnt", detections, false, nil},
		{"queue=triage&step=new", alerts, true, func(q ogcQuery) bool { return q.queue == "triage" && q.step == "new" }},
		{"queue=triage", detections, false, nil},
		{"queue=other", alerts, false, nil},
		{"forest_class=inside", units, false, nil},
		{"datetime=2026-03-01T00:00:00Z", units, true, nil},
		{"crs=" + url.QueryEscape(crs84) + "&bbox-crs=" + url.QueryEscape(crs84), units, true, nil},
		{"crs=EPSG:4326", units, false, nil},
		{"unknown=1", units, false, nil},
		{"limit=1&limit=2", units, false, nil},
		{"cursor=2026-03-01T00:00:00Z,42", detections, true, func(q ogcQuery) bool { return q.afterTime.Equal(start) && *q.afterID == 42 }},
		{"cursor=42", detections, false, nil},
		{"cursor=42", units, true, func(q ogcQuery) bool { return *q.afterID == 42 && q.afterTime == nil }},
		{"cursor=x", units, false, nil},
	} {
		values, err := url.ParseQuery(tc.query)
		if err != nil {
			t.Fatal(err)
		}
		q, err := readOGCQuery(values, tc.c)
		if (err == nil) != tc.ok {
			t.Errorf("%s on %s: error %v, want ok %v", tc.query, tc.c.ID, err, tc.ok)
			continue
		}
		if tc.ok && tc.check != nil && !tc.check(q) {
			t.Errorf("%s on %s: got %+v", tc.query, tc.c.ID, q)
		}
	}
}

func TestPropsKeepOrderAndLevels(t *testing.T) {
	p := props{{"b", 1}, {"a", nil}}
	p = append(p, levelProps([]string{"state", "beat"}, []byte(`{"beat":{"code":"B01","name":"Beat 01"}}`))...)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"b":1,"a":null,"state_code":null,"state_name":null,"beat_code":"B01","beat_name":"Beat 01"}`
	if string(data) != want {
		t.Fatalf("got %s, want %s", data, want)
	}
}

func TestAccessKeyForm(t *testing.T) {
	key, hash, hint, err := newAccessKey()
	if err != nil {
		t.Fatal(err)
	}
	if !looksLikeAccessKey(key) || len(hash) != 32 || key[len(accessKeyPrefix):len(accessKeyPrefix)+len(hint)] != hint ||
		string(hashAccessKey(key)) != string(hash) {
		t.Fatalf("key %q, hint %q", key, hint)
	}
	other, _, _, _ := newAccessKey()
	if other == key {
		t.Fatal("two keys are the same")
	}
	for _, bad := range []string{"", "fimra_", key[1:], key + "A", "other_" + key[len(accessKeyPrefix):], key[:len(key)-1] + "!"} {
		if looksLikeAccessKey(bad) {
			t.Errorf("%q looks like a key", bad)
		}
	}

	r := httptest.NewRequest("GET", "/ogc/workspaces/demo", nil)
	if _, found := gisAccessKey(r); found {
		t.Fatal("found a key in a request without one")
	}
	r.SetBasicAuth("anyone", key)
	if got, found := gisAccessKey(r); !found || got != key {
		t.Fatalf("Basic: %q %v", got, found)
	}
	r.Header.Set(accessKeyHeader, " "+other+" ")
	if got, found := gisAccessKey(r); !found || got != other {
		t.Fatalf("the header should win over Basic: %q %v", got, found)
	}
	r.Header.Add(accessKeyHeader, key)
	if got, found := gisAccessKey(r); !found || got != "" {
		t.Fatalf("two key headers should be refused: %q %v", got, found)
	}
	r = httptest.NewRequest("GET", "/ogc/workspaces/demo", nil)
	r.Header.Set("Authorization", "Bearer token")
	if _, found := gisAccessKey(r); found {
		t.Fatal("a bearer token is not a key")
	}
}
