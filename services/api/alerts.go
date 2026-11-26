package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

//go:embed alerts.sql
var alertsSQL string

type alertPage struct {
	Counts     map[string]int    `json:"counts"`
	Items      []json.RawMessage `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
	CanReview  bool              `json:"canReview"`
}

type alertFilters struct {
	queue, status, class, group string
	path                        *string
	hours, limit                int
	before                      *time.Time
	beforeID                    int64
}

var stepSlug = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

var unitPrefix = regexp.MustCompile(`^[[:alnum:]_-]{1,1000}(\.[[:alnum:]_-]{1,1000})*$`)

func readAlertFilters(q url.Values) (alertFilters, error) {
	f := alertFilters{queue: q.Get("queue"), status: q.Get("status"), class: q.Get("class"), group: q.Get("group"), hours: 168, limit: 100}
	if !slices.Contains([]string{"", "unit", "edge_review", "triage"}, f.queue) ||
		(f.status != "" && !stepSlug.MatchString(f.status)) ||
		!slices.Contains([]string{"", "inside", "near_edge", "outside", "fixed_heat_source"}, f.class) {
		return f, errors.New("Unknown queue, status or forest class.")
	}
	if !slices.Contains([]string{"", "active", "overdue", "pending_reports"}, f.group) {
		return f, errors.New("Unknown alert group.")
	}
	if q.Has("unitPath") {
		path := q.Get("unitPath")
		if !unitPrefix.MatchString(path) {
			return f, errors.New("Invalid unit path.")
		}
		f.path = &path
	}
	var err error
	if q.Has("hours") {
		f.hours, err = strconv.Atoi(q.Get("hours"))
		if err != nil || f.hours < 1 || f.hours > 720 {
			return f, errors.New("hours must be an integer from 1 to 720.")
		}
	}
	if q.Has("limit") {
		f.limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || f.limit < 1 || f.limit > 200 {
			return f, errors.New("limit must be an integer from 1 to 200.")
		}
	}
	if q.Has("cursor") {
		timestamp, id, _ := strings.Cut(q.Get("cursor"), ",")
		before, timeErr := time.Parse(time.RFC3339Nano, timestamp)
		f.beforeID, err = strconv.ParseInt(id, 10, 64)
		if timeErr != nil || err != nil || f.beforeID < 1 {
			return f, errors.New("Invalid alert cursor.")
		}
		f.before = &before
	}
	return f, nil
}

func (a api) alerts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	f, err := readAlertFilters(r.URL.Query())
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	sort := r.URL.Query().Get("sort")
	if sort != "" && sort != "newest" && sort != "oldest" {
		writeError(w, 400, "Unknown alert sort order.")
		return
	}
	var id *int64
	if raw := chi.URLParam(r, "id"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 {
			writeError(w, 400, "Invalid alert ID.")
			return
		}
		id = &n
	}
	var detectionID, incidentID *int64
	if r.URL.Query().Has("detection") {
		n, err := strconv.ParseInt(r.URL.Query().Get("detection"), 10, 64)
		if err != nil || n < 1 {
			writeError(w, 400, "Invalid detection ID.")
			return
		}
		detectionID = &n
	}
	if r.URL.Query().Has("incident") {
		n, err := strconv.ParseInt(r.URL.Query().Get("incident"), 10, 64)
		if err != nil || n < 1 {
			writeError(w, 400, "Invalid incident ID.")
			return
		}
		incidentID = &n
	}
	caller := r.Context().Value(identityKey{}).(identity)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var allowed, knownStatus bool
	var page alertPage
	err = a.reader.QueryRow(ctx, alertsSQL, caller.Subject, chi.URLParam(r, "slug"), id, f.hours,
		f.queue, f.status, f.class, f.path, f.before, f.beforeID, f.limit+1, detectionID, incidentID, f.group, sort == "oldest").Scan(&allowed, &page.CanReview, &knownStatus, &page.Counts, &page.Items)
	if err != nil {
		a.logger.Error("Could not read alerts", "error", err)
		writeError(w, 500, "Could not load alerts.")
		return
	}
	if !allowed {
		writeError(w, 403, "A role in this workspace is required.")
		return
	}
	if !knownStatus {
		writeError(w, 400, "Unknown workflow step.")
		return
	}
	if id != nil {
		if len(page.Items) == 0 {
			writeError(w, 404, "Alert not found.")
			return
		}
		writeJSON(w, 200, page.Items[0])
		return
	}
	if len(page.Items) > f.limit {
		page.Items = page.Items[:f.limit]
		var last struct {
			Time string `json:"observed_time"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(page.Items[f.limit-1], &last); err != nil {
			a.logger.Error("Could not read alert cursor", "error", err)
			writeError(w, 500, "Could not load alerts.")
			return
		}
		page.NextCursor = last.Time + "," + last.ID
	}
	writeJSON(w, 200, page)
}
