package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const modisHeader = "latitude,longitude,brightness,scan,track,acq_date,acq_time,satellite,instrument,confidence,version,bright_t31,frp,daynight"

// fakeFIRMS stands in for the FIRMS API in tests. It accepts one map key,
// answers data availability, and serves the demo fixture for area requests
// with its observation times moved to the last day.
type fakeFIRMS struct {
	server *httptest.Server
	key    string

	mu       sync.Mutex
	requests []string
}

func newFakeFIRMS(t *testing.T, key string) *fakeFIRMS {
	t.Helper()
	fixture, err := os.ReadFile("../../packages/connectors/firms/fixtures/demo.csv")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFIRMS{key: key}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.EscapedPath(), "/"), "/")
		f.mu.Lock()
		f.requests = append(f.requests, strings.Join(parts[:min(len(parts), 3)], "/"))
		f.mu.Unlock()
		// /api/area/csv/{key}/{product}/{box}/{days} and /api/data_availability/csv/{key}/{product}
		if len(parts) < 5 || parts[0] != "api" || parts[2] != "csv" {
			http.NotFound(w, r)
			return
		}
		key, _ := url.PathUnescape(parts[3])
		if key != f.key {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(w, "Invalid MAP_KEY.")
			return
		}
		switch parts[1] {
		case "data_availability":
			if parts[4] != firmsTestProduct {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, "data_id,min_date,max_date\n%s,2020-01-01,%s\n", parts[4], time.Now().UTC().Format(time.DateOnly))
		case "area":
			if parts[4] != "VIIRS_NOAA20_NRT" {
				// Other products answer with their column names and no records.
				header, _, _ := bytes.Cut(fixture, []byte("\n"))
				if strings.HasPrefix(parts[4], "MODIS") {
					header = []byte(modisHeader)
				}
				_, _ = w.Write(append(header, '\n'))
				return
			}
			_, _ = w.Write(shiftFixture(t, fixture, time.Now()))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// shiftFixture moves the fixture's times, which count from midnight UTC on
// January 8, 2000, so that they end at the current hour.
func shiftFixture(t *testing.T, fixture []byte, now time.Time) []byte {
	records, err := csv.NewReader(bytes.NewReader(fixture)).ReadAll()
	if err != nil {
		t.Error(err)
		return nil
	}
	anchor := time.Date(2000, 1, 8, 0, 0, 0, 0, time.UTC)
	base := now.UTC().Truncate(time.Hour)
	for _, row := range records[1:] {
		observed, err := time.Parse("2006-01-02 1504", row[5]+" "+row[6])
		if err != nil {
			t.Error(err)
			return nil
		}
		shifted := base.Add(observed.Sub(anchor))
		row[5], row[6] = shifted.Format(time.DateOnly), shifted.Format("1504")
		row[7] = "N20"
	}
	var out bytes.Buffer
	writer := csv.NewWriter(&out)
	_ = writer.WriteAll(records)
	return out.Bytes()
}

// transport sends FIRMS requests to the fake server instead.
func (f *fakeFIRMS) transport() http.RoundTripper {
	target, _ := url.Parse(f.server.URL)
	return roundTripper(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		clone.URL.Scheme, clone.URL.Host, clone.Host = target.Scheme, target.Host, target.Host
		return http.DefaultTransport.RoundTrip(clone)
	})
}

func (f *fakeFIRMS) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
