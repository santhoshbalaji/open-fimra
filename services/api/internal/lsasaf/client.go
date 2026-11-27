// Package lsasaf reads the EUMETSAT LSA SAF MSG-IODC FRP-PIXEL list product.
package lsasaf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"time"
)

const (
	Source     = "lsasaf"
	Product    = "SEVIRI_IODC_FRP"
	Endpoint   = "https://datalsasaf.lsasvcs.ipma.pt/PRODUCTS/MSG-IODC/FRP-PIXEL/HDF5"
	filePrefix = "HDF5_LSASAF_MSG-IODC_FRP-PIXEL-ListProduct_IODC-Disk_"
	stamp      = "200601021504"
	Cadence    = 15 * time.Minute
)

var filenames = regexp.MustCompile(filePrefix + `([0-9]{12})["'<>/[:space:]]`)

type Client struct {
	http                     *http.Client
	base, username, password string
}

func NewClient(base, username, password string, httpClient *http.Client) *Client {
	client := *httpClient
	// A redirect must never forward Basic Auth to a different URL.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &client, base: base, username: username, password: password}
}

func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/"+path, nil)
	if err != nil {
		return nil, errors.New("invalid LSA SAF request")
	}
	req.SetBasicAuth(c.username, c.password)
	res, err := c.http.Do(req)
	if err != nil {
		// Transport errors may include a URL or other request details.
		return nil, errors.New("LSA SAF request failed")
	}
	return res, nil
}

// List looks back across UTC day boundaries and returns the available slots in
// scan order. The current day's directory may not have been created yet.
func (c *Client) List(ctx context.Context, start, end time.Time) ([]time.Time, error) {
	var slots []time.Time
	var failures []error
	start, end = start.UTC(), end.UTC()
	for day := start.Truncate(24 * time.Hour); !day.After(end); day = day.AddDate(0, 0, 1) {
		found, err := c.listDay(ctx, day, start, end)
		slots = append(slots, found...)
		failures = append(failures, err)
	}
	slices.SortFunc(slots, time.Time.Compare)
	return slices.Compact(slots), errors.Join(failures...)
}

func (c *Client) listDay(ctx context.Context, day, start, end time.Time) ([]time.Time, error) {
	res, err := c.get(ctx, day.Format("2006/01/02")+"/")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LSA SAF listing: HTTP %d", res.StatusCode)
	}
	const maxListing = 4 << 20
	data, err := io.ReadAll(io.LimitReader(res.Body, maxListing+1))
	if err != nil || len(data) > maxListing {
		return nil, errors.New("could not read LSA SAF listing")
	}
	var slots []time.Time
	for _, match := range filenames.FindAllSubmatch(data, -1) {
		slot, err := time.Parse(stamp, string(match[1]))
		if err == nil && slot.Truncate(time.Hour*24).Equal(day) && slot.Equal(slot.Truncate(Cadence)) &&
			!slot.Before(start) && !slot.After(end) {
			slots = append(slots, slot)
		}
	}
	return slots, nil
}

var ErrMissing = errors.New("LSA SAF slot is missing")

func (c *Client) Fetch(ctx context.Context, slot time.Time, keep func(Pixel) bool) ([]Pixel, error) {
	slot = slot.UTC()
	res, err := c.get(ctx, slot.Format("2006/01/02")+"/"+filePrefix+slot.Format(stamp))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrMissing
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LSA SAF ListProduct: HTTP %d", res.StatusCode)
	}
	// The reader needs random access to a file. Keep the spool private and
	// bounded, and remove it on every exit.
	f, err := os.CreateTemp("", "fimra-lsasaf-*.h5")
	if err != nil {
		return nil, errors.New("could not create LSA SAF download spool")
	}
	defer os.Remove(f.Name())
	const maxProduct = 16 << 20
	n, copyErr := io.Copy(f, io.LimitReader(res.Body, maxProduct+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || n > maxProduct {
		return nil, errors.New("could not spool LSA SAF ListProduct")
	}
	return decode(f.Name(), slot, keep)
}
