package firms

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	areaURL = "https://firms.modaps.eosdis.nasa.gov/api/area/csv/"
	Days    = 2
)

type Bounds struct {
	West, South, East, North float64
}

type Area struct {
	key    string
	client http.Client
}

func NewArea(key string, transport http.RoundTripper) Area {
	return Area{key: key, client: http.Client{
		Transport: transport,
		Timeout:   45 * time.Second,
		// The key is part of the request path.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (a Area) Fetch(ctx context.Context, product string, box Bounds) ([]Observation, error) {
	if a.key == "" {
		return nil, errors.New("FIRMS map key is empty")
	}
	var coords [4]string
	for i, v := range [...]float64{box.West, box.South, box.East, box.North} {
		coords[i] = strconv.FormatFloat(v, 'f', -1, 64)
	}
	endpoint := areaURL + url.PathEscape(a.key) + "/" + product + "/" + strings.Join(coords[:], ",") + "/" + strconv.Itoa(Days)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build FIRMS area request: %w", a.redact(err))
	}
	res, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send FIRMS area request: %w", a.redact(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("FIRMS area request returned HTTP %d", res.StatusCode)
	}
	const maxBytes = 32 << 20
	body := &io.LimitedReader{R: res.Body, N: maxBytes + 1}
	observations, err := Parse(body, product)
	if body.N == 0 {
		return observations, errors.New("FIRMS area response exceeded 32 MiB")
	}
	if err != nil {
		return observations, a.redact(err)
	}
	return observations, nil
}

type redactedError struct {
	message string
	cause   error
}

func (e redactedError) Error() string { return e.message }
func (e redactedError) Unwrap() error { return e.cause }

func (a Area) redact(err error) error {
	var requestError *url.Error
	if errors.As(err, &requestError) && err == requestError {
		// Keep the transport cause, and remove the URL from the error chain.
		err = requestError.Err
	}
	message := strings.NewReplacer(url.PathEscape(a.key), "[redacted]", a.key, "[redacted]").Replace(err.Error())
	return redactedError{message: message, cause: err}
}
