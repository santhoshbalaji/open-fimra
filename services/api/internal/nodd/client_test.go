package nodd

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchGranules(t *testing.T) {
	const day = "2026/11/27/"
	const fireKey = prefix + day + "EFIRE-VIIRSI_v1r3_n20_s202611270001100_e202611270002350_c202611270038210.nc"
	const emptyKey = prefix + day + "EFIRE-VIIRSI_v1r3_n20_s202611270002350_e202611270004000_c202611270039210.nc"
	files := make(map[string][]byte)
	for key, name := range map[string]string{fireKey: "fires.nc", emptyKey: "empty.nc"} {
		data, err := os.ReadFile("../../../../packages/connectors/noaa-nodd/fixtures/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[key] = data
	}
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path[1:]
		if key != "" {
			if files[key] == nil || r.Header.Get("Range") == "" || r.Header.Get("Authorization") != "" {
				t.Errorf("unexpected object request %s", r.URL.Path)
				http.NotFound(w, r)
				return
			}
			downloads.Add(1)
			w.Header().Set("ETag", `"123"`)
			http.ServeContent(w, r, key, time.Time{}, bytes.NewReader(files[key]))
			return
		}
		query := r.URL.Query()
		if query.Get("list-type") != "2" {
			t.Error("expected ListObjectsV2")
		}
		type object struct {
			Key, LastModified, ETag string
			Size                    int
		}
		page := struct {
			XMLName               xml.Name `xml:"ListBucketResult"`
			IsTruncated           bool
			NextContinuationToken string
			Contents              []object
		}{}
		if query.Get("prefix") == prefix+day {
			key := fireKey
			if query.Get("continuation-token") == "next+token" {
				key = emptyKey
			} else {
				page.IsTruncated, page.NextContinuationToken = true, "next+token"
			}
			page.Contents = []object{{key, "2026-11-27T00:41:00Z", `"123"`, len(files[key])}}
		}
		if err := xml.NewEncoder(w).Encode(page); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	sat := Satellites[0]
	sat.Endpoint = server.URL
	end := time.Date(2026, 11, 27, 1, 0, 0, 0, time.UTC)
	boxes := []bounds{{West: 86, East: 87, South: 16, North: 17}, {West: 70, East: 71, South: 16, North: 17}}
	client := NewClient(server.Client())
	observationSets, err := client.Fetch(t.Context(), sat, end.Add(-2*time.Hour), end, boxes)
	if err != nil {
		t.Fatal(err)
	}
	if len(observationSets) != 2 || len(observationSets[0]) != 2 || len(observationSets[1]) != 0 {
		t.Fatalf("got observations %v", observationSets)
	}
	pixel, fill := observationSets[0][0], observationSets[0][1]
	if pixel.NativeID != "n20_s202611270001100:5:118" || pixel.Satellite != "N20" ||
		pixel.ObservedAt.Format(time.RFC3339) != "2026-11-27T00:01:10Z" ||
		pixel.DayNight != "D" || pixel.Confidence != "75" || pixel.Raw["FP_FireMask"] != "8" ||
		pixel.FRP == nil || *pixel.FRP != 11 || pixel.ScanMetres == nil || *pixel.ScanMetres != 500 ||
		fill.FRP != nil || fill.Raw["object_last_modified"] != "2026-11-27T00:41:00Z" {
		t.Fatalf("incorrect pixel metadata: %+v, %+v", pixel, fill)
	}
	before := downloads.Load()
	if _, err := client.Fetch(t.Context(), sat, end.Add(-2*time.Hour), end, boxes); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != before {
		t.Fatal("replay downloaded unchanged granules")
	}
}
