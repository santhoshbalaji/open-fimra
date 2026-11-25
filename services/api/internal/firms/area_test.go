package firms

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAreaFixtures(t *testing.T) {
	for _, tc := range []struct {
		product, file, satellite, instrument, confidence string
		observed                                         time.Time
		count                                            int
		scanMetres, frp                                  float64
	}{
		{"VIIRS_SNPP_NRT", "viirs.csv", "N", "VIIRS", "n", time.Date(2000, 1, 6, 21, 56, 0, 0, time.UTC), 2, 400, 6.5},
		{"MODIS_NRT", "modis.csv", "Terra", "MODIS", "76", time.Date(2000, 1, 7, 0, 45, 0, 0, time.UTC), 1, 1120, 10.5},
	} {
		t.Run(tc.product, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", "..", "packages", "connectors", "firms", "fixtures", tc.file)
			csv, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			const key = "test/key"
			area := NewArea(key, transportFunc(func(r *http.Request) (*http.Response, error) {
				want := areaURL + "test%2Fkey/" + tc.product + "/86.19,16.29,86.69,16.55/2"
				if r.Method != http.MethodGet || r.URL.String() != want {
					t.Fatalf("sent %s %s, want GET %s", r.Method, r.URL, want)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(csv)))}, nil
			}))
			got, err := area.Fetch(t.Context(), tc.product, Bounds{West: 86.19, South: 16.29, East: 86.69, North: 16.55})
			if err != nil || len(got) != tc.count {
				t.Fatalf("parsed %d records, want %d: %v", len(got), tc.count, err)
			}
			first := got[0]
			if first.Satellite != tc.satellite || first.Instrument != tc.instrument || first.Confidence != tc.confidence ||
				!first.ObservedAt.Equal(tc.observed) || math.Abs(*first.ScanMetres-tc.scanMetres) > 0.001 || *first.FRP != tc.frp {
				t.Fatalf("parsed unexpected observation: %+v", first)
			}
			formatted := strings.Replace(string(csv), first.Raw["latitude"], first.Raw["latitude"]+"0", 1)
			again, err := Parse(strings.NewReader(formatted), tc.product)
			if err != nil || first.SourceKey(Source, tc.product) != again[0].SourceKey(Source, tc.product) {
				t.Fatalf("equivalent coordinates changed the source key: %v", err)
			}
		})
	}
}

func TestAreaRedactsMapKey(t *testing.T) {
	const key = "test/private-key"
	area := NewArea(key, transportFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &urlEchoError{message: r.URL.String() + " " + key, cause: context.DeadlineExceeded}
	}))
	_, err := area.Fetch(t.Context(), Products[0], Bounds{})
	if !errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "test%2Fprivate-key") {
		t.Fatalf("expected a redacted error with its original cause, got %v", err)
	}
}

type urlEchoError struct {
	message string
	cause   error
}

func (e *urlEchoError) Error() string { return e.message }
func (e *urlEchoError) Unwrap() error { return e.cause }
