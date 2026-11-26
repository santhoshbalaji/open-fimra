package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

//go:embed daily_return_facts.sql
var dailyReturnFactsSQL string

//go:embed daily_return_coverage.sql
var dailyReturnCoverageSQL string

//go:embed state_extract_facts.sql
var stateExtractFactsSQL string

var dailyAndStateFactsSQL = "SELECT (" + dailyReturnFactsSQL + "), (" + stateExtractFactsSQL + ")"

type dailyPolicy struct {
	Hierarchy string `json:"hierarchy"`
	Level     string `json:"level"`
	TimeZone  string `json:"timeZone"`
	Cutoff    string `json:"cutoff"`
	From      string `json:"from"`
	To        string `json:"to"`
	Approver  string `json:"approver"`
}

type dailyWindow struct {
	ID                       int64
	WorkspaceID, HierarchyID string
	Depth                    int16
	Today, Day               string
	Start, Due, End          time.Time
	dailyPolicy
}

type dailyFact struct {
	Kind   string     `json:"kind"`
	Key    string     `json:"key"`
	Metric string     `json:"metric"`
	Amount float64    `json:"amount"`
	At     *time.Time `json:"at,omitempty"`
}

type dailyRevision struct {
	ID          string     `json:"id"`
	Number      int        `json:"revision"`
	Previous    *string    `json:"previous"`
	Status      string     `json:"status"`
	SubmittedBy string     `json:"submittedBy"`
	SubmittedAt time.Time  `json:"submittedAt"`
	DecidedBy   *string    `json:"decidedBy"`
	DecidedAt   *time.Time `json:"decidedAt"`
	ReviewNote  *string    `json:"reviewNote"`
	owner       string
}

type dailySnapshot struct {
	Scope       string         `json:"scope"`
	Period      reportPeriod   `json:"period"`
	DailyReturn dailyFigures   `json:"dailyReturn"`
	Review      *dailyRevision `json:"review,omitempty"`
	Language    string         `json:"language,omitempty"`
}

type dailyFigures struct {
	ExpectedUnit *dailyExpectedUnit `json:"expectedUnit,omitempty"`
	Cutoff       time.Time          `json:"cutoff"`
	Policy       dailyPolicy        `json:"policy"`
	Nil          bool               `json:"nil"`
	Remarks      string             `json:"remarks"`
	Author       string             `json:"author"`
	Facts        []dailyFact        `json:"facts,omitempty"`
	StateFacts   *[]dailyFact       `json:"stateFacts,omitempty"`
	Totals       map[string]float64 `json:"totals"`
}

type dailyExpectedUnit struct {
	Path            string  `json:"path"`
	Code            string  `json:"code"`
	Name            string  `json:"name"`
	BoundaryVersion *string `json:"boundaryVersion"`
}

type dailyRequest struct {
	Action   string `json:"action"`
	Base     string `json:"base"`
	Nil      bool   `json:"nil"`
	Remarks  string `json:"remarks"`
	Received int    `json:"received"`
	Language string `json:"language"`
}

func dailyError(status int, message string) error {
	return &reportProblem{status, message}
}

func dailyDate(day string) bool {
	_, err := time.Parse(time.DateOnly, day)
	return reportDate.MatchString(day) && !strings.HasPrefix(day, "0000") && err == nil
}

func decodeDaily(w http.ResponseWriter, r *http.Request, into any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 12<<10))
	d.DisallowUnknownFields()
	return d.Decode(into) == nil && d.Decode(new(any)) == io.EOF
}

func (a api) dailyReturnPolicy(w http.ResponseWriter, r *http.Request) {
	var p dailyPolicy
	if !decodeDaily(w, r, &p) || !dailyDate(p.From) || !dailyDate(p.To) || p.To < p.From ||
		len(p.Cutoff) != 5 || !validDailyText(p.TimeZone, 100) ||
		(p.Approver != "state_admin" && p.Approver != "division_officer") {
		writeError(w, 400, "Check the daily return settings.")
		return
	}
	if _, err := time.Parse("15:04", p.Cutoff); err != nil {
		writeError(w, 400, "Check the daily return settings.")
		return
	}
	subject := r.Context().Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	err := pgx.BeginFunc(ctx, a.pool, func(tx pgx.Tx) error {
		var workspaceID string
		err := tx.QueryRow(ctx, `SELECT workspace_id::text FROM permanent_alert_grants
WHERE slug=$1 AND oidc_subject=$2 AND role_slug='state_admin' AND depth=0 LIMIT 1`, slug, subject).Scan(&workspaceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return dailyError(403, "A workspace administrator role at the root is required.")
		}
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO daily_return_policies(workspace_id,hierarchy_id,depth,season,time_zone,cutoff,approver_role)
SELECT h.workspace_id,h.id,l.depth,daterange($4::date,$5::date,'[]'),$6,$7::text::time,$8
FROM hierarchies h JOIN hierarchy_levels l ON l.hierarchy_id=h.id
WHERE h.workspace_id=$1 AND h.slug=$2 AND l.slug=$3 AND l.depth>0 AND EXISTS (
    SELECT FROM pg_timezone_names WHERE name=$6)
AND ($8 <> 'division_officer' OR EXISTS (SELECT FROM hierarchy_levels parent
    WHERE parent.hierarchy_id=h.id AND parent.slug='division' AND parent.depth<=l.depth))`,
			workspaceID, p.Hierarchy, p.Level, p.From, p.To, p.TimeZone, p.Cutoff, p.Approver)
		var dbErr *pgconn.PgError
		if errors.As(err, &dbErr) && dbErr.Code == "23P01" {
			return dailyError(409, "A reporting season already covers those dates.")
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return dailyError(400, "Check the reporting level, approver role and time zone.")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO notification_rules(workspace_id,event,recipients,channel)
SELECT $1,event,recipients,'in_app' FROM (VALUES
    ('daily_return_due','division_officer'),('daily_return_due','field_staff'),
    ('daily_return_missing','reviewers')) r(event,recipients)
WHERE (SELECT count(*) FROM daily_return_policies WHERE workspace_id=$1)=1
ON CONFLICT DO NOTHING`, workspaceID); err != nil {
			return err
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,details)
VALUES($1,'user',$2,'daily_return.configure','workspace',$1::uuid::text,$3)`, workspaceID, subject, raw)
		return err
	})
	if err != nil {
		a.reportFailure(w, err)
		return
	}
	writeJSON(w, 201, p)
}

func validDailyText(text string, max int) bool {
	return !strings.ContainsRune(text, 0) && utf8.RuneCountInString(text) <= max
}

func loadDailyWindow(ctx context.Context, tx pgx.Tx, subject, slug, day string) (dailyWindow, error) {
	var window dailyWindow
	err := tx.QueryRow(ctx, `
SELECT p.id, p.workspace_id::text, p.hierarchy_id::text, p.depth, h.slug, l.slug, p.time_zone,
    left(p.cutoff::text,5), lower(p.season)::text, (upper(p.season)-1)::text, p.approver_role,
    local.today::text, chosen.day::text,
    daily_return_cutoff(chosen.day-1,p.cutoff,p.time_zone), boundary.cutoff, least(now(),boundary.cutoff)
FROM daily_return_policies p JOIN hierarchies h ON h.id=p.hierarchy_id
JOIN hierarchy_levels l ON (l.hierarchy_id,l.depth)=(p.hierarchy_id,p.depth)
CROSS JOIN LATERAL (SELECT (now() AT TIME ZONE p.time_zone)::date AS today) local
CROSS JOIN LATERAL (SELECT coalesce(nullif($3,'')::date,least(local.today,upper(p.season)-1)) AS day) chosen
CROSS JOIN LATERAL (SELECT daily_return_cutoff(chosen.day,p.cutoff,p.time_zone) AS cutoff) boundary
WHERE p.season @> chosen.day AND EXISTS (
    SELECT FROM permanent_alert_grants g WHERE g.slug=$2 AND g.oidc_subject=$1 AND g.workspace_id=p.workspace_id)
ORDER BY upper(p.season) DESC LIMIT 1`, subject, slug, day).Scan(
		&window.ID, &window.WorkspaceID, &window.HierarchyID, &window.Depth, &window.Hierarchy, &window.Level,
		&window.TimeZone, &window.Cutoff, &window.From, &window.To, &window.Approver, &window.Today, &window.Day,
		&window.Start, &window.Due, &window.End)
	return window, err
}

const dailyMemberSQL = `SELECT count(*) > 0, coalesce(bool_or(g.depth=0 AND g.role_slug='state_admin'),false)
FROM permanent_alert_grants g WHERE g.slug=$1 AND g.oidc_subject=$2`

func (a api) dailyReturns(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	subject := r.Context().Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	query := r.URL.Query()
	day, path, scope, version := query.Get("day"), query.Get("unit"), query.Get("scope"), query.Get("revision")
	if (day != "" && !dailyDate(day)) ||
		(path != "" && (!unitPrefix.MatchString(path) || len(path) > 2000)) ||
		(scope != "" && (!unitPrefix.MatchString(scope) || len(scope) > 2000)) {
		writeError(w, 400, "Check the day and unit.")
		return
	}
	var versionID pgtype.UUID
	if version != "" && versionID.Scan(version) != nil {
		writeError(w, 400, "Invalid return revision.")
		return
	}
	var in dailyRequest
	if r.Method == http.MethodPost {
		if !decodeDaily(w, r, &in) || day == "" || path == "" || !validDailyText(in.Remarks, 2000) ||
			in.Received < 0 || in.Received > 100000 || (in.Nil && in.Received != 0) {
			writeError(w, 400, "Check the daily return and remarks.")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var result any
	status := 200
	options := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if r.Method == http.MethodPost {
		options = pgx.TxOptions{}
	}
	err := pgx.BeginTxFunc(ctx, a.pool, options, func(tx pgx.Tx) error {
		var member, configure bool
		if err := tx.QueryRow(ctx, dailyMemberSQL, slug, subject).Scan(&member, &configure); err != nil {
			return err
		}
		if !member {
			return dailyError(403, "A role in this workspace is required.")
		}
		window, err := loadDailyWindow(ctx, tx, subject, slug, day)
		if errors.Is(err, pgx.ErrNoRows) {
			if path != "" {
				return dailyError(404, "No reporting season covers this day.")
			}
			result = map[string]any{"canConfigure": configure, "day": day}
			return nil
		}
		if err != nil {
			return err
		}
		if window.Day > window.Today {
			return dailyError(400, "Choose today or an earlier reporting day.")
		}
		if path == "" {
			var coverage json.RawMessage
			scopes := json.RawMessage("[]")
			coverageOnly := query.Get("coverageOnly") == "1"
			err = tx.QueryRow(ctx, dailyReturnCoverageSQL, window.HierarchyID, window.Depth, window.Day, window.Due,
				optionalString(scope), window.WorkspaceID, subject, window.ID, coverageOnly).Scan(&coverage)
			if err != nil {
				return err
			}
			if !coverageOnly {
				err = tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('path',u.path::text,'name',u.name)
ORDER BY u.path),'[]') FROM units u
WHERE u.hierarchy_id=$1 AND u.depth<$2 AND u.valid_to IS NULL AND EXISTS (
    SELECT FROM permanent_alert_grants g WHERE g.workspace_id=$3 AND g.oidc_subject=$4
      AND (g.depth=0 OR (g.hierarchy_id=$1 AND u.path <@ g.unit_path)))`,
					window.HierarchyID, window.Depth, window.WorkspaceID, subject).Scan(&scopes)
			}
			result = map[string]any{"policy": window.dailyPolicy, "day": window.Day, "today": window.Today,
				"cutoff": window.Due, "coverage": coverage, "scopes": scopes, "canConfigure": configure}
			return err
		}
		if r.Method == http.MethodPost {
			// Serialize submissions and decisions for this unit and reporting day.
			if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
'daily-return/' || $1::bigint || '/' || $2 || '/' || $3,0))`, window.ID, path, window.Day); err != nil {
				return err
			}
		}
		unit := dailyExpectedUnit{Path: path}
		var name string
		var canSubmit, approver bool
		err = tx.QueryRow(ctx, `SELECT u.name, u.code, u.layer_version_id::text, max(actor.display_name),
coalesce(bool_or(g.hierarchy_id=$1 AND g.unit_path=u.path),false),
coalesce(bool_or(g.role_slug=$6 AND (g.depth=0 OR
    (g.can_review AND g.hierarchy_id=$1 AND u.path <@ g.unit_path))),false)
FROM daily_return_units($1,$2,$3) u JOIN permanent_alert_grants g ON g.workspace_id=$4
    AND (g.depth=0 OR (g.hierarchy_id=$1 AND u.path <@ g.unit_path))
JOIN users actor ON actor.id=g.user_id
WHERE u.path=$5::ltree AND g.oidc_subject=$7 GROUP BY u.name, u.code, u.layer_version_id`, window.HierarchyID, window.Depth,
			window.End, window.WorkspaceID, path, window.Approver, subject).Scan(&unit.Name, &unit.Code, &unit.BoundaryVersion, &name, &canSubmit, &approver)
		if errors.Is(err, pgx.ErrNoRows) {
			return dailyError(404, "The reporting unit was not found.")
		}
		if err != nil {
			return err
		}
		history, err := readDailyHistory(ctx, tx, window, path)
		if err != nil {
			return err
		}
		var latest dailyRevision
		if len(history) > 0 {
			latest = history[0]
		}
		if r.Method == http.MethodPost {
			status = http.StatusCreated
			result, err = a.changeDailyReturn(ctx, tx, slug, subject, name, unit, window, in, latest, history, canSubmit, approver)
			return err
		}
		selected := latest
		if versionID.Valid {
			selected = dailyRevision{}
			for _, prior := range history {
				if prior.ID == versionID.String() {
					selected = prior
					break
				}
			}
			if selected.ID == "" {
				return dailyError(404, "The return revision was not found.")
			}
		}
		var figures *dailySnapshot
		if selected.ID != "" {
			figures, err = readDailySnapshot(ctx, tx, selected)
		}
		result = map[string]any{"unit": unit.Name, "cutoff": window.Due, "history": history, "snapshot": figures,
			"canSubmit": canSubmit && latest.Status != "submitted",
			"canReview": approver && latest.Status == "submitted" && latest.owner != subject}
		return err
	})
	if err != nil {
		a.reportFailure(w, err)
		return
	}
	writeJSON(w, status, result)
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func readDailyHistory(ctx context.Context, tx pgx.Tx, window dailyWindow, path string) ([]dailyRevision, error) {
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.daily_revision,r.daily_previous_id::text,
coalesce(d.decision,'submitted'),r.content #>> '{dailyReturn,author}',r.created_at,d.decided_name,d.decided_at,d.note,r.requested_by
FROM situation_reports r LEFT JOIN daily_return_decisions d ON d.report_id=r.id
WHERE r.daily_policy_id=$1 AND r.unit_path=$2::ltree AND r.period_from=$3::date ORDER BY r.daily_revision DESC`,
		window.ID, path, window.Day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := []dailyRevision{}
	for rows.Next() {
		var item dailyRevision
		if err = rows.Scan(&item.ID, &item.Number, &item.Previous, &item.Status, &item.SubmittedBy, &item.SubmittedAt,
			&item.DecidedBy, &item.DecidedAt, &item.ReviewNote, &item.owner); err != nil {
			return nil, err
		}
		history = append(history, item)
	}
	return history, rows.Err()
}

func readDailySnapshot(ctx context.Context, tx pgx.Tx, revision dailyRevision) (*dailySnapshot, error) {
	var snapshot dailySnapshot
	err := tx.QueryRow(ctx, `SELECT content #- '{dailyReturn,facts}' #- '{dailyReturn,stateFacts}' FROM situation_reports WHERE id=$1::uuid`,
		revision.ID).Scan(&snapshot)
	snapshot.Review = &revision
	return &snapshot, err
}

func (a api) changeDailyReturn(ctx context.Context, tx pgx.Tx, slug, subject, author string, unit dailyExpectedUnit, window dailyWindow,
	in dailyRequest, latest dailyRevision, history []dailyRevision, canSubmit, approver bool) (any, error) {
	if in.Action == "pdf" {
		for _, revision := range history {
			if revision.ID == in.Base && revision.Status == "approved" {
				snapshot, err := readDailySnapshot(ctx, tx, revision)
				if err != nil {
					return nil, err
				}
				if in.Language != "" && in.Language != "en" && in.Language != "hi" {
					return nil, dailyError(400, "Choose English or Hindi for the PDF.")
				}
				snapshot.Language = in.Language
				raw, err := json.Marshal(snapshot)
				if err != nil {
					return nil, err
				}
				var id string
				err = tx.QueryRow(ctx, `INSERT INTO situation_reports(workspace_id,requested_by,hierarchy_id,unit_path,period_from,
period_to,content,expires_at,snapshot_grants) SELECT $3,$2,$4,$5::ltree,$6::date,$6::date,$7,now()+$8::interval,
`+situationReportGrants+` WHERE $1<>'' RETURNING id::text`, slug, subject, window.WorkspaceID, window.HierarchyID,
					unit.Path, window.Day, raw, pgtype.Interval{Microseconds: a.reportTTL().Microseconds(), Valid: true}).Scan(&id)
				if err != nil {
					return nil, err
				}
				return map[string]string{"id": id}, auditDailyReturn(ctx, tx, window.WorkspaceID, subject, "pdf", id, revision.ID)
			}
		}
		return nil, dailyError(409, "Choose an approved return for the PDF.")
	}
	if in.Base != latest.ID {
		return nil, dailyError(409, "The return changed. Reload it before continuing.")
	}
	switch in.Action {
	case "submit":
		if !canSubmit {
			return nil, dailyError(403, "The producing unit must submit the return.")
		}
		if latest.Status == "submitted" {
			return nil, dailyError(409, "The latest revision is awaiting a decision.")
		}
		id, err := submitDailyReturn(ctx, tx, window, subject, author, unit, in, latest)
		if err != nil {
			return nil, err
		}
		return map[string]string{"id": id}, auditDailyReturn(ctx, tx, window.WorkspaceID, subject, "submit", id, latest.ID)
	case "approved", "returned":
		if !approver || latest.owner == subject {
			return nil, dailyError(403, "The designated approver must review the return.")
		}
		if latest.Status != "submitted" || (in.Action == "returned" && strings.TrimSpace(in.Remarks) == "") {
			return nil, dailyError(409, "Choose a submitted return and give a reason when returning it.")
		}
		_, err := tx.Exec(ctx, `INSERT INTO daily_return_decisions(report_id,decision,decided_by,decided_name,note)
VALUES($1::uuid,$2,$3,$4,$5)`, latest.ID, in.Action, subject, author, strings.TrimSpace(in.Remarks))
		if err != nil {
			return nil, err
		}
		return map[string]string{"id": latest.ID}, auditDailyReturn(ctx, tx, window.WorkspaceID, subject, in.Action, latest.ID, latest.Previous)
	default:
		return nil, dailyError(400, "Choose a daily return action.")
	}
}

func submitDailyReturn(ctx context.Context, tx pgx.Tx, window dailyWindow, subject, author string, unit dailyExpectedUnit,
	in dailyRequest, latest dailyRevision) (string, error) {
	facts := []dailyFact{}
	stateFacts := []dailyFact{}
	if !in.Nil {
		if err := tx.QueryRow(ctx, dailyAndStateFactsSQL, window.WorkspaceID, window.HierarchyID, unit.Path,
			window.Start, window.End, window.TimeZone).Scan(&facts, &stateFacts); err != nil {
			return "", err
		}
		if in.Received > 0 {
			facts = append(facts, dailyFact{Kind: "received", Key: unit.Path, Metric: "assistance.received", Amount: float64(in.Received)})
		}
	}
	totals := map[string]float64{}
	for _, fact := range facts {
		totals[fact.Metric] += fact.Amount
	}
	snapshot := dailySnapshot{Scope: unit.Name,
		Period: reportPeriod{From: window.Day, To: window.Day, Days: 1, Start: window.Start, End: window.End,
			TimeZone: window.TimeZone, Partial: window.End.Before(window.Due)},
		DailyReturn: dailyFigures{ExpectedUnit: &unit, Policy: window.dailyPolicy, Cutoff: window.Due, Nil: in.Nil,
			Remarks: strings.TrimSpace(in.Remarks), Author: author, Facts: facts, StateFacts: &stateFacts, Totals: totals}}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO situation_reports(workspace_id,requested_by,hierarchy_id,unit_path,period_from,
period_to,content,expires_at,status,daily_policy_id,daily_revision,daily_previous_id)
VALUES($1,$2,$3,$4::ltree,$5::date,$5::date,$6,'infinity','draft',$7,$8,$9::uuid) RETURNING id::text`,
		window.WorkspaceID, subject, window.HierarchyID, unit.Path, window.Day, raw, window.ID, latest.Number+1, optionalString(latest.ID)).Scan(&id)
	return id, err
}

func auditDailyReturn(ctx context.Context, tx pgx.Tx, workspace, subject, action, id string, previous any) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,details)
VALUES($1,'user',$2,'daily_return.' || $3,'situation_report',$4,jsonb_build_object('relatedRevision',$5::text))`,
		workspace, subject, action, id, previous)
	return err
}
