package main

import (
	"context"
	_ "embed"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	extract "github.com/santhoshbalaji/open-fimra/packages/state-extract"
)

//go:embed state_extract_coverage.sql
var stateExtractCoverageSQL string

//go:embed state_extract_area.sql
var stateExtractAreaSQL string

//go:embed state_extract_preparedness.sql
var stateExtractPreparednessSQL string

type stateExtractPair struct {
	Incident     extract.Document `json:"incident"`
	Preparedness extract.Document `json:"preparedness"`
}

func buildStateExtract(ctx context.Context, tx pgx.Tx, workspace, from, to string, authority extract.Authority) (stateExtractPair, error) {
	var docs stateExtractPair
	root := extract.Document{SchemaVersion: 1, Authority: authority}
	var policy int64
	window := &root.Window
	window.From, window.To = from, to
	err := tx.QueryRow(ctx, `SELECT id,lower(season)::text,(upper(season)-1)::text,time_zone,left(cutoff::text,5),
daily_return_cutoff($2::date-1,cutoff,time_zone),daily_return_cutoff($3::date,cutoff,time_zone)
FROM daily_return_policies WHERE workspace_id=$1 AND season @> $2::date AND season @> $3::date
AND daily_return_cutoff($3::date,cutoff,time_zone) <= now()`, workspace, from, to).Scan(&policy, &window.SeasonFrom,
		&window.SeasonTo, &window.TimeZone, &window.Cutoff, &window.StartsAt, &window.EndsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return docs, dailyError(422, "Choose a closed window within one reporting season.")
	}
	if err != nil {
		return docs, err
	}
	window.StartsAt, window.EndsAt = window.StartsAt.UTC(), window.EndsAt.UTC()
	docs.Incident, docs.Preparedness = root, root
	docs.Incident.Kind, docs.Preparedness.Kind = "incident", "preparedness"
	docs.Incident.Source = "approved_daily_returns_and_assessments"
	docs.Preparedness.Source = "approved_preparedness_records"
	if err := loadStateCoverage(ctx, tx, policy, &docs.Incident, workspace); err != nil {
		return docs, err
	}
	docs.Preparedness.Companion, err = loadStatePreparedness(ctx, tx, workspace, *window)
	return docs, err
}

func loadStateCoverage(ctx context.Context, tx pgx.Tx, policy int64, doc *extract.Document, workspace string) error {
	rows, err := tx.Query(ctx, stateExtractCoverageSQL, policy, doc.Window.From, doc.Window.To)
	if err != nil {
		return err
	}
	defer rows.Close()
	coverage := &extract.Coverage{Units: []extract.UnitDay{}}
	dedup := make(map[[2]string]dailyFact)
	confirmed := make(map[int64]bool)
	observed, complete, nilUnits := 0, 0, 0
	seen := map[string]bool{}
	for rows.Next() {
		var unit extract.UnitDay
		var approved, nilReturn, full bool
		var stateFacts *[]dailyFact
		var dailyFacts []dailyFact
		if err := rows.Scan(&unit.Day, &unit.Code, &unit.BoundaryVersion, &approved, &nilReturn, &full, &stateFacts, &dailyFacts); err != nil {
			return err
		}
		if key := unit.Day + "/" + unit.Code; seen[key] || !stateUnitCode.MatchString(unit.Code) {
			return dailyError(422, "Set a distinct exchange code for each reporting unit.")
		} else {
			seen[key] = true
		}
		coverage.Expected++
		if coverage.Expected > 25000 {
			return dailyError(422, "Choose a shorter reporting window.")
		}
		unit.Status = extract.NotReported
		if approved {
			coverage.Reported++
			unit.Status = extract.Incomplete
			if stateFacts != nil || nilReturn {
				observed++
				var detections int64
				unit.DetectionRecords = &detections
				if full {
					complete++
					unit.Status = extract.Reported
					if nilReturn {
						nilUnits++
						unit.Status = extract.Zero
					}
				}
				if !nilReturn {
					for _, fact := range *stateFacts {
						if fact.Kind == "detection" {
							detections++
						}
						switch fact.Kind {
						case "confirmed_active", "confirmed_new", "closure_backlog", "controlled", "extinguished", "closed":
							if id, err := strconv.ParseInt(fact.Key, 10, 64); err == nil {
								confirmed[id] = true
							}
						}
						if unit.Day == doc.Window.To || !slices.Contains(
							[]string{"awaiting", "confirmed_active", "closure_backlog"}, fact.Kind) {
							dedup[[2]string{fact.Kind, fact.Key}] = fact
						}
					}
					for _, fact := range dailyFacts {
						switch fact.Kind {
						case "requested", "response.time", "response.count":
							dedup[[2]string{fact.Kind, fact.Key}] = fact
						case "overdue", "overdue_alert":
							if unit.Day == doc.Window.To {
								dedup[[2]string{fact.Kind, fact.Key}] = fact
							}
						case "received":
							dedup[[2]string{fact.Kind, unit.Day + "/" + fact.Key}] = fact
						}
					}
				}
			}
		}
		coverage.Units = append(coverage.Units, unit)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	coverage.Status = extract.NotReported
	switch {
	case coverage.Reported == 0:
	case complete < coverage.Expected:
		coverage.Status = extract.Incomplete
	case nilUnits == coverage.Expected:
		coverage.Status = extract.Zero
	default:
		coverage.Status = extract.Reported
	}
	doc.Coverage = coverage
	if observed == 0 {
		return nil
	}
	ids := make([]int64, 0, len(confirmed))
	for id := range confirmed {
		ids = append(ids, id)
	}
	// Earlier snapshots can contain IDs merged later in this same window.
	// Resolve those aliases before counting case metrics or looking up area.
	roots, err := stateIncidentRoots(ctx, tx, workspace, ids, doc.Window.EndsAt)
	if err != nil {
		return err
	}
	for key, fact := range dedup {
		if root := roots[fact.Key]; strings.HasPrefix(fact.Metric, "incident.") && root != "" && root != fact.Key {
			delete(dedup, key)
			dedup[[2]string{fact.Kind, root}] = fact
		}
	}
	doc.Totals = summarizeStateFacts(dedup)
	doc.Totals.Area, err = loadStateArea(ctx, tx, workspace, doc.Authority.StateCode, ids, roots, doc.Window)
	return err
}

func stateIncidentRoots(ctx context.Context, tx pgx.Tx, workspace string, cases []int64, cutoff time.Time) (map[string]string, error) {
	rows, err := tx.Query(ctx, `WITH RECURSIVE chain AS (
    SELECT id AS original,id,merged_into,merged_at FROM incidents WHERE workspace_id=$1 AND id=ANY($2::bigint[])
    UNION ALL
    SELECT c.original,i.id,i.merged_into,i.merged_at FROM chain c JOIN incidents i ON i.id=c.merged_into
    WHERE c.merged_at < $3 AND i.workspace_id=$1
) CYCLE id SET cycled USING path
SELECT original::text,id::text FROM chain WHERE NOT cycled AND (merged_into IS NULL OR merged_at >= $3)`,
		workspace, cases, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roots := make(map[string]string, len(cases))
	for rows.Next() {
		var id, root string
		if err := rows.Scan(&id, &root); err != nil {
			return nil, err
		}
		roots[id] = root
	}
	return roots, rows.Err()
}

func zeroStateCounts(keys ...string) map[string]int64 {
	counts := make(map[string]int64, len(keys))
	for _, key := range keys {
		counts[key] = 0
	}
	return counts
}

func summarizeStateFacts(facts map[[2]string]dailyFact) *extract.Totals {
	t := &extract.Totals{
		Detections: zeroStateCounts("records", "deduplicated"),
		Alerts: zeroStateCounts("routed", "fire_seen", "no_fire_found", "awaiting_verification", "awaiting_at_cutoff",
			"false_alarm", "duplicate", "non_forest", "controlled_burn", "not_reachable", "unsafe", "outside_jurisdiction"),
		Incidents:  zeroStateCounts("new", "active", "controlled", "extinguished", "closed", "closure_backlog"),
		Operations: zeroStateCounts("assistance_requested", "assistance_received", "overdue_cases", "overdue_alerts"),
		Response:   map[string]extract.Response{},
	}
	for _, f := range facts {
		_, metric, _ := strings.Cut(f.Metric, ".")
		switch f.Kind {
		case "detection":
			t.Detections["records"]++
			t.Detections[metric]++
		case "deduplicated":
			t.Detections["deduplicated"]++
		case "alert":
			t.Alerts["routed"]++
			t.Alerts[metric]++
		case "awaiting":
			t.Alerts["awaiting_at_cutoff"]++
		case "confirmed_new", "confirmed_active", "controlled", "extinguished", "closed", "closure_backlog":
			t.Incidents[metric]++
		case "requested":
			t.Operations["assistance_requested"] += int64(f.Amount)
		case "received":
			t.Operations["assistance_received"] += int64(f.Amount)
		case "overdue":
			t.Operations["overdue_cases"]++
		case "overdue_alert":
			t.Operations["overdue_alerts"]++
		case "response.time", "response.count":
			stage, measure, _ := strings.Cut(metric, ".")
			r := t.Response[stage]
			if f.Kind == "response.time" {
				r.TotalSeconds += f.Amount
			} else {
				r.Denominator++
				if measure == "unmeasured" {
					r.Unknown++
				} else {
					r.Measured++
				}
			}
			t.Response[stage] = r
		}
	}
	return t
}

func loadStateArea(ctx context.Context, tx pgx.Tx, workspace, state string, cases []int64, roots map[string]string, window extract.Window) (extract.Area, error) {
	unique := map[int64]bool{}
	for _, id := range cases {
		if root, err := strconv.ParseInt(roots[strconv.FormatInt(id, 10)], 10, 64); err == nil {
			id = root
		}
		unique[id] = true
	}
	cases = cases[:0]
	for id := range unique {
		cases = append(cases, id)
	}
	area := extract.Area{Expected: len(cases), Breakdowns: map[string]float64{}, Impacts: map[string]int{}}
	var assessments []struct {
		Hectares                  float64
		LandClass, Method, Status string
		Impacts                   map[string]bool
	}
	if err := tx.QueryRow(ctx, stateExtractAreaSQL, workspace, cases, state, window.StartsAt, window.EndsAt).Scan(&assessments); err != nil {
		return area, err
	}
	var sum float64
	for _, a := range assessments {
		area.Breakdowns[a.LandClass+"."+a.Method+"."+a.Status] += a.Hectares
		sum += a.Hectares
		for category, present := range a.Impacts {
			if present {
				area.Impacts[category]++
			}
		}
	}
	area.Reported = len(assessments)
	if area.Reported > 0 {
		area.Hectares = &sum
	}
	area.Status = extract.Status(area.Expected, area.Reported, area.Hectares)
	return area, nil
}

func loadStatePreparedness(ctx context.Context, tx pgx.Tx, workspace string, window extract.Window) (*extract.Preparedness, error) {
	var p extract.Preparedness
	err := tx.QueryRow(ctx, stateExtractPreparednessSQL, workspace, window.StartsAt, window.EndsAt).Scan(
		&p.ApprovedRecords, &p.WorkRecords, &p.CompletedWork, &p.TrainingRecords, &p.ReadinessChecks,
		&p.Trained.Value, &p.Trained.Reported, &p.Target.Value, &p.Target.Reported, &p.WorkDone.Value, &p.WorkDone.Reported,
		&p.ExpenditureINR.Value, &p.ExpenditureINR.Reported)
	if err != nil {
		return nil, err
	}
	p.Status = extract.Reported
	if p.ApprovedRecords == 0 {
		p.Status = extract.NotReported
	}
	p.Trained.Expected = p.TrainingRecords
	p.Target.Expected, p.WorkDone.Expected, p.ExpenditureINR.Expected = p.WorkRecords, p.WorkRecords, p.WorkRecords
	for _, q := range []*extract.Quantity{&p.Trained, &p.Target, &p.WorkDone, &p.ExpenditureINR} {
		q.Status = extract.Status(q.Expected, q.Reported, q.Value)
		if q.Reported < q.Expected {
			p.Status = extract.Incomplete
		}
	}
	return &p, nil
}
