package nodd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
	"golang.org/x/sync/errgroup"
)

type Client struct {
	http  *http.Client
	cache map[string]cached
}

type cached struct {
	etag string
	data decoded
}

func NewClient(httpClient *http.Client) *Client {
	return &Client{http: httpClient, cache: make(map[string]cached)}
}

// Fetch lists the rolling observation window and shares the downloads between
// workspaces. Call it serially on a given Client.
func (c *Client) Fetch(ctx context.Context, sat Satellite, start, end time.Time, boxes []firms.Bounds) ([][]firms.Observation, error) {
	objects, err := list(ctx, c.http, sat, start, end)
	if err != nil {
		return nil, err
	}
	if len(objects) == 0 {
		return nil, fmt.Errorf("%s bucket has no granules in the observation window", sat.Code)
	}
	results := make([]cached, len(objects))
	var group errgroup.Group
	group.SetLimit(8)
	for i, g := range objects {
		if hit, ok := c.cache[g.Key]; ok && hit.etag == g.ETag && (hit.data.pixelsRead || !hit.data.covers(boxes)) {
			results[i] = hit
			continue
		}
		if ctx.Err() != nil {
			break
		}
		group.Go(func() error {
			reader := &objectReader{ctx: ctx, client: c.http, url: sat.Endpoint + "/" + g.Key, size: g.Size, etag: g.ETag, pages: make(map[int64][]byte)}
			data, err := readPixels(granuleReader{io.NewSectionReader(reader, 0, g.Size)}, g, sat, boxes)
			if err != nil {
				return fmt.Errorf("read %s: %w", g.Key, err)
			}
			results[i] = cached{g.ETag, data}
			return nil
		})
	}
	fetchErr := errors.Join(group.Wait(), ctx.Err())
	// Retain only the most recent listing for this satellite.
	for key := range c.cache {
		if m := granuleName.FindStringSubmatch(key); m != nil && m[1] == sat.Code {
			delete(c.cache, key)
		}
	}
	byWorkspace := make([][]firms.Observation, len(boxes))
	for i, result := range results {
		if result.data.box != nil || result.data.pixelsRead {
			c.cache[objects[i].Key] = result
		}
		for _, pixel := range result.data.pixels {
			for w, box := range boxes {
				if contains(box, pixel.Longitude, pixel.Latitude) {
					byWorkspace[w] = append(byWorkspace[w], pixel)
				}
			}
		}
	}
	return byWorkspace, fetchErr
}
