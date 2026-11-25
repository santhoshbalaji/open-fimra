package main

import (
	"context"
	_ "embed"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

//go:embed summary.sql
var summarySQL string

type responsePercentiles struct {
	Median *float64 `json:"median"`
	P90    *float64 `json:"p90"`
}
type summaryFeed struct {
	Source  string `json:"source"`
	Product string `json:"product"`
	Status  string `json:"status"`
}
type workspaceSummary struct {
	Detections      map[string]int                 `json:"detections"`
	Today           map[string]int                 `json:"today"`
	Queues          map[string]int                 `json:"queues"`
	Steps           map[string]int                 `json:"steps"`
	Active          int                            `json:"active"`
	Overdue         int                            `json:"overdue"`
	PendingReports  int                            `json:"pendingReports"`
	ResponseSeconds map[string]responsePercentiles `json:"responseSeconds"`
	FeedHealth      []summaryFeed                  `json:"feedHealth"`
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
	var allowed bool
	var root *string
	caller := r.Context().Value(identityKey{}).(identity)
	err = a.reader.QueryRow(ctx, summarySQL, caller.Subject, chi.URLParam(r, "slug"), f.hours, f.class, f.path).Scan(&allowed, &root, &s.Detections, &s.Today, &s.Queues, &s.Steps, &s.Active, &s.Overdue, &s.PendingReports, &s.ResponseSeconds)
	if err != nil {
		a.logger.Error("Could not read dashboard counts", "error", err)
		writeError(w, 500, "Could not load dashboard.")
		return
	}
	if !allowed {
		writeError(w, 403, "A role in this workspace is required.")
		return
	}
	for _, step := range []string{"acknowledged", "reached_site", "controlled"} {
		if _, ok := s.ResponseSeconds[step]; !ok {
			s.ResponseSeconds[step] = responsePercentiles{}
		}
	}
	if root != nil {
		now := time.Now()
		if a.now != nil {
			now = a.now()
		}
		facts, err := loadFeedFacts(ctx, a.feedDB, root, now)
		if err != nil {
			a.logger.Error("Could not read dashboard feed health", "error", err)
			writeError(w, 500, "Could not load feed health.")
			return
		}
		s.FeedHealth = make([]summaryFeed, 0, len(facts))
		for _, f := range facts {
			set, err := readFeedSettings(f.Settings)
			if err != nil {
				a.logger.Warn("Using default feed settings", "workspace", f.WorkspaceSlug, "error", err)
			}
			s.FeedHealth = append(s.FeedHealth, summaryFeed{f.Source, f.Product, feedStatus(f, set.Thresholds, now)})
		}
	}
	writeJSON(w, 200, s)
}
