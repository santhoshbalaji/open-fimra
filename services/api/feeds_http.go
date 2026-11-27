package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type feedThresholdsJSON struct {
	FailingAfterMinutes float64 `json:"failingAfterMinutes"`
	DelayedAfterHours   float64 `json:"delayedAfterHours"`
	QuietAfterHours     float64 `json:"quietAfterHours"`
}

type fireSeasonJSON struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	TimeZone string `json:"timeZone"`
	InSeason bool   `json:"inSeason"`
}

type processingJSON struct {
	// Records new in a poll over the last 7 days, with a comparable previous fetch.
	Records int `json:"records"`
	// Upper estimate of the time from publication to match, at the 95th
	// percentile, and the share of records within 20 minutes.
	P95Seconds          *float64 `json:"p95Seconds"`
	WithinTwentyMinutes *float64 `json:"withinTwentyMinutes"`
}

type firstSeenJSON struct {
	Records    int      `json:"records"`
	P50Seconds *float64 `json:"p50Seconds"`
	P95Seconds *float64 `json:"p95Seconds"`
}

type feedJSON struct {
	Source              string         `json:"source"`
	Product             string         `json:"product"`
	Status              string         `json:"status"`
	LastRunAt           time.Time      `json:"lastRunAt"`
	LastRunStatus       string         `json:"lastRunStatus"`
	LastError           *string        `json:"lastError"`
	LastSuccessAt       *time.Time     `json:"lastSuccessAt"`
	LatestObservationAt *time.Time     `json:"latestObservationAt"`
	DelaySeconds        *int64         `json:"delaySeconds"`
	FailuresLast24h     int            `json:"failuresLast24h"`
	Processing          processingJSON `json:"processing"`
	FirstSeen           firstSeenJSON  `json:"firstSeen"`
	Latency             []feedLatency  `json:"latency"`
}

type feedAlertJSON struct {
	ID            int64     `json:"id"`
	Source        string    `json:"source"`
	Product       string    `json:"product"`
	OpenedStatus  string    `json:"openedStatus"`
	Status        string    `json:"status"`
	OpenedAt      time.Time `json:"openedAt"`
	LastCheckedAt time.Time `json:"lastCheckedAt"`
}

type feedsResponse struct {
	CheckedAt  time.Time          `json:"checkedAt"`
	Thresholds feedThresholdsJSON `json:"thresholds"`
	FireSeason fireSeasonJSON     `json:"fireSeason"`
	Feeds      []feedJSON         `json:"feeds"`
	OpenAlerts []feedAlertJSON    `json:"openAlerts"`
}

// feeds lists the workspace's live feeds with their health, and its open
// feed-health alerts. Only a role at the root of one of the workspace's
// hierarchies may see it.
func (a api) feeds(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	caller := r.Context().Value(identityKey{}).(identity)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	now := time.Now()
	if a.now != nil {
		now = a.now()
	}

	var workspaceID string
	var settings []byte
	var allowed bool
	err := a.feedDB.QueryRow(ctx, `
		SELECT w.id::text, w.settings, EXISTS (
		    SELECT 1 FROM role_assignments g
		    JOIN users u ON u.id = g.user_id AND u.oidc_subject = $1
		    JOIN units n ON n.hierarchy_id = g.hierarchy_id AND n.current_path = g.unit_path
		    WHERE g.workspace_id = w.id AND n.depth = 0)
		FROM workspaces w WHERE w.slug = $2`, caller.Subject, chi.URLParam(r, "slug")).Scan(&workspaceID, &settings, &allowed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
		// An unknown workspace looks the same as one the caller may not see.
		writeError(w, http.StatusForbidden, "A role at the top of this workspace's hierarchy is required.")
		return
	}
	if err != nil {
		a.logger.Error("Could not check access to feed health", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load feed health.")
		return
	}

	facts, err := loadFeedFacts(ctx, a.feedDB, &workspaceID, now, a.firms.liveProducts())
	if err != nil {
		a.logger.Error("Could not read feed health", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load feed health.")
		return
	}
	latencies, err := loadFeedLatency(ctx, a.feedDB, workspaceID, now)
	if err != nil {
		a.logger.Error("Could not read feed latency", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load feed health.")
		return
	}
	alerts, err := openFeedAlerts(ctx, a.feedDB, workspaceID, a.firms.liveProducts())
	if err != nil {
		a.logger.Error("Could not read feed health alerts", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load feed health.")
		return
	}

	set, err := readFeedSettings(settings)
	if err != nil {
		a.logger.Warn("Using the default feed health settings", "workspace", chi.URLParam(r, "slug"), "error", err)
	}
	response := feedsResponse{
		CheckedAt: now.UTC(),
		Thresholds: feedThresholdsJSON{
			FailingAfterMinutes: set.Thresholds.FailingAfter.Minutes(),
			DelayedAfterHours:   set.Thresholds.DelayedAfter.Hours(),
			QuietAfterHours:     set.Thresholds.QuietAfter.Hours(),
		},
		FireSeason: fireSeasonJSON{
			Start: set.Season.Start.String(), End: set.Season.End.String(),
			TimeZone: "Asia/Kolkata", InSeason: set.Season.contains(now),
		},
		Feeds:      make([]feedJSON, 0, len(facts)),
		OpenAlerts: alerts,
	}
	for _, f := range facts {
		feed := feedJSON{
			Source: f.Source, Product: f.Product, Status: feedStatus(f, set.Thresholds, now),
			LastRunAt: f.LastRunAt, LastRunStatus: f.LastRunStatus, LastError: f.LastError,
			LastSuccessAt: f.LastSuccessAt, LatestObservationAt: f.LatestObservedAt,
			FailuresLast24h: f.FailuresLast24h,
			FirstSeen:       firstSeenJSON{Records: f.FirstSeenRecords, P50Seconds: f.FirstSeenP50Seconds, P95Seconds: f.FirstSeenP95Seconds},
			Latency:         append([]feedLatency{}, latencies[feedKey{f.Source, f.Product}]...),
			Processing: processingJSON{
				Records: f.ProcessedRecords, P95Seconds: f.ProcessingP95Seconds, WithinTwentyMinutes: f.WithinGate,
			},
		}
		if f.LatestObservedAt != nil {
			delay := int64(now.Sub(*f.LatestObservedAt).Seconds())
			feed.DelaySeconds = &delay
		}
		response.Feeds = append(response.Feeds, feed)
	}
	writeJSON(w, http.StatusOK, response)
}

func openFeedAlerts(ctx context.Context, db feedDB, workspaceID string, liveProducts []string) ([]feedAlertJSON, error) {
	rows, err := db.Query(ctx, `
		SELECT id, source, product, opened_status, status, opened_at, last_checked_at
		FROM feed_health_alerts WHERE workspace_id = $1 AND resolved_at IS NULL
		  AND (source <> 'firms' OR product = ANY($2::text[]))
		ORDER BY opened_at, id`, workspaceID, liveProducts)
	if err != nil {
		return nil, err
	}
	alerts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (feedAlertJSON, error) {
		var a feedAlertJSON
		err := row.Scan(&a.ID, &a.Source, &a.Product, &a.OpenedStatus, &a.Status, &a.OpenedAt, &a.LastCheckedAt)
		return a, err
	})
	if alerts == nil {
		alerts = []feedAlertJSON{}
	}
	return alerts, err
}
