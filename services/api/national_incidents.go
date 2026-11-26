package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type nationalIncidentInput struct {
	SharedEventID      string    `json:"sharedEventId"`
	FSIID              *string   `json:"fsiId"`
	Status             string    `json:"status"`
	Severity           string    `json:"severity"`
	OccurredAt         time.Time `json:"occurredAt"`
	NextUpdateAt       time.Time `json:"nextUpdateAt"`
	PrecisionDecimals  *int      `json:"precisionDecimals"`
	Longitude          *float64  `json:"longitude"`
	Latitude           *float64  `json:"latitude"`
	Threats            string    `json:"threats"`
	AidRequested       string    `json:"aidRequested"`
	AidAccepted        string    `json:"aidAccepted"`
	ProvisionalImpacts string    `json:"provisionalImpacts"`
	Shared             bool      `json:"shared"`
}

func nationalText(text string, limit int) bool {
	return strings.TrimSpace(text) != "" && len([]rune(text)) <= limit && !strings.ContainsRune(text, 0)
}

func (i nationalIncidentInput) valid() bool {
	switch i.Status {
	case "active", "controlled", "extinguished", "closed":
	default:
		return false
	}
	switch i.Severity {
	case "major", "severe", "critical":
	default:
		return false
	}
	if !stateExtractID.MatchString(i.SharedEventID) || (i.FSIID != nil && !stateUnitCode.MatchString(*i.FSIID)) ||
		i.OccurredAt.IsZero() || (i.NextUpdateAt.IsZero() && i.Status != "closed") ||
		(!i.NextUpdateAt.IsZero() && !i.NextUpdateAt.After(i.OccurredAt)) ||
		(i.PrecisionDecimals != nil && (*i.PrecisionDecimals < 0 || *i.PrecisionDecimals > 1)) ||
		(i.Longitude == nil) != (i.Latitude == nil) ||
		(i.Longitude != nil && (*i.Longitude < -180 || *i.Longitude > 180 || *i.Latitude < -90 || *i.Latitude > 90)) {
		return false
	}
	for _, field := range []string{i.Threats, i.AidRequested, i.AidAccepted, i.ProvisionalImpacts} {
		if len([]rune(field)) > 500 || strings.ContainsRune(field, 0) {
			return false
		}
	}
	return true
}

func (a api) nationalIncidentPut(w http.ResponseWriter, r *http.Request) {
	var input nationalIncidentInput
	ref, code := chi.URLParam(r, "incident"), chi.URLParam(r, "state")
	valid := stateUnitCode.MatchString(ref) && stateExtractCode.MatchString(code) && decodeDaily(w, r, &input) && input.valid()
	a.nationalOperation(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		caller := nationalCaller(r)
		workspace, err := nationalWorkspaceID(ctx, tx, caller.slug)
		if err != nil {
			return 0, nil, err
		}
		var writeErr error
		if valid {
			// Keep the access audit even if the update conflicts with a saved record.
			writeErr = pgx.BeginFunc(ctx, tx, func(savepoint pgx.Tx) error {
				state, _, err := nationalStateForRole(ctx, savepoint, caller, code, "national_nodal")
				if err != nil {
					return err
				}
				return storeNationalIncident(ctx, savepoint, state, ref, input)
			})
		} else {
			writeErr = dailyError(400, "Check the major incident update.")
		}

		if writeErr != nil {
			status, message := nationalProblem(writeErr)
			err = nationalAudit(ctx, tx, workspace, caller.subject, "incident_update_refused", "national_channel", "", map[string]int{"status": status})
			return status, map[string]string{"error": message}, err
		}
		err = nationalAudit(ctx, tx, workspace, caller.subject, "incident_update", "national_incident", ref,
			map[string]any{"state": code, "shared": input.Shared})
		return 200, map[string]string{"incidentRef": ref}, err
	})
}

func storeNationalIncident(ctx context.Context, tx pgx.Tx, state int64, ref string, input nationalIncidentInput) error {
	var next *time.Time
	if !input.NextUpdateAt.IsZero() {
		next = &input.NextUpdateAt
	}
	tag, err := tx.Exec(ctx, `WITH agreed AS (
    SELECT least(location_decimals,coalesce($9::smallint,location_decimals)) AS decimals
    FROM national_states WHERE id=$1
)
INSERT INTO national_incident_updates(state_id,incident_ref,shared_event_id,fsi_id,status,severity,occurred_at,next_update_at,
    precision_decimals,longitude,latitude,threats,aid_requested,aid_accepted,provisional_impacts,shared)
SELECT $1,$2,$3,$4,$5,$6,$7,$8,decimals,round($10::numeric,decimals),round($11::numeric,decimals),$12,$13,$14,$15,$16 FROM agreed
ON CONFLICT(state_id,incident_ref) DO UPDATE SET
    fsi_id=excluded.fsi_id,status=excluded.status,severity=excluded.severity,occurred_at=excluded.occurred_at,
    next_update_at=excluded.next_update_at,precision_decimals=excluded.precision_decimals,longitude=excluded.longitude,
    latitude=excluded.latitude,threats=excluded.threats,aid_requested=excluded.aid_requested,aid_accepted=excluded.aid_accepted,
    provisional_impacts=excluded.provisional_impacts,shared=excluded.shared
WHERE national_incident_updates.shared_event_id=excluded.shared_event_id
AND national_incident_updates.occurred_at<excluded.occurred_at`,
		state, ref, input.SharedEventID, input.FSIID, input.Status, input.Severity, input.OccurredAt, next,
		input.PrecisionDecimals, input.Longitude, input.Latitude, input.Threats, input.AidRequested, input.AidAccepted,
		input.ProvisionalImpacts, input.Shared)
	if err == nil && tag.RowsAffected() == 0 {
		err = dailyError(409, "Use the same event reference and a later update time.")
	}
	return err
}
