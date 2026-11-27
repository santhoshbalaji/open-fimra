package firms

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func date(s string) time.Time {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestPlanChunks(t *testing.T) {
	type want struct {
		start string
		days  int
	}
	for _, tc := range []struct {
		name        string
		first, last string
		done        []string
		want        []want
	}{
		{"one day", "2000-01-01", "2000-01-01", nil, []want{{"2000-01-01", 1}}},
		{"exactly the limit", "2000-01-01", "2000-01-05", nil, []want{{"2000-01-01", 5}}},
		{"over the limit", "2000-01-30", "2000-02-11", nil,
			[]want{{"2000-01-30", 5}, {"2000-02-04", 5}, {"2000-02-09", 3}}},
		{"leap day and year end", "2000-02-27", "2001-01-02", nil, nil},
		{"finished days split chunks", "2000-01-01", "2000-01-12", []string{"2000-01-03", "2000-01-09", "2000-01-10"},
			[]want{{"2000-01-01", 2}, {"2000-01-04", 5}, {"2000-01-11", 2}}},
		{"everything finished", "2000-01-01", "2000-01-02", []string{"2000-01-01", "2000-01-02"}, []want{}},
		{"reversed range", "2000-01-02", "2000-01-01", nil, []want{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := map[time.Time]bool{}
			for _, d := range tc.done {
				done[date(d)] = true
			}
			// Times inside a day count as that UTC day.
			got := PlanChunks(date(tc.first).Add(13*time.Hour), date(tc.last).Add(23*time.Hour), MaxDayRange,
				func(day time.Time) bool { return done[day] })
			if tc.want == nil {
				// Check the general rules for a long range.
				days := 0
				for i, c := range got {
					if c.Days < 1 || c.Days > MaxDayRange || (i > 0 && !got[i-1].End().Equal(c.Start)) {
						t.Fatalf("chunk %d is %+v", i, c)
					}
					days += c.Days
				}
				if days != 311 || !got[len(got)-1].End().Equal(date("2001-01-03")) {
					t.Fatalf("chunks cover %d days ending %s", days, got[len(got)-1].End())
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d chunks %+v, want %+v", len(got), got, tc.want)
			}
			for i, w := range tc.want {
				if !got[i].Start.Equal(date(w.start)) || got[i].Days != w.days {
					t.Fatalf("chunk %d is %s for %d days, want %s for %d", i, got[i].Start.Format(dateLayout), got[i].Days, w.start, w.days)
				}
			}
		})
	}
}

type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func TestLimiter(t *testing.T) {
	clock := &fakeClock{now: date("2000-01-01")}
	limiter := NewLimiter(10, 10*time.Minute).WithClock(clock.Now, clock.Sleep)
	ctx := t.Context()
	// Spend 4 at 0:00, 4 at 0:03 and 2 at 0:05: the window is full.
	for _, step := range []struct {
		advance time.Duration
		cost    int
	}{{0, 4}, {3 * time.Minute, 4}, {2 * time.Minute, 2}} {
		clock.now = clock.now.Add(step.advance)
		if err := limiter.Wait(ctx, step.cost); err != nil {
			t.Fatal(err)
		}
	}
	if len(clock.sleeps) != 0 {
		t.Fatalf("waited %v while under the limit", clock.sleeps)
	}
	// 5 more need the first two spends to leave the window, at 0:13.
	if err := limiter.Wait(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != 8*time.Minute || !clock.now.Equal(date("2000-01-01").Add(13*time.Minute)) {
		t.Fatalf("waited %v and finished at %s, want one wait of 8m ending at 00:13", clock.sleeps, clock.now)
	}
	// Exactly the limit inside any 10 minutes: 2 (0:05) + 5 (0:13) + 3 = 10.
	if err := limiter.Wait(ctx, 3); err != nil || len(clock.sleeps) != 1 {
		t.Fatalf("waited %v for a request that fits: %v", clock.sleeps, err)
	}
	if err := limiter.Wait(ctx, 11); err == nil {
		t.Fatal("accepted a request larger than the limit")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := limiter.Wait(cancelled, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled wait returned %v", err)
	}
}

func TestFetchRangeUsesTheDateForm(t *testing.T) {
	csv, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "connectors", "firms", "fixtures", "viirs_snpp_sp.csv"))
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: date("2000-01-01")}
	var urls []string
	area := NewArea("test/key", transportFunc(func(r *http.Request) (*http.Response, error) {
		urls = append(urls, r.URL.String())
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(csv)))}, nil
	})).WithLimiter(NewLimiter(6, TransactionWindow).WithClock(clock.Now, clock.Sleep))
	box := Bounds{West: 86.19, South: 16.29, East: 86.69, North: 16.55}
	got, err := area.FetchRange(t.Context(), "VIIRS_SNPP_SP", box, date("2000-01-30").Add(5*time.Hour), 5)
	if err != nil || len(got) == 0 || got[0].Instrument != "VIIRS" {
		t.Fatalf("parsed %d standard-processing records: %v", len(got), err)
	}
	want := areaURL + "test%2Fkey/VIIRS_SNPP_SP/86.19,16.29,86.69,16.55/5/2000-01-30"
	if urls[0] != want {
		t.Fatalf("sent %s, want %s", urls[0], want)
	}
	// Five days cost five transactions, so a second request of two waits.
	if _, err := area.Fetch(t.Context(), "VIIRS_SNPP_NRT", box); err != nil {
		t.Fatal(err)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != TransactionWindow {
		t.Fatalf("waited %v, want one wait of the whole window", clock.sleeps)
	}
	for _, days := range []int{0, MaxDayRange + 1} {
		if _, err := area.FetchRange(t.Context(), "MODIS_SP", box, date("2000-01-01"), days); err == nil {
			t.Fatalf("accepted a day range of %d", days)
		}
	}
	if len(urls) != 2 {
		t.Fatalf("sent %d requests, want 2", len(urls))
	}
}

func TestAvailability(t *testing.T) {
	const body = "data_id,min_date,max_date\nMODIS_NRT,2026-08-01,2026-11-24\nMODIS_SP,2000-11-01,2026-07-31\n"
	var path string
	area := NewArea("k", transportFunc(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	first, last, err := area.Availability(t.Context(), "MODIS_SP")
	if err != nil || !first.Equal(date("2000-11-01")) || !last.Equal(date("2026-07-31")) {
		t.Fatalf("got %s to %s: %v", first, last, err)
	}
	if path != "/api/data_availability/csv/k/MODIS_SP" {
		t.Fatalf("requested %s", path)
	}
	if _, _, err := ParseAvailability(strings.NewReader(body), "VIIRS_SNPP_SP"); err == nil {
		t.Fatal("found a product that is not listed")
	}
}

func TestProducts(t *testing.T) {
	for _, pair := range [][2]string{
		{"VIIRS_SNPP_NRT", "VIIRS_SNPP_SP"},
		{"VIIRS_NOAA20_NRT", "VIIRS_NOAA20_SP"},
		{"VIIRS_NOAA21_NRT", "VIIRS_NOAA21_SP"},
		{"MODIS_NRT", "MODIS_SP"},
	} {
		nrt, standard := pair[0], pair[1]
		if IsStandard(nrt) || !IsStandard(standard) ||
			Counterpart(nrt) != standard || Counterpart(standard) != nrt {
			t.Fatalf("products %s and %s are not paired", nrt, standard)
		}
	}
	if Counterpart("LANDSAT_NRT") != "" {
		t.Fatal("an unknown product has a counterpart")
	}
}
