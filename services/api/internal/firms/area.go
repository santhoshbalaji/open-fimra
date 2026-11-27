package firms

import (
	"context"
	"encoding/csv"
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
	areaURL         = "https://firms.modaps.eosdis.nasa.gov/api/area/csv/"
	availabilityURL = "https://firms.modaps.eosdis.nasa.gov/api/data_availability/csv/"
	// Days is the rolling window that each live poll requests.
	Days = 2
	// MaxDayRange is the most days one area request may ask for. The FIRMS
	// area API documentation gives DAY_RANGE as "1 .. 5".
	MaxDayRange = 5
	dateLayout  = "2006-01-02"
)

type Bounds struct {
	West, South, East, North float64
}

// Area calls the FIRMS area API with one map key. Copies of an Area share one
// rate limiter, so the poller and backfill jobs stay inside the key's limit
// together.
type Area struct {
	key     string
	client  *http.Client
	limiter *Limiter
}

func NewArea(key string, transport http.RoundTripper) Area {
	return Area{key: key, limiter: NewLimiter(TransactionLimit, TransactionWindow), client: &http.Client{
		Transport: transport,
		Timeout:   45 * time.Second,
		// The key is part of the request path.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// WithLimiter returns a copy of the Area that uses limiter.
func (a Area) WithLimiter(limiter *Limiter) Area {
	a.limiter = limiter
	return a
}

// Fetch returns the most recent Days of observations in box.
func (a Area) Fetch(ctx context.Context, product string, box Bounds) ([]Observation, error) {
	return a.fetch(ctx, product, box, Days, "")
}

// FetchRange returns the observations in box for days UTC calendar days
// starting on the date of start. FIRMS answers the date form with the data
// for [DATE] to [DATE + DAY_RANGE - 1].
func (a Area) FetchRange(ctx context.Context, product string, box Bounds, start time.Time, days int) ([]Observation, error) {
	if days < 1 || days > MaxDayRange {
		return nil, fmt.Errorf("FIRMS day range must be from 1 to %d, got %d", MaxDayRange, days)
	}
	return a.fetch(ctx, product, box, days, "/"+start.UTC().Format(dateLayout))
}

func (a Area) fetch(ctx context.Context, product string, box Bounds, days int, date string) ([]Observation, error) {
	if a.key == "" {
		return nil, errors.New("FIRMS map key is empty")
	}
	var coords [4]string
	for i, v := range [...]float64{box.West, box.South, box.East, box.North} {
		coords[i] = strconv.FormatFloat(v, 'f', -1, 64)
	}
	endpoint := areaURL + url.PathEscape(a.key) + "/" + product + "/" + strings.Join(coords[:], ",") + "/" + strconv.Itoa(days) + date
	// FIRMS says larger requests may count as more than one transaction, so
	// count one transaction per day requested. This overestimates on purpose.
	body, err := a.get(ctx, endpoint, days, "area")
	if err != nil {
		return nil, err
	}
	defer body.Close()
	const maxBytes = 32 << 20
	limited := &io.LimitedReader{R: body, N: maxBytes + 1}
	observations, err := Parse(limited, product)
	if limited.N == 0 {
		return observations, errors.New("FIRMS area response exceeded 32 MiB")
	}
	if err != nil {
		return observations, a.redact(err)
	}
	return observations, nil
}

// Availability returns the first and last UTC dates for which FIRMS holds
// data for product. Backfill uses it so that it never records a day as done
// before FIRMS has published it.
func (a Area) Availability(ctx context.Context, product string) (first, last time.Time, err error) {
	if a.key == "" {
		return first, last, errors.New("FIRMS map key is empty")
	}
	body, err := a.get(ctx, availabilityURL+url.PathEscape(a.key)+"/"+product, 1, "data availability")
	if err != nil {
		return first, last, err
	}
	defer body.Close()
	first, last, err = ParseAvailability(&io.LimitedReader{R: body, N: 1 << 20}, product)
	if err != nil {
		return first, last, a.redact(err)
	}
	return first, last, nil
}

func (a Area) get(ctx context.Context, endpoint string, cost int, what string) (io.ReadCloser, error) {
	if a.limiter != nil {
		if err := a.limiter.Wait(ctx, cost); err != nil {
			return nil, fmt.Errorf("wait for the FIRMS transaction limit: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build FIRMS %s request: %w", what, a.redact(err))
	}
	res, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send FIRMS %s request: %w", what, a.redact(err))
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("FIRMS %s request returned HTTP %d", what, res.StatusCode)
	}
	return res.Body, nil
}

// ParseAvailability reads the data availability CSV and returns the date
// range for product.
func ParseAvailability(input io.Reader, product string) (first, last time.Time, err error) {
	reader := csv.NewReader(input)
	header, err := reader.Read()
	if err != nil {
		return first, last, fmt.Errorf("read FIRMS availability header: %w", err)
	}
	columns := make(map[string]int, len(header))
	for i, name := range header {
		columns[strings.TrimSpace(strings.TrimPrefix(name, "\ufeff"))] = i
	}
	for _, name := range []string{"data_id", "min_date", "max_date"} {
		if _, ok := columns[name]; !ok {
			return first, last, fmt.Errorf("missing FIRMS availability column %s", name)
		}
	}
	for {
		values, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return first, last, fmt.Errorf("FIRMS availability has no row for %s", product)
		}
		if err != nil {
			return first, last, fmt.Errorf("read FIRMS availability: %w", err)
		}
		if strings.TrimSpace(values[columns["data_id"]]) != product {
			continue
		}
		first, err = time.Parse(dateLayout, strings.TrimSpace(values[columns["min_date"]]))
		if err != nil {
			return first, last, fmt.Errorf("invalid FIRMS availability start: %w", err)
		}
		last, err = time.Parse(dateLayout, strings.TrimSpace(values[columns["max_date"]]))
		if err != nil {
			return first, last, fmt.Errorf("invalid FIRMS availability end: %w", err)
		}
		if last.Before(first) {
			return first, last, errors.New("FIRMS availability ends before it starts")
		}
		return first, last, nil
	}
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
