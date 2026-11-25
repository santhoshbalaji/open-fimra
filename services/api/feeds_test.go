package main

import (
	"testing"
	"time"
)

// feedSim is a feed on a fake clock: a poll every 15 minutes that succeeds or
// fails, and observations that arrive with some polls.
type feedSim struct {
	now   time.Time
	facts feedFacts
	fails []time.Time
}

func newFeedSim(start time.Time) *feedSim {
	return &feedSim{now: start, facts: feedFacts{FirstRunAt: start, LastRunAt: start}}
}

// poll advances the clock by 15 minutes and records one poll.
func (s *feedSim) poll(ok bool, observed *time.Time) {
	s.now = s.now.Add(15 * time.Minute)
	s.facts.LastRunAt = s.now
	if !ok {
		s.fails = append(s.fails, s.now)
		return
	}
	at := s.now
	s.facts.LastSuccessAt = &at
	if observed != nil && (s.facts.LatestObservedAt == nil || observed.After(*s.facts.LatestObservedAt)) {
		o := *observed
		s.facts.LatestObservedAt = &o
	}
}

func (s *feedSim) status() string { return feedStatus(s.facts, defaultThresholds, s.now) }

func TestFeedStatusTransitions(t *testing.T) {
	start := time.Date(2030, 3, 10, 0, 0, 0, 0, time.UTC)
	sim := newFeedSim(start)
	if got := sim.status(); got != feedHealthy {
		t.Fatalf("a new feed is %s, want healthy", got)
	}

	// Two days of normal polls. Each 12 hours a pass is published 3 hours
	// after the satellite saw it.
	var transitions []string
	record := func(label string) {
		status := sim.status()
		if len(transitions) == 0 || transitions[len(transitions)-1] != status {
			transitions = append(transitions, status)
			t.Logf("%s at +%s: %s", label, sim.now.Sub(start), status)
		}
	}
	for i := 1; i <= 2*24*4; i++ {
		var observed *time.Time
		if elapsed := time.Duration(i) * 15 * time.Minute; elapsed%(12*time.Hour) == 3*time.Hour {
			o := sim.now.Add(15*time.Minute - 3*time.Hour)
			observed = &o
		}
		sim.poll(true, observed)
		record("normal")
	}
	if len(transitions) != 1 || transitions[0] != feedHealthy {
		t.Fatalf("normal polling went through %v, want only healthy", transitions)
	}

	// The source goes silent: polls succeed but return nothing new. The last
	// observation was seen at +36h and published at +39h.
	last := *sim.facts.LatestObservedAt
	for sim.now.Sub(last) <= 40*time.Hour {
		sim.poll(true, nil)
		record("silent")
		switch age := sim.now.Sub(last); {
		case age <= 18*time.Hour && sim.status() != feedHealthy,
			age > 18*time.Hour && age <= 36*time.Hour && sim.status() != feedDelayed,
			age > 36*time.Hour && sim.status() != feedQuiet:
			t.Fatalf("%s after the last observation the feed is %s", age, sim.status())
		}
	}

	// Then fetching fails. Within the hour it stays quiet, then it is failing.
	for range 4 {
		sim.poll(false, nil)
		record("failing")
		if sim.status() != feedQuiet {
			t.Fatalf("%s after the last success the feed is %s, want quiet", sim.now.Sub(*sim.facts.LastSuccessAt), sim.status())
		}
	}
	sim.poll(false, nil)
	record("failing")
	if sim.status() != feedFailing {
		t.Fatalf("75 minutes after the last success the feed is %s, want failing", sim.status())
	}

	// Recovery: a successful poll with a fresh observation.
	fresh := sim.now.Add(15*time.Minute - 2*time.Hour)
	sim.poll(true, &fresh)
	record("recovered")
	want := []string{feedHealthy, feedDelayed, feedQuiet, feedFailing, feedHealthy}
	if len(transitions) != len(want) {
		t.Fatalf("went through %v, want %v", transitions, want)
	}
	for i := range want {
		if transitions[i] != want[i] {
			t.Fatalf("went through %v, want %v", transitions, want)
		}
	}
}

func TestFeedStatusNewFeed(t *testing.T) {
	start := time.Date(2030, 3, 10, 0, 0, 0, 0, time.UTC)
	f := feedFacts{FirstRunAt: start, LastRunAt: start}
	// A first run that has not finished is not a failure yet.
	if got := feedStatus(f, defaultThresholds, start.Add(59*time.Minute)); got != feedHealthy {
		t.Fatalf("got %s", got)
	}
	if got := feedStatus(f, defaultThresholds, start.Add(61*time.Minute)); got != feedFailing {
		t.Fatalf("a feed that never succeeded is %s after an hour", got)
	}
	// A feed that works but has never seen a fire becomes quiet.
	ok := start.Add(37 * time.Hour)
	f.LastSuccessAt = &ok
	if got := feedStatus(f, defaultThresholds, ok); got != feedQuiet {
		t.Fatalf("a feed without observations for 37 hours is %s", got)
	}
}

func TestFeedAlertAction(t *testing.T) {
	for _, tc := range []struct {
		status   string
		inSeason bool
		want     alertAction
	}{
		{feedHealthy, true, resolveAlert},
		{feedDelayed, true, resolveAlert},
		{feedQuiet, true, openAlert},
		{feedFailing, true, openAlert},
		{feedHealthy, false, resolveAlert},
		{feedQuiet, false, keepAlert},
		{feedFailing, false, keepAlert},
	} {
		if got := feedAlertAction(tc.status, tc.inSeason); got != tc.want {
			t.Fatalf("%s in season %t: got %d, want %d", tc.status, tc.inSeason, got, tc.want)
		}
	}
}

func TestFireSeason(t *testing.T) {
	ist := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, indiaTime)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		season fireSeason
		at     time.Time
		want   bool
	}{
		{defaultFireSeason, ist("2030-11-01 00:00"), true},
		{defaultFireSeason, ist("2030-10-31 23:59"), false},
		{defaultFireSeason, ist("2031-01-15 12:00"), true},
		{defaultFireSeason, ist("2031-06-30 23:59"), true},
		{defaultFireSeason, ist("2031-07-01 00:00"), false},
		// 20:00 UTC on 31 October is already 1 November in India.
		{defaultFireSeason, time.Date(2030, 10, 31, 20, 0, 0, 0, time.UTC), true},
		{fireSeason{monthDay{2, 1}, monthDay{5, 31}}, ist("2031-01-31 12:00"), false},
		{fireSeason{monthDay{2, 1}, monthDay{5, 31}}, ist("2031-03-01 12:00"), true},
		{fireSeason{monthDay{1, 1}, monthDay{12, 31}}, ist("2031-08-01 12:00"), true},
	} {
		if got := tc.season.contains(tc.at); got != tc.want {
			t.Fatalf("season %s to %s at %s: got %t", tc.season.Start, tc.season.End, tc.at, got)
		}
	}
}

func TestReadFeedSettings(t *testing.T) {
	got, err := readFeedSettings([]byte(`{"nearEdgeMetres": 500, "feedHealth": {"quietAfterHours": 48,
		"failingAfterMinutes": 30, "fireSeason": {"start": "02-01", "end": "05-31"}}}`))
	if err != nil || got.Thresholds.QuietAfter != 48*time.Hour || got.Thresholds.FailingAfter != 30*time.Minute ||
		got.Thresholds.DelayedAfter != defaultThresholds.DelayedAfter || got.Season.Start != (monthDay{2, 1}) {
		t.Fatalf("got %+v, %v", got, err)
	}
	for _, bad := range []string{
		`{"feedHealth": {"quietAfterHours": -1}}`,
		`{"feedHealth": {"delayedAfterHours": 40}}`,
		`{"feedHealth": {"fireSeason": {"start": "13-01", "end": "05-31"}}}`,
		`{"feedHealth": {"fireSeason": {"start": "2-1", "end": "05-31"}}}`,
		`{"feedHealth": []}`,
	} {
		got, err := readFeedSettings([]byte(bad))
		if err == nil || got.Thresholds != defaultThresholds || got.Season != defaultFireSeason {
			t.Fatalf("%s gave %+v, %v; want the defaults and an error", bad, got, err)
		}
	}
	if got, err := readFeedSettings([]byte(`{}`)); err != nil || got.Season != defaultFireSeason {
		t.Fatalf("empty settings gave %+v, %v", got, err)
	}
}
