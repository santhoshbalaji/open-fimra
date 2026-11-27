package main

import (
	"context"
	_ "embed"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// scopeSQL chooses what a caller may see in a time window. The dashboard and
// situation reports both start with it.
//
//go:embed scope.sql
var scopeSQL string

//go:embed summary.sql
var summaryTailSQL string

var summarySQL = scopeSQL + summaryTailSQL

// responseMilestones are the workflow steps whose response times are measured,
// in workflow order. Alerted is the time from the satellite observation to the
// alert being raised; the others run from the alert being raised.
var responseMilestones = []string{"alerted", "acknowledged", "team_sent", "reached_site", "controlled", "extinguished"}

type responsePercentiles struct {
	Median *float64 `json:"median"`
	P90    *float64 `json:"p90"`
	// Count is how many alerts reached the step.
	Count int `json:"count"`
}
type summaryFeed struct {
	Source              string          `json:"source"`
	Product             string          `json:"product"`
	Status              string          `json:"status"`
	LastSuccessAt       *time.Time      `json:"lastSuccessAt"`
	LatestObservationAt *time.Time      `json:"latestObservationAt"`
	Latency             []feedLatency   `json:"latency,omitempty"`
	Scans               *scanHealthJSON `json:"scans,omitempty"`
}

// summaryCounts are the counts the dashboard and situation reports share.
type summaryCounts struct {
	Detections      map[string]int `json:"detections"`
	Today           map[string]int `json:"today"`
	Queues          map[string]int `json:"queues"`
	Steps           map[string]int `json:"steps"`
	Active          int            `json:"active"`
	Overdue         int            `json:"overdue"`
	backlogOverdue  *int
	PendingReports  int                            `json:"pendingReports"`
	ResponseSeconds map[string]responsePercentiles `json:"responseSeconds"`
}

type workspaceSummary struct {
	summaryCounts
	BacklogOverdue         *int          `json:"backlogOverdue,omitempty"`
	FeedHealth             []summaryFeed `json:"feedHealth"`
	FeedHealthUnavailable  bool          `json:"feedHealthUnavailable,omitempty"`
	FeedLatencyUnavailable bool          `json:"feedLatencyUnavailable,omitempty"`
}

// countWindow is the time window of the shared counts. A nil start means
// hours before now, and a nil end means now.
type countWindow struct {
	start   *time.Time
	hours   int
	end     *time.Time
	class   string
	path    *string
	backlog bool
}

// loadCounts runs the dashboard's counts query. It reports whether the caller
// has a grant in the workspace, and the workspace ID for a root grant.
func loadCounts(ctx context.Context, db rowQuerier, subject, slug string, w countWindow) (c summaryCounts, allowed bool, root *string, err error) {
	err = db.QueryRow(ctx, summarySQL, subject, slug, w.start, w.hours, w.end, w.class, w.path, w.backlog).Scan(&allowed, &root,
		&c.Detections, &c.Today, &c.Queues, &c.Steps, &c.Active, &c.Overdue, &c.PendingReports, &c.ResponseSeconds, &c.backlogOverdue)
	if err != nil {
		return c, false, nil, err
	}
	for _, step := range responseMilestones {
		if _, ok := c.ResponseSeconds[step]; !ok {
			c.ResponseSeconds[step] = responsePercentiles{}
		}
	}
	return c, allowed, root, nil
}

func (a api) summary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	f, err := readAlertFilters(r.URL.Query())
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var s workspaceSummary
	caller := r.Context().Value(identityKey{}).(identity)
	counts, allowed, root, err := loadCounts(ctx, a.reader, caller.Subject, chi.URLParam(r, "slug"),
		countWindow{hours: f.hours, class: f.class, path: f.path, backlog: true})
	s.summaryCounts = counts
	s.BacklogOverdue = counts.backlogOverdue
	if err != nil {
		a.logger.Error("Could not read dashboard counts", "error", err)
		writeError(w, 500, "Could not load dashboard.")
		return
	}
	if !allowed {
		writeError(w, 403, "A role in this workspace is required.")
		return
	}
	if root != nil {
		now := time.Now()
		if a.now != nil {
			now = a.now()
		}
		facts, err := loadFeedFacts(ctx, a.feedDB, root, now, a.firms.liveProducts())
		if err != nil {
			a.logger.Error("Could not read dashboard feed health", "error", err)
			s.FeedHealthUnavailable = true
			writeJSON(w, 200, s)
			return
		}
		var latencies map[feedKey][]feedLatency
		if len(facts) > 0 {
			latencies, err = loadFeedLatency(ctx, a.feedDB, *root, now)
			if err != nil {
				a.logger.Error("Could not read dashboard feed latency", "error", err)
				s.FeedLatencyUnavailable = true
			}
		}
		s.FeedHealth = make([]summaryFeed, 0, len(facts))
		for _, f := range facts {
			set, err := readFeedSettings(f.Settings)
			if err != nil {
				a.logger.Warn("Using default feed settings", "workspace", f.WorkspaceSlug, "error", err)
			}
			s.FeedHealth = append(s.FeedHealth, summaryFeed{
				Source: f.Source, Product: f.Product, Status: feedStatus(f, set.Thresholds, now),
				LastSuccessAt: f.LastSuccessAt, LatestObservationAt: f.LatestObservedAt,
				Latency: latencies[feedKey{f.Source, f.Product}], Scans: f.scanHealth(),
			})
		}
	}
	writeJSON(w, 200, s)
}
