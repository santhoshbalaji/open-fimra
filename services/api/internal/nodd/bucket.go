// Package nodd reads the VIIRS Enterprise Fire I-band product from NOAA NODD.
package nodd

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

const Source = "noaa_nodd"
const prefix = "VIIRS_EFIRE_VIIRSI_EDR/"
const maxGranuleBytes = 32 << 20

type Satellite struct {
	Code, Product, Endpoint string
}

var Satellites = [...]Satellite{
	{"n20", "VIIRS_NOAA20_EFIRE", "https://noaa-nesdis-n20-pds.s3.amazonaws.com"},
	{"n21", "VIIRS_NOAA21_EFIRE", "https://noaa-nesdis-n21-pds.s3.amazonaws.com"},
}

type granule struct {
	Key, ID, ETag        string
	Start, End, Modified time.Time
	Size                 int64
}

var granuleName = regexp.MustCompile(`^VIIRS_EFIRE_VIIRSI_EDR/\d{4}/\d{2}/\d{2}/EFIRE-VIIRSI_v\d+r\d+_(n2[01])_s(\d{15})_e(\d{15})_c\d{15}\.nc$`)

func granuleTime(s string) (time.Time, error) {
	return time.Parse("20060102150405.0", s[:14]+"."+s[14:])
}

func list(ctx context.Context, client *http.Client, sat Satellite, start, end time.Time) ([]granule, error) {
	var out []granule
	for day := start.UTC().Truncate(24 * time.Hour); !day.After(end); day = day.AddDate(0, 0, 1) {
		query := url.Values{"list-type": {"2"}, "prefix": {prefix + day.Format("2006/01/02/")}}
		for {
			var page struct {
				Truncated bool   `xml:"IsTruncated"`
				Next      string `xml:"NextContinuationToken"`
				Objects   []struct {
					Key, ETag    string
					LastModified time.Time
					Size         int64
				} `xml:"Contents"`
				XMLName xml.Name `xml:"ListBucketResult"`
			}
			resp, err := get(ctx, client, sat.Endpoint+"/?"+query.Encode(), "", "")
			if err != nil {
				return nil, err
			}
			err = xml.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&page)
			resp.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("decode %s bucket listing: %w", sat.Code, err)
			}
			for _, item := range page.Objects {
				m := granuleName.FindStringSubmatch(item.Key)
				if m == nil || m[1] != sat.Code {
					continue
				}
				from, err := granuleTime(m[2])
				if err != nil {
					return nil, fmt.Errorf("parse %s start time: %w", item.Key, err)
				}
				to, err := granuleTime(m[3])
				if err != nil || !to.After(from) || to.Sub(from) > 3*time.Minute {
					return nil, fmt.Errorf("invalid granule end time in %s", item.Key)
				}
				if to.Before(start) || from.After(end) {
					continue
				}
				if item.Size <= 0 || item.Size > maxGranuleBytes {
					return nil, fmt.Errorf("invalid object size for %s: %d", item.Key, item.Size)
				}
				// Keep the identity stable if NOAA republishes the same granule.
				out = append(out, granule{item.Key, sat.Code + "_s" + m[2], item.ETag, from, to, item.LastModified, item.Size})
			}
			if !page.Truncated {
				break
			}
			if page.Next == "" || page.Next == query.Get("continuation-token") {
				return nil, errors.New("invalid bucket continuation token")
			}
			query.Set("continuation-token", page.Next)
		}
	}
	return out, nil
}

func get(ctx context.Context, client *http.Client, endpoint, byteRange, etag string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
		if etag != "" {
			req.Header.Set("If-Match", etag)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK || (byteRange != "" && resp.StatusCode == http.StatusPartialContent) {
		return resp, nil
	}
	resp.Body.Close()
	return nil, fmt.Errorf("GET %s: %s", req.URL.Path, resp.Status)
}

// objectReader lets the NetCDF reader seek to metadata and sparse pixel columns
// without fetching the multi-megabyte fire mask in every worldwide granule.
type objectReader struct {
	ctx    context.Context
	client *http.Client
	url    string
	etag   string
	size   int64
	pages  map[int64][]byte
	full   []byte
}

func (r *objectReader) ReadAt(p []byte, offset int64) (int, error) {
	const pageSize = 64 << 10
	n := 0
	for len(p) > 0 && offset < r.size {
		if r.full != nil {
			copied := copy(p, r.full[offset:])
			n += copied
			p = p[copied:]
			break
		}
		start := offset / pageSize * pageSize
		page := r.pages[start]
		if page == nil {
			end := min(start+pageSize, r.size)
			resp, err := get(r.ctx, r.client, r.url, fmt.Sprintf("bytes=%d-%d", start, end-1), r.etag)
			if err != nil {
				return n, err
			}
			expected := end - start
			if resp.StatusCode == http.StatusOK {
				expected = r.size
			}
			page, err = io.ReadAll(io.LimitReader(resp.Body, expected+1))
			resp.Body.Close()
			if err != nil {
				return n, err
			}
			if int64(len(page)) != expected {
				return n, io.ErrUnexpectedEOF
			}
			if resp.StatusCode == http.StatusOK {
				r.full = page
				continue
			}
			if resp.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", start, end-1, r.size) {
				return n, errors.New("unexpected granule Content-Range")
			}
			r.pages[start] = page
		}
		copied := copy(p, page[offset-start:])
		n += copied
		offset += int64(copied)
		p = p[copied:]
	}
	if len(p) != 0 {
		return n, io.EOF
	}
	return n, nil
}

type granuleReader struct{ *io.SectionReader }

func (granuleReader) Close() error { return nil }
