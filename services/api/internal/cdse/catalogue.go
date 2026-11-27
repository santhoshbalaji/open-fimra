// Package cdse reads Sentinel-3 SLSTR near-real-time fire detections.
package cdse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

const (
	Source      = "cdse"
	File        = "FRP_MWIR1km_standard.csv"
	Catalogue   = "https://catalogue.dataspace.copernicus.eu/odata/v1/Products"
	ObjectStore = "https://eodata.dataspace.copernicus.eu"
	maxCSVBytes = 8 << 20
)

var Satellites = [...]string{"S3A", "S3B"}

func Product(satellite string) string { return "SLSTR_" + satellite + "_FRP" }

type Granule struct {
	ID, Name, S3Path string
	ContentDate      struct{ Start, End time.Time }
	PublicationDate  time.Time
	GeoFootprint     json.RawMessage
}

var namePattern = regexp.MustCompile(`^(S3[AB])_SL_2_FRP____([0-9]{8}T[0-9]{6})_[0-9A-Z_]+_NR_[0-9]{3}\.SEN3$`)

type Client struct {
	http               *http.Client
	catalogue, objects string
	sign               func(*http.Request)
}

func NewClient(httpClient *http.Client, catalogue, objects string, sign func(*http.Request)) *Client {
	client := *httpClient
	// Keep signed requests at the configured endpoint.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &client, catalogue: catalogue, objects: objects, sign: sign}
}

// Search uses the union of the workspace envelopes as a coarse catalogue
// filter. Limit the number per request to keep URLs within upstream limits.
func (c *Client) Search(ctx context.Context, satellite string, start, end time.Time, areas []firms.Bounds) ([]Granule, error) {
	var granules []Granule
	seen := make(map[string]bool)
	for batch := range slices.Chunk(areas, 12) {
		var regions []string
		for _, a := range batch {
			regions = append(regions, fmt.Sprintf("OData.CSC.Intersects(area=geography'SRID=4326;POLYGON((%f %f,%f %f,%f %f,%f %f,%f %f))')",
				a.West, a.South, a.East, a.South, a.East, a.North, a.West, a.North, a.West, a.South))
		}
		first, err := url.Parse(c.catalogue)
		if err != nil {
			return nil, errors.New("invalid CDSE catalogue endpoint")
		}
		q := first.Query()
		q.Set("$filter", fmt.Sprintf("startswith(Name,'%s_SL_2_FRP____') and contains(Name,'_NR_') and PublicationDate ge %s and PublicationDate lt %s and (%s)",
			satellite, start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano), strings.Join(regions, " or ")))
		q.Set("$select", "Id,Name,S3Path,ContentDate,PublicationDate,GeoFootprint")
		q.Set("$orderby", "PublicationDate asc")
		q.Set("$top", "100")
		first.RawQuery = q.Encode()
		pages := make(map[string]bool)
		for next := first; next != nil; {
			if pages[next.String()] {
				return granules, errors.New("CDSE catalogue repeated a result page")
			}
			pages[next.String()] = true
			page, err := c.page(ctx, next.String())
			if err != nil {
				return granules, err
			}
			for _, g := range page.Value {
				name := namePattern.FindStringSubmatch(g.Name)
				if name == nil || name[1] != satellite {
					continue
				}
				if g.PublicationDate.IsZero() || g.S3Path == "" {
					return granules, errors.New("CDSE catalogue omitted granule metadata")
				}
				if g.PublicationDate.Before(start) || !g.PublicationDate.Before(end) {
					continue
				}
				if !seen[g.S3Path] {
					granules = append(granules, g)
					seen[g.S3Path] = true
				}
			}
			if page.Next == "" {
				break
			}
			next, err = next.Parse(page.Next)
			if err != nil || next.Scheme != first.Scheme || next.Host != first.Host || next.Path != first.Path || next.User != nil {
				return granules, errors.New("invalid CDSE catalogue pagination link")
			}
		}
	}
	return granules, nil
}

type resultPage struct {
	Value []Granule `json:"value"`
	Next  string    `json:"@odata.nextLink"`
}

func (c *Client) page(ctx context.Context, target string) (resultPage, error) {
	var page resultPage
	res, err := c.get(ctx, target, "catalogue", false)
	if err != nil {
		return page, err
	}
	defer res.Body.Close()
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&page); err != nil || page.Value == nil {
		return page, errors.New("invalid CDSE catalogue response")
	}
	return page, nil
}

func (c *Client) Download(ctx context.Context, g Granule) ([]firms.Observation, error) {
	const prefix = "/eodata/Sentinel-3/SLSTR/SL_2_FRP___/"
	if !strings.HasPrefix(g.S3Path, prefix) || path.Clean(g.S3Path) != g.S3Path || path.Base(g.S3Path) != g.Name {
		return nil, errors.New("invalid CDSE granule object path")
	}
	target, err := url.Parse(c.objects)
	if err != nil {
		return nil, errors.New("invalid CDSE object endpoint")
	}
	target.Path = strings.TrimRight(target.Path, "/") + g.S3Path + "/" + File
	res, err := c.get(ctx, target.String(), "object store", true)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	reader := &io.LimitedReader{R: res.Body, N: maxCSVBytes + 1}
	observations, err := Parse(reader, g, res.Header.Get("ETag"))
	if reader.N == 0 {
		return nil, errors.New("CDSE fire CSV exceeds size limit")
	}
	return observations, err
}

func (c *Client) get(ctx context.Context, target, service string, signed bool) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid CDSE %s request", service)
	}
	if signed {
		c.sign(req)
	}
	res, err := c.http.Do(req)
	if err != nil {
		// Transport errors can include request URLs. Service errors can echo
		// authorization headers in the body. Keep only the operation/status.
		return nil, fmt.Errorf("CDSE %s request failed", service)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("CDSE %s returned HTTP %d", service, res.StatusCode)
	}
	return res, nil
}
