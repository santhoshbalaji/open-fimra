package cdse

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

func fixtureGranule() Granule {
	g := Granule{
		Name:            "S3A_SL_2_FRP____20000102T140100_20000102T140400_20000102T154900_0179_146_333______MAR_O_NR_003.SEN3",
		PublicationDate: time.Date(2000, 1, 2, 15, 50, 0, 0, time.UTC),
	}
	g.S3Path = "/eodata/Sentinel-3/SLSTR/SL_2_FRP___/2000/01/02/" + g.Name
	g.ContentDate.Start = time.Date(2000, 1, 2, 14, 1, 0, 500_000_000, time.UTC)
	g.ContentDate.End = g.ContentDate.Start.Add(3 * time.Minute)
	return g
}

func TestCatalogueAndCSV(t *testing.T) {
	g := fixtureGranule()
	nt := g
	nt.Name = strings.Replace(nt.Name, "_NR_", "_NT_", 1)
	start, end := g.PublicationDate.Add(-time.Minute), g.PublicationDate.Add(time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Products":
			if r.Header.Get("Authorization") != "" {
				t.Error("public catalogue received authorization")
			}
			if r.URL.Query().Get("page") == "2" {
				json.NewEncoder(w).Encode(resultPage{Value: []Granule{g}})
				return
			}
			filter := r.URL.Query().Get("$filter")
			for _, term := range []string{"S3A_SL_2_FRP____", "contains(Name,'_NR_')", "PublicationDate ge " + start.Format(time.RFC3339), "PublicationDate lt " + end.Format(time.RFC3339), " or OData.CSC.Intersects"} {
				if !strings.Contains(filter, term) {
					t.Errorf("catalogue filter missing %q", term)
				}
			}
			json.NewEncoder(w).Encode(resultPage{Value: []Granule{nt, g}, Next: "?page=2"})
		case g.S3Path + "/" + File:
			if r.Header.Get("Authorization") != "fixture-signature" {
				t.Error("object request was not signed")
			}
			w.Header().Set("ETag", "fixture-etag")
			http.ServeFile(w, r, "testdata/fires.csv")
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(server.Client(), server.URL+"/Products", server.URL, func(req *http.Request) {
		req.Header.Set("Authorization", "fixture-signature")
	})
	areas := []firms.Bounds{
		{West: 86.20, South: 16.30, East: 86.31, North: 16.44},
		{West: 86.29, South: 16.31, East: 86.40, North: 16.44},
	}
	grans, err := client.Search(t.Context(), "S3A", start, end, areas)
	if err != nil || len(grans) != 1 || grans[0].Name != g.Name {
		t.Fatalf("search: %+v, %v", grans, err)
	}
	records, err := client.Download(t.Context(), grans[0])
	if err != nil || len(records) != 1 {
		t.Fatalf("download: %+v, %v", records, err)
	}
	o := records[0]
	if o.Satellite != "S3A" || o.Instrument != "SLSTR" || o.DayNight != "N" || o.Confidence != "92.5" ||
		o.ObservedAt.Format(time.RFC3339) != "2000-01-02T14:01:11Z" || *o.FRP != 18.5 ||
		*o.ScanMetres != 1200 || *o.TrackMetres != 1400 || o.Raw["etag"] != "fixture-etag" ||
		o.Raw["used_channel"] != "S7" || o.Raw["attribution"] != "Contains modified Copernicus Sentinel data 2000" {
		t.Fatalf("unexpected observation %+v", o)
	}
	data, err := os.ReadFile("testdata/fires.csv")
	if err != nil {
		t.Fatal(err)
	}
	edition := g
	edition.Name = strings.Replace(g.Name, "20000102T154900", "20000102T155100", 1)
	edition.Name = strings.Replace(edition.Name, "_003.SEN3", "_004.SEN3", 1)
	read, err := Parse(strings.NewReader(string(data)), edition, "")
	if err != nil || len(read) != 1 || read[0].NativeID != o.NativeID {
		t.Fatalf("new NR edition should keep native ID: %+v, %v", read, err)
	}
	body := string(data)
	empty := strings.Replace(body[:strings.LastIndex(strings.TrimSpace(body), "\n")+1], "fires = 1", "fires = 0", 1)
	if read, err := Parse(strings.NewReader(empty), g, ""); err != nil || len(read) != 0 {
		t.Fatalf("valid empty granule: %+v, %v", read, err)
	}
	for _, bad := range []string{
		strings.Replace(body, "fires = 1", "fires = 2", 1),
		strings.Replace(body, "18.5", "NaN", 1),
		strings.Replace(body, "14:01:11", "13:01:11", 1),
	} {
		if _, err := Parse(strings.NewReader(bad), g, ""); err == nil {
			t.Error("malformed CSV accepted")
		}
	}
}

func TestDownloadDoesNotEchoCredentialsOrFollowRedirects(t *testing.T) {
	const secret = "fixture-secret"
	g := fixtureGranule()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/unauthorized" {
			t.Error("signed download followed redirect")
		}
		w.Header().Set("Location", "/unauthorized?key="+secret)
		w.WriteHeader(http.StatusFound)
		fmt.Fprint(w, secret)
	}))
	defer server.Close()
	client := NewClient(server.Client(), server.URL, server.URL, func(req *http.Request) {
		req.Header.Set("Authorization", secret)
	})
	if _, err := client.Download(t.Context(), g); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("expected redacted download error: %v", err)
	}
}
