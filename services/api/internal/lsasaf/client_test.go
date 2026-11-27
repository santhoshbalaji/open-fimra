package lsasaf

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListProduct(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "fixture" || pass != "synthetic-password" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/2026/11/26/":
			for _, minute := range []string{"0015", "0000", "0000", "0030", "0101"} {
				fmt.Fprintf(w, "<a href=\"%s20261126%s\">file</a>", filePrefix, minute)
			}
			fmt.Fprint(w, "<a href=\"HDF5_LSASAF_MSG-IODC_FRP-PIXEL-QualityProduct_IODC-Disk_202611260045\">quality</a>")
		case "/2026/11/26/" + filePrefix + "202611260000":
			http.ServeFile(w, r, "testdata/202611260000.h5")
		case "/2026/11/26/" + filePrefix + "202611260015":
			http.ServeFile(w, r, "testdata/202611260015.h5")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	at, _ := time.Parse(stamp, "202611260000")
	client := NewClient(server.URL, "fixture", "synthetic-password", server.Client())
	slots, err := client.List(t.Context(), at.Add(-time.Hour), at.Add(time.Hour))
	if err != nil || len(slots) != 3 || !slots[0].Equal(at) {
		t.Fatalf("list slots: %v, %v", slots, err)
	}
	pixels, err := client.Fetch(t.Context(), at, func(p Pixel) bool { return p.Observation.Latitude > 10 })
	if err != nil || len(pixels) != 1 {
		t.Fatalf("decode pixels: %v, %v", pixels, err)
	}
	p := pixels[0]
	o := p.Observation
	if o.NativeID != "202611260000:1291:3123" || !o.ObservedAt.Equal(at) || o.FRP == nil || *o.FRP != 42 ||
		o.Confidence != "78" || o.Raw["ACQTIME"] != "10" || o.Raw["OVERALL_QUALITY_FLAG"] != "OK" ||
		p.Footprint[0] != p.Footprint[4] || p.Footprint[0] == p.Footprint[1] {
		t.Fatalf("unexpected observation: %+v", p)
	}
	outside, err := client.Fetch(t.Context(), at, func(p Pixel) bool { return p.Observation.Latitude < 10 })
	if err != nil || len(outside) != 0 {
		t.Fatalf("excluded pixel: %v, %v", outside, err)
	}
	empty, err := client.Fetch(t.Context(), slots[1], nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("valid empty scan: %v, %v", empty, err)
	}
	if _, err := client.Fetch(t.Context(), slots[2], nil); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing file: %v", err)
	}
	bad := NewClient(server.URL, "fixture", "wrong-password", server.Client())
	if _, err := bad.List(t.Context(), at, at); err == nil || strings.Contains(err.Error(), "wrong-password") {
		t.Fatalf("authentication error: %v", err)
	}
}
