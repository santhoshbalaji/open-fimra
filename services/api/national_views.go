package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func (a api) nationalPortal(w http.ResponseWriter, r *http.Request) {
	caller := nationalCaller(r)
	a.nationalOperation(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		return nationalJSON(ctx, tx, `
SELECT jsonb_build_object(
    'roles',jsonb_agg(DISTINCT g.role_slug ORDER BY g.role_slug),
    'submittingStates',coalesce(jsonb_agg(s.state_code ORDER BY s.state_code)
        FILTER (WHERE g.role_slug='national_nodal'),'[]'))
FROM national_state_grants g JOIN national_states s ON s.id=g.state_id
WHERE g.slug=$1 AND g.oidc_subject=$2 HAVING count(*)>0`, caller.slug, caller.subject)
	})
}

const nationalWindowRows = `
WITH grants AS MATERIALIZED (
    SELECT state_id FROM national_state_grants WHERE slug=$1 AND oidc_subject=$2 AND role_slug=$3
), windows AS (
    SELECT w.*, s.state_code, u.name,
        jsonb_build_object('state',s.state_code,'geography',u.name,'from',lower(w.days),'to',upper(w.days)-1) AS context
    FROM grants g JOIN national_states s ON s.id=g.state_id
    JOIN units u ON (u.hierarchy_id,u.current_path)=(s.hierarchy_id,s.unit_path)
    JOIN national_windows w ON w.state_id=s.id
)
SELECT coalesce(jsonb_agg(item ORDER BY upper(days) DESC,state_code),'[]') FROM (`

const nationalProvenance = `jsonb_build_object(
    'source',x.payload->>'source','window',x.payload->'window',
    'generatedAt',x.payload->'generatedAt','revision',x.revision,
    'coverage', (x.payload->'coverage') - 'units'
)`

const nationalWindowJoins = `
FROM windows w LEFT JOIN national_current_extracts x ON x.window_id=w.id AND x.kind='incident'`

func (a api) nationalDashboard(w http.ResponseWriter, r *http.Request) {
	caller := nationalCaller(r)
	view := chi.URLParam(r, "view")
	var role, query string
	switch view {
	case "ministry":
		role = "national_ministry"
		query = `SELECT w.days,w.state_code,w.context || jsonb_build_object(
    'stateReturnShared',w.ministry_shared, 'preparednessShared',w.preparedness_shared,
    'stateReturn',CASE WHEN w.ministry_shared AND x.id IS NOT NULL THEN ` + nationalProvenance + ` || jsonb_build_object('totals',x.payload->'totals') END,
    'preparedness',CASE WHEN w.preparedness_shared AND p.id IS NOT NULL THEN jsonb_build_object(
        'source', p.payload->>'source','revision',p.revision,'window',p.payload->'window',
        'generatedAt',p.payload->'generatedAt','figures',p.payload->'preparedness') END
) AS item ` + nationalWindowJoins + `
LEFT JOIN national_current_extracts p ON p.window_id=w.id AND p.kind='preparedness'
WHERE w.ministry_shared OR w.preparedness_shared`
	case "survey":
		role = "national_survey"
		query = `SELECT w.days,w.state_code,w.context || jsonb_build_object(
    'stateReturnShared',w.survey_shared, 'feedbackShared',w.feedback_shared,
    'stateReturn',CASE WHEN w.survey_shared AND x.id IS NOT NULL THEN ` + nationalProvenance + ` || jsonb_build_object('detections',x.payload #> '{totals,detections}') END,
    'satellite',CASE WHEN w.survey_shared AND sat.window_id IS NOT NULL THEN jsonb_build_object(
        'source',sat.source,'product',sat.product,'detections',sat.detections,'expected',sat.expected,'reported',sat.reported,
        'startsAt',sat.starts_at,'endsAt',sat.ends_at,
        'samePeriod',sat.starts_at=(x.payload #>> '{window,startsAt}')::timestamptz
                 AND sat.ends_at=(x.payload #>> '{window,endsAt}')::timestamptz) END,
    'feedback',CASE WHEN w.feedback_shared AND f.window_id IS NOT NULL THEN jsonb_build_object(
        'prepared',f.prepared,'submitted',f.submitted,'accepted',f.accepted,'rejected',f.rejected) END
) AS item ` + nationalWindowJoins + `
LEFT JOIN national_satellite_measures sat ON sat.window_id=w.id
LEFT JOIN national_feedback f ON f.window_id=w.id
WHERE w.survey_shared OR w.feedback_shared`
	case "submissions":
		role = "national_nodal"
		query = `SELECT w.days,w.state_code,w.context || jsonb_build_object(
    'ministry',w.ministry_shared,'survey',w.survey_shared,
    'preparednessShared',w.preparedness_shared,'feedbackShared',w.feedback_shared,
    'public',coalesce(p.extract_id=x.id,false),
    'currentIncidentId',x.id,
    'receipts',(SELECT coalesce(jsonb_agg(receipt ORDER BY revision DESC,kind),'[]')
                FROM national_extracts WHERE window_id=w.id)
) AS item ` + nationalWindowJoins + `
LEFT JOIN national_publications p ON p.window_id=w.id`
	case "public":
		role = "national_public"
		query = `SELECT w.days,w.state_code,p.aggregate AS item FROM windows w JOIN national_public_aggregates p ON p.window_id=w.id`
	default:
		writeError(w, 404, "This national view was not found.")
		return
	}
	a.nationalOperation(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		return nationalJSON(ctx, tx, nationalWindowRows+query+`) listed
HAVING EXISTS (SELECT FROM grants)`, caller.slug, caller.subject, role)
	})
}

type nationalSatelliteInput struct {
	nationalWindow
	Source     string    `json:"source"`
	Product    string    `json:"product"`
	StartsAt   time.Time `json:"startsAt"`
	EndsAt     time.Time `json:"endsAt"`
	Expected   int32     `json:"expected"`
	Reported   int32     `json:"reported"`
	Detections *int64    `json:"detections"`
}

func (a api) nationalSatellite(w http.ResponseWriter, r *http.Request) {
	var input nationalSatelliteInput
	if !decodeDaily(w, r, &input) || !input.valid() || !nationalText(input.Source, 120) || !nationalText(input.Product, 120) ||
		input.StartsAt.IsZero() || !input.EndsAt.After(input.StartsAt) || input.Expected <= 0 || input.Reported < 0 ||
		input.Reported > input.Expected || (input.Reported == 0) != (input.Detections == nil) ||
		(input.Detections != nil && (*input.Detections < 0 || *input.Detections > 9007199254740991)) {
		writeError(w, 400, "Check the satellite source, period and coverage.")
		return
	}
	a.nationalOperation(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		caller := nationalCaller(r)
		state, workspace, err := nationalStateForRole(ctx, tx, caller, chi.URLParam(r, "state"), "national_survey")
		if err != nil {
			return 0, nil, err
		}
		id, err := nationalWindowID(ctx, tx, state, input.nationalWindow, false)
		if err != nil {
			return 0, nil, err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO national_satellite_measures(window_id,source,product,starts_at,ends_at,expected,reported,detections)
SELECT id,$2,$3,$4,$5,$6,$7,$8 FROM national_windows WHERE id=$1 AND survey_shared
ON CONFLICT(window_id) DO UPDATE SET source=excluded.source,product=excluded.product,
starts_at=excluded.starts_at,ends_at=excluded.ends_at,expected=excluded.expected,reported=excluded.reported,detections=excluded.detections`,
			id, input.Source, input.Product, input.StartsAt, input.EndsAt, input.Expected, input.Reported, input.Detections)
		if err == nil && tag.RowsAffected() == 0 {
			err = errNationalScope
		}
		if err == nil {
			err = nationalAudit(ctx, tx, workspace, caller.subject, "satellite", "national_window", chi.URLParam(r, "state"), input.nationalWindow)
		}
		return 200, input, err
	})
}

func (a api) nationalFeedback(w http.ResponseWriter, r *http.Request) {
	var input struct {
		nationalWindow
		Prepared  int32 `json:"prepared"`
		Submitted int32 `json:"submitted"`
		Accepted  int32 `json:"accepted"`
		Rejected  int32 `json:"rejected"`
	}
	if !decodeDaily(w, r, &input) || !input.valid() || input.Prepared < 0 || input.Submitted < 0 || input.Accepted < 0 || input.Rejected < 0 {
		writeError(w, 400, "Check the feedback counts and reporting window.")
		return
	}
	a.nationalOperation(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		caller := nationalCaller(r)
		state, workspace, err := nationalStateForRole(ctx, tx, caller, chi.URLParam(r, "state"), "national_nodal")
		if err != nil {
			return 0, nil, err
		}
		id, err := nationalWindowID(ctx, tx, state, input.nationalWindow, false)
		if err != nil {
			return 0, nil, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO national_feedback(window_id,prepared,submitted,accepted,rejected) VALUES($1,$2,$3,$4,$5)
ON CONFLICT(window_id) DO UPDATE SET prepared=excluded.prepared,submitted=excluded.submitted,accepted=excluded.accepted,rejected=excluded.rejected`,
			id, input.Prepared, input.Submitted, input.Accepted, input.Rejected)
		if err == nil {
			err = nationalAudit(ctx, tx, workspace, caller.subject, "feedback", "national_window", chi.URLParam(r, "state"), input.nationalWindow)
		}
		return 200, input, err
	})
}

func (a api) nationalIncidents(w http.ResponseWriter, r *http.Request) {
	caller := nationalCaller(r)
	a.nationalOperation(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		workspace, err := nationalWorkspaceID(ctx, tx, caller.slug)
		if err != nil {
			return 0, nil, err
		}
		var data json.RawMessage
		var allowed bool
		err = tx.QueryRow(ctx, `
WITH grants AS MATERIALIZED (
    SELECT state_id,role_slug FROM national_state_grants
    WHERE workspace_id=$1 AND oidc_subject=$2 AND role_slug IN ('national_disaster','national_nodal')
), visible AS MATERIALIZED (
    SELECT i.*,s.state_code,u.name,least(i.precision_decimals,s.location_decimals) AS display_decimals FROM national_incident_updates i JOIN national_states s ON i.state_id=s.id
    JOIN units u ON (u.hierarchy_id,u.current_path)=(s.hierarchy_id,s.unit_path)
    WHERE EXISTS (SELECT FROM grants g WHERE g.state_id=i.state_id AND (g.role_slug='national_nodal' OR i.shared))
), audit AS (
    INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,details)
    SELECT $1,'user',$2,'national.incident_read','national_incident',v.incident_ref,jsonb_build_object('state',v.state_code)
    FROM visible v
)
SELECT EXISTS(SELECT FROM grants),jsonb_build_object(
    'eventCount',count(DISTINCT shared_event_id),
    'updates',coalesce(jsonb_agg(jsonb_build_object(
        'state',state_code,'geography',name,'incidentRef',incident_ref,'sharedEventId',shared_event_id,
        'fsiId',fsi_id,'status',status,'severity',severity,'occurredAt',occurred_at,'nextUpdateAt',next_update_at,
        'precisionDecimals',display_decimals,'longitude',round(longitude,display_decimals),'latitude',round(latitude,display_decimals),'threats',threats,
        'aidRequested',aid_requested,'aidAccepted',aid_accepted,'provisionalImpacts',provisional_impacts,'shared',shared
    ) ORDER BY occurred_at DESC,state_code,incident_ref),'[]')
) FROM visible`, workspace, caller.subject).Scan(&allowed, &data)
		if err == nil {
			err = nationalAudit(ctx, tx, workspace, caller.subject, "incident_list", "national_channel", "", map[string]bool{"allowed": allowed})
		}
		if !allowed {
			// Commit the audit event for a denied request as well.
			return 403, map[string]string{"error": "A national role for this record is required."}, err
		}
		return 200, data, err
	})
}
