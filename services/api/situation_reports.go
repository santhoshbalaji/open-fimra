package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed situation_report.sql
var situationTailSQL string

// situationReportSQL starts with the dashboard's scope, so the report's lists
// and map use the same alerts as its counts.
var situationReportSQL = scopeSQL + situationTailSQL

const (
	// maxReportDays is the longest period one report covers: a fire season.
	maxReportDays = 184
	// reportMapAlerts is the most alerts drawn on a report's map. The counts
	// always include every alert.
	reportMapAlerts = 5000
	// reportListRows is the most rows in each list of a report.
	reportListRows = 200
	// defaultReportTTL is how long a PDF can be downloaded when exports have no
	// lifetime of their own.
	defaultReportTTL = 24 * time.Hour
)

var reportDate = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// workflowOrder lists the default steps in workflow order, then the outcomes.
// Other steps follow in alphabetical order.
var workflowOrder = []string{"new", "acknowledged", "team_sent", "reached_site", "controlled", "extinguished",
	"first_report", "final_report", "verified", "closed", "false_alarm", "duplicate", "controlled_burn", "not_reachable"}

var milestoneLabels = map[string]string{
	"alerted": "Alert raised", "acknowledged": "Acknowledged", "team_sent": "Team sent",
	"reached_site": "Reached site", "controlled": "Controlled", "extinguished": "Extinguished",
}

// reportLabels are the words for the codes in a report, so the web page and
// the PDF print the same text.
var reportLabels = map[string]map[string]string{
	"forestClasses": {"inside": "Inside forest", "near_edge": "Near the forest edge", "outside": "Outside forest",
		"fixed_heat_source": "Suspected fixed heat source"},
	"queues": {"unit": "Unit", "edge_review": "Edge review", "triage": "Triage"},
	"roles":  {"state_admin": "State administrator", "division_officer": "Division officer", "field_staff": "Field staff"},
}

// reportNotes explain the numbers. The web page and the PDF print them as they are.
var reportNotes = []string{
	"Days run from midnight to midnight in India Standard Time. A period that includes today ends when the report was made.",
	"Detections and alerts are counted by the time of the satellite observation, with the same rules as the control-room dashboard.",
	"Steps, active and closed alerts, overdue alerts and field reports show each alert's state when the report was made.",
	"An alert is overdue when it has stayed at its step longer than the shortest time limit for leaving that step.",
	"Alert raised is the time from the satellite observation to the alert. Every other response time runs from the alert being raised to the first time the step log records that step. The median is the middle value; the 90th percentile is the value 9 in 10 alerts reached within.",
	"Incidents are those with at least one alert in the period. An incident counts as opened in the period when its first alert you can see was observed in the period.",
	"Field reports are the forms submitted for the period's alerts. A form is missing when an alert is at a step that needs it and no submission counts for it.",
}

type reportRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Hours is the other kind of period: the hours before now, as on the
	// dashboard.
	Hours     *int    `json:"hours"`
	UnitPath  *string `json:"unitPath"`
	Hierarchy string  `json:"hierarchy"`
}

type reportPeriod struct {
	// Hours is set for a period of hours before now, and From and To are then
	// the days it touches.
	Hours    *int      `json:"hours"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Days     int       `json:"days"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	TimeZone string    `json:"timeZone"`
	// Partial is true when the period includes today, so it ends at the
	// time the report was made.
	Partial bool `json:"partial"`
}

type reportUnit struct {
	Hierarchy  string `json:"hierarchy"`
	Path       string `json:"path"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Level      string `json:"level"`
	LevelLabel string `json:"levelLabel"`
	Depth      int    `json:"depth"`
}

type reportGrant struct {
	Role       string `json:"role"`
	LevelLabel string `json:"levelLabel"`
	UnitName   string `json:"unitName"`
}

type reportStep struct {
	Step     string `json:"step"`
	Label    string `json:"label"`
	Terminal bool   `json:"terminal"`
	Count    int    `json:"count"`
}

type reportMilestone struct {
	Step  string `json:"step"`
	Label string `json:"label"`
	responsePercentiles
}

type reportAlerts struct {
	Total  int `json:"total"`
	Active int `json:"active"`
	Closed int `json:"closed"`
}

// situationReport is the whole report. The web page shows it, and the geo
// worker draws exactly this as a PDF.
type situationReport struct {
	Workspace     workspace                    `json:"workspace"`
	Unit          *reportUnit                  `json:"unit"`
	Scope         string                       `json:"scope"`
	Period        reportPeriod                 `json:"period"`
	GeneratedAt   time.Time                    `json:"generatedAt"`
	PreparedFor   []reportGrant                `json:"preparedFor"`
	Counts        summaryCounts                `json:"counts"`
	Alerts        reportAlerts                 `json:"alerts"`
	Steps         []reportStep                 `json:"steps"`
	ResponseTimes []reportMilestone            `json:"responseTimes"`
	PlaceLevels   []string                     `json:"placeLevels"`
	Incidents     json.RawMessage              `json:"incidents"`
	FieldReports  json.RawMessage              `json:"fieldReports"`
	OverdueAlerts json.RawMessage              `json:"overdueAlerts"`
	UnitLevel     *string                      `json:"unitLevel"`
	Units         json.RawMessage              `json:"units"`
	Map           json.RawMessage              `json:"map"`
	Labels        map[string]map[string]string `json:"labels"`
	Notes         []string                     `json:"notes"`
}

// reportProblem is a refusal with its HTTP status and plain-English message.
type reportProblem struct {
	status  int
	message string
}

func (p *reportProblem) Error() string { return p.message }

// readReportQuery reads a report request from the query string of a GET.
func readReportQuery(q url.Values) reportRequest {
	req := reportRequest{From: q.Get("from"), To: q.Get("to"), Hierarchy: q.Get("hierarchy")}
	if q.Has("unitPath") && q.Get("unitPath") != "" {
		path := q.Get("unitPath")
		req.UnitPath = &path
	}
	if q.Has("hours") {
		hours, err := strconv.Atoi(q.Get("hours"))
		if err != nil {
			hours = -1
		}
		req.Hours = &hours
	}
	return req
}

// reportWindow checks the request's period and returns it. now is the
// database's time for the report.
func reportWindow(req reportRequest, now time.Time) (reportPeriod, error) {
	bad := &reportProblem{http.StatusBadRequest, "Give from, and optionally to, as dates such as 2026-03-15, or hours."}
	if req.Hours != nil {
		if req.From != "" || req.To != "" {
			return reportPeriod{}, &reportProblem{http.StatusBadRequest, "Give either dates or hours, not both."}
		}
		if *req.Hours < 1 || *req.Hours > 720 {
			return reportPeriod{}, &reportProblem{http.StatusBadRequest, "hours must be an integer from 1 to 720."}
		}
		start := now.Add(-time.Duration(*req.Hours) * time.Hour)
		first, last := start.In(indiaTime), now.In(indiaTime)
		return reportPeriod{Hours: req.Hours, From: first.Format(time.DateOnly), To: last.Format(time.DateOnly),
			Days: int(time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, indiaTime).Sub(
				time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, indiaTime)).Hours()/24+0.5) + 1,
			Start: start.UTC(), End: now.UTC(), TimeZone: "Asia/Kolkata", Partial: true}, nil
	}
	if req.To == "" {
		req.To = req.From
	}
	if !reportDate.MatchString(req.From) || !reportDate.MatchString(req.To) {
		return reportPeriod{}, bad
	}
	from, err1 := time.ParseInLocation(time.DateOnly, req.From, indiaTime)
	to, err2 := time.ParseInLocation(time.DateOnly, req.To, indiaTime)
	if err1 != nil || err2 != nil {
		return reportPeriod{}, bad
	}
	local := now.In(indiaTime)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, indiaTime)
	switch {
	case to.Before(from):
		return reportPeriod{}, &reportProblem{http.StatusBadRequest, "The period must end on or after the day it starts."}
	case to.After(today):
		return reportPeriod{}, &reportProblem{http.StatusBadRequest, "The period cannot end after today."}
	}
	days := int(to.Sub(from).Hours()/24+0.5) + 1
	if days > maxReportDays {
		return reportPeriod{}, &reportProblem{http.StatusBadRequest,
			fmt.Sprintf("A report covers at most %d days. Choose a shorter period.", maxReportDays)}
	}
	end := to.AddDate(0, 0, 1).Add(-time.Microsecond)
	p := reportPeriod{From: req.From, To: req.To, Days: days, Start: from.UTC(), End: end.UTC(), TimeZone: "Asia/Kolkata"}
	if !end.Before(now) {
		p.End, p.Partial = now.UTC(), true
	}
	return p, nil
}

// resolveReportUnit finds the report's unit and checks that the caller may
// report on it. With no unit path, the caller's single highest grant is the
// unit, and several grants at that height make a report on all of them.
func resolveReportUnit(ctx context.Context, tx pgx.Tx, subject, slug string, req reportRequest) (*reportUnit, workspace, []reportGrant, error) {
	var ws workspace
	var grants []reportGrant
	var units []reportUnit
	var allowed bool
	if req.UnitPath != nil && (!unitPrefix.MatchString(*req.UnitPath) || len(*req.UnitPath) > 2000) {
		return nil, ws, nil, &reportProblem{http.StatusBadRequest, "Invalid unit path."}
	}
	if len(req.Hierarchy) > 64 {
		return nil, ws, nil, &reportProblem{http.StatusBadRequest, "Invalid hierarchy."}
	}
	err := tx.QueryRow(ctx, `WITH grants AS (SELECT * FROM alert_grants WHERE oidc_subject=$1 AND slug=$2),
granted AS (
    SELECT g.*, h.slug AS hierarchy, u.code, u.name, l.slug AS level, l.labels->>'en' AS level_label
    FROM grants g JOIN hierarchies h ON h.id=g.hierarchy_id
    JOIN units u ON u.hierarchy_id=g.hierarchy_id AND u.current_path=g.unit_path
    JOIN hierarchy_levels l ON (l.hierarchy_id,l.depth)=(u.hierarchy_id,u.depth)
), chosen AS (
    SELECT h.slug AS hierarchy, u.path::text AS path, u.code, u.name, l.slug AS level, l.labels->>'en' AS level_label, u.depth
    FROM hierarchies h JOIN units u ON u.hierarchy_id=h.id AND u.valid_to IS NULL
    JOIN hierarchy_levels l ON (l.hierarchy_id,l.depth)=(u.hierarchy_id,u.depth)
    WHERE $3::text IS NOT NULL AND h.workspace_id=(SELECT workspace_id FROM grants LIMIT 1)
      AND h.slug=coalesce(nullif($4,''),(SELECT hierarchy FROM granted ORDER BY depth, unit_path LIMIT 1))
      AND u.path=$3::ltree
    UNION ALL
    SELECT DISTINCT hierarchy, unit_path::text, code, name, level, level_label, depth FROM granted
    WHERE $3::text IS NULL AND depth=(SELECT min(depth) FROM granted)
)
SELECT coalesce((SELECT jsonb_build_object('slug',w.slug,'name',w.name) FROM workspaces w
        WHERE w.slug=$2 AND EXISTS (SELECT FROM grants)),'{}'),
    (SELECT coalesce(jsonb_agg(DISTINCT jsonb_build_object('role',role_slug,'levelLabel',level_label,'unitName',name)),'[]') FROM granted),
    (SELECT coalesce(jsonb_agg(jsonb_build_object('hierarchy',hierarchy,'path',path,'code',code,'name',name,
        'level',level,'levelLabel',level_label,'depth',depth) ORDER BY hierarchy, path),'[]') FROM chosen),
    $3::text IS NULL OR EXISTS (SELECT FROM granted g JOIN chosen c ON true
        WHERE g.depth=0 OR (g.hierarchy=c.hierarchy AND c.path::ltree <@ g.unit_path))`,
		subject, slug, req.UnitPath, req.Hierarchy).Scan(&ws, &grants, &units, &allowed)
	switch {
	case err != nil:
		return nil, ws, nil, err
	case ws.Slug == "":
		return nil, ws, nil, &reportProblem{http.StatusForbidden, "A role in this workspace is required."}
	case req.UnitPath != nil && len(units) == 0:
		return nil, ws, nil, &reportProblem{http.StatusNotFound, "That unit was not found."}
	case !allowed:
		return nil, ws, nil, &reportProblem{http.StatusForbidden, "You can report only on units in your own area."}
	case len(units) == 1:
		return &units[0], ws, grants, nil
	}
	return nil, ws, grants, nil
}

// buildSituationReport works out a report inside tx with the caller's grants.
func buildSituationReport(ctx context.Context, tx pgx.Tx, subject, slug string, req reportRequest) (situationReport, error) {
	var rep situationReport
	// The transaction's time, which the counts also use as now.
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		return rep, err
	}
	period, err := reportWindow(req, now)
	if err != nil {
		return rep, err
	}
	unit, ws, grants, err := resolveReportUnit(ctx, tx, subject, slug, req)
	if err != nil {
		return rep, err
	}
	rep = situationReport{Workspace: ws, Unit: unit, Period: period, GeneratedAt: now.UTC(),
		PreparedFor: grants, Labels: reportLabels, Notes: reportNotes}
	var hierarchyID, unitPath, countPath *string
	rep.Scope = "All units you can see"
	if unit != nil {
		rep.Scope = unit.Name + " (" + unit.LevelLabel + ")"
		unitPath = &unit.Path
		// A whole hierarchy also covers alerts that matched no unit, as on
		// the dashboard without a unit filter.
		if unit.Depth > 0 {
			countPath = &unit.Path
		}
		var id string
		if err := tx.QueryRow(ctx, `SELECT h.id::text FROM hierarchies h JOIN workspaces w ON w.id=h.workspace_id
 WHERE w.slug=$1 AND h.slug=$2`, slug, unit.Hierarchy).Scan(&id); err != nil {
			return rep, err
		}
		hierarchyID = &id
	}
	start, end := period.Start, period.End
	counts, allowed, _, err := loadCounts(ctx, tx, subject, slug, countWindow{start: &start, end: &end, path: countPath})
	if err != nil {
		return rep, err
	}
	if !allowed {
		return rep, &reportProblem{http.StatusForbidden, "A role in this workspace is required."}
	}
	rep.Counts = counts
	var tail struct {
		StepLabels map[string]struct {
			Label    string `json:"label"`
			Terminal bool   `json:"terminal"`
		} `json:"stepLabels"`
		PlaceLevels   []string        `json:"placeLevels"`
		Incidents     json.RawMessage `json:"incidents"`
		FieldReports  json.RawMessage `json:"fieldReports"`
		OverdueAlerts json.RawMessage `json:"overdueAlerts"`
		UnitLevel     *string         `json:"unitLevel"`
		Units         json.RawMessage `json:"units"`
		Map           json.RawMessage `json:"map"`
	}
	var raw []byte
	if err := tx.QueryRow(ctx, situationReportSQL, subject, slug, &start, nil, &end, "", countPath,
		hierarchyID, unitPath, reportMapAlerts, reportListRows).Scan(&raw); err != nil {
		return rep, err
	}
	if err := json.Unmarshal(raw, &tail); err != nil {
		return rep, err
	}
	rep.PlaceLevels, rep.Incidents, rep.FieldReports, rep.OverdueAlerts = tail.PlaceLevels, tail.Incidents, tail.FieldReports, tail.OverdueAlerts
	rep.UnitLevel, rep.Units, rep.Map = tail.UnitLevel, tail.Units, tail.Map
	for _, n := range counts.Queues {
		rep.Alerts.Total += n
	}
	rep.Alerts.Active = counts.Active
	rep.Alerts.Closed = rep.Alerts.Total - counts.Active
	rep.Steps = []reportStep{}
	for step, n := range counts.Steps {
		label := tail.StepLabels[step]
		if label.Label == "" {
			label.Label = step
		}
		rep.Steps = append(rep.Steps, reportStep{Step: step, Label: label.Label, Terminal: label.Terminal, Count: n})
	}
	slices.SortFunc(rep.Steps, func(a, b reportStep) int {
		ia, ib := slices.Index(workflowOrder, a.Step), slices.Index(workflowOrder, b.Step)
		if ia < 0 {
			ia = len(workflowOrder)
		}
		if ib < 0 {
			ib = len(workflowOrder)
		}
		if ia != ib {
			return ia - ib
		}
		return strings.Compare(a.Step, b.Step)
	})
	for _, step := range responseMilestones {
		rep.ResponseTimes = append(rep.ResponseTimes, reportMilestone{step, milestoneLabels[step], counts.ResponseSeconds[step]})
	}
	return rep, nil
}

// reportTx opens the transaction a report is worked out in. Every query in it
// sees the same snapshot, so the counts and lists agree.
func (a api) reportTx(ctx context.Context) (pgx.Tx, error) {
	if a.pool != nil {
		return a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	}
	return a.reader.Begin(ctx)
}

func (a api) reportFailure(w http.ResponseWriter, err error) {
	var problem *reportProblem
	if errors.As(err, &problem) {
		writeError(w, problem.status, problem.message)
		return
	}
	a.logger.Error("Could not make the situation report", "error", err)
	writeError(w, http.StatusInternalServerError, "Could not make the situation report.")
}

func reportAuditDetails(rep situationReport) []byte {
	details := map[string]any{"from": rep.Period.From, "to": rep.Period.To, "alerts": rep.Alerts.Total}
	if rep.Unit != nil {
		details["hierarchy"], details["unitPath"] = rep.Unit.Hierarchy, rep.Unit.Path
	}
	data, _ := json.Marshal(details)
	return data
}

// situationReport returns a report as JSON for the web page, and records it
// in the audit log.
func (a api) situationReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	caller := r.Context().Value(identityKey{}).(identity)
	slug := chi.URLParam(r, "slug")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tx, err := a.reportTx(ctx)
	if err != nil {
		a.reportFailure(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rep, err := buildSituationReport(ctx, tx, caller.Subject, slug, readReportQuery(r.URL.Query()))
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
 SELECT id, 'user', $2, 'report.view', 'workspace', id::text, $3 FROM workspaces WHERE slug=$1`, slug, caller.Subject, reportAuditDetails(rep))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.reportFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

type reportStatus struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	From       string     `json:"from"`
	To         string     `json:"to"`
	Scope      string     `json:"scope"`
	PageCount  *int       `json:"pageCount"`
	FileBytes  *int       `json:"fileBytes"`
	FileName   string     `json:"fileName,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	Error      *string    `json:"error"`
}

func (a api) reportTTL() time.Duration {
	if a.exports.ttl > 0 {
		return a.exports.ttl
	}
	return defaultReportTTL
}

// createSituationReport saves a report and queues its PDF for the geo worker.
func (a api) createSituationReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	var req reportRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "Send the report request as one JSON object.")
		return
	}
	caller := r.Context().Value(identityKey{}).(identity)
	slug := chi.URLParam(r, "slug")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tx, err := a.reportTx(ctx)
	if err != nil {
		a.reportFailure(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rep, err := buildSituationReport(ctx, tx, caller.Subject, slug, req)
	var id string
	if err == nil {
		var content []byte
		content, err = json.Marshal(rep)
		if err == nil {
			var hierarchy, path *string
			if rep.Unit != nil {
				hierarchy, path = &rep.Unit.Hierarchy, &rep.Unit.Path
			}
			err = tx.QueryRow(ctx, `INSERT INTO situation_reports (workspace_id, requested_by, hierarchy_id, unit_path,
    period_from, period_to, content, expires_at)
SELECT w.id, $2, h.id, $4::ltree, $5::date, $6::date, $7, now() + $8::interval
FROM workspaces w LEFT JOIN hierarchies h ON h.workspace_id=w.id AND h.slug=$3
WHERE w.slug=$1 RETURNING id::text`, slug, caller.Subject, hierarchy, path, rep.Period.From, rep.Period.To, content,
				pgtype.Interval{Microseconds: a.reportTTL().Microseconds(), Valid: true}).Scan(&id)
		}
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
 SELECT workspace_id, 'user', $2, 'report.create', 'situation_report', id::text, $3 FROM situation_reports WHERE id=$1`,
			id, caller.Subject, reportAuditDetails(rep))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		a.reportFailure(w, err)
		return
	}
	status, err := a.readSituationReport(r.Context(), slug, id, caller.Subject)
	if err != nil {
		a.reportFailure(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/workspaces/"+slug+"/situation-reports/"+id)
	writeJSON(w, http.StatusAccepted, status)
}

// readSituationReport returns the caller's own report. Other users' reports are not found.
func (a api) readSituationReport(ctx context.Context, slug, id, subject string) (reportStatus, error) {
	var s reportStatus
	var workspaceSlug string
	err := a.reader.QueryRow(ctx, `SELECT r.id::text, r.status, r.period_from::text, r.period_to::text,
    coalesce(r.content->>'scope',''), r.page_count, octet_length(r.pdf), r.created_at, r.finished_at, r.expires_at,
    r.error_message, w.slug
FROM situation_reports r JOIN workspaces w ON w.id=r.workspace_id
WHERE r.id=$1::uuid AND w.slug=$2 AND r.requested_by=$3`, id, slug, subject).Scan(&s.ID, &s.Status, &s.From, &s.To,
		&s.Scope, &s.PageCount, &s.FileBytes, &s.CreatedAt, &s.FinishedAt, &s.ExpiresAt, &s.Error, &workspaceSlug)
	if err != nil {
		return s, err
	}
	if s.Status == "ready" {
		period := s.From
		if s.To != s.From {
			period += "-to-" + s.To
		}
		s.FileName = fmt.Sprintf("open-fimra-%s-situation-report-%s.pdf", workspaceSlug, period)
	}
	return s, nil
}

// ownSituationReport finds the caller's report and checks that the caller
// still has a role in the workspace. It writes the error response when it
// returns false.
func (a api) ownSituationReport(w http.ResponseWriter, r *http.Request) (reportStatus, bool) {
	w.Header().Set("Cache-Control", "private, no-store")
	caller := r.Context().Value(identityKey{}).(identity)
	slug := chi.URLParam(r, "slug")
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusNotFound, "The report was not found.")
		return reportStatus{}, false
	}
	var member bool
	if err := a.reader.QueryRow(r.Context(), `SELECT EXISTS (SELECT FROM alert_grants WHERE oidc_subject=$1 AND slug=$2)`,
		caller.Subject, slug).Scan(&member); err != nil {
		a.reportFailure(w, err)
		return reportStatus{}, false
	}
	if !member {
		writeError(w, http.StatusForbidden, "A role in this workspace is required.")
		return reportStatus{}, false
	}
	status, err := a.readSituationReport(r.Context(), slug, id.String(), caller.Subject)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "The report was not found.")
		return status, false
	}
	if err != nil {
		a.reportFailure(w, err)
		return status, false
	}
	return status, true
}

func (a api) situationReportStatus(w http.ResponseWriter, r *http.Request) {
	if status, ok := a.ownSituationReport(w, r); ok {
		writeJSON(w, http.StatusOK, status)
	}
}

func (a api) situationReportFile(w http.ResponseWriter, r *http.Request) {
	status, ok := a.ownSituationReport(w, r)
	if !ok {
		return
	}
	switch {
	case status.Status == "expired" || !time.Now().Before(status.ExpiresAt):
		writeError(w, http.StatusGone, "This report has expired. Make it again.")
		return
	case status.Status == "pending":
		writeError(w, http.StatusConflict, "This report is still being prepared.")
		return
	case status.Status == "failed":
		writeError(w, http.StatusConflict, "This report could not be drawn. Make it again.")
		return
	}
	caller := r.Context().Value(identityKey{}).(identity)
	var pdf []byte
	err := a.reader.QueryRow(r.Context(), `WITH logged AS (
    INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
    SELECT workspace_id, 'user', $2, 'report.download', 'situation_report', id::text,
           jsonb_build_object('from', period_from, 'to', period_to, 'pages', page_count)
    FROM situation_reports WHERE id=$1::uuid AND status='ready' RETURNING 1)
SELECT pdf FROM situation_reports WHERE id=$1::uuid AND status='ready' AND EXISTS (SELECT FROM logged)`,
		status.ID, caller.Subject).Scan(&pdf)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusGone, "This report has expired. Make it again.")
		return
	}
	if err != nil {
		a.reportFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+status.FileName+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

type reportableUnit struct {
	Hierarchy  string `json:"hierarchy"`
	Path       string `json:"path"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Depth      int    `json:"depth"`
	LevelLabel string `json:"levelLabel"`
}

// reportUnits lists the units the caller may report on: their granted units
// and every unit below them.
func (a api) reportUnits(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	caller := r.Context().Value(identityKey{}).(identity)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var member bool
	var units []reportableUnit
	err := a.reader.QueryRow(ctx, `WITH grants AS (SELECT * FROM alert_grants WHERE oidc_subject=$1 AND slug=$2)
SELECT EXISTS (SELECT FROM grants), coalesce((SELECT jsonb_agg(jsonb_build_object('hierarchy',h.slug,'path',u.path::text,
    'code',u.code,'name',u.name,'depth',u.depth,'levelLabel',l.labels->>'en') ORDER BY h.slug, u.path)
  FROM (SELECT u.* FROM units u JOIN hierarchies h ON h.id=u.hierarchy_id
        WHERE u.valid_to IS NULL AND EXISTS (SELECT FROM grants g WHERE (g.depth=0 AND g.workspace_id=h.workspace_id)
            OR (g.hierarchy_id=u.hierarchy_id AND u.path <@ g.unit_path))
        ORDER BY h.slug, u.path LIMIT 5000) u
  JOIN hierarchies h ON h.id=u.hierarchy_id
  JOIN hierarchy_levels l ON (l.hierarchy_id,l.depth)=(u.hierarchy_id,u.depth)),'[]')`,
		caller.Subject, chi.URLParam(r, "slug")).Scan(&member, &units)
	if err != nil {
		a.reportFailure(w, err)
		return
	}
	if !member {
		writeError(w, http.StatusForbidden, "A role in this workspace is required.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"units": units})
}

// expireSituationReports marks reports past their expiry time as expired and
// removes their content and file.
func expireSituationReports(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	tag, err := pool.Exec(ctx, `UPDATE situation_reports SET status='expired', content='{}', pdf=NULL, page_count=NULL,
    error_message=NULL, finished_at=coalesce(finished_at, now())
WHERE status IN ('pending','ready','failed') AND expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("expire situation reports: %w", err)
	}
	return tag.RowsAffected(), nil
}
