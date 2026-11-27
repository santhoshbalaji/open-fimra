package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

const lsasafWatchSQL = `
WITH grants AS MATERIALIZED (
    SELECT * FROM alert_grants WHERE oidc_subject=$1 AND slug=$2
),
visible AS (
    SELECT c.id, p.slot AS last_slot, c.alert_id, p.footprint,
           d.frp_mw, d.confidence, p.candidates
    FROM lsasaf_cases c
    CROSS JOIN LATERAL (
        SELECT p.detection_id,p.slot,p.footprint,
               m.boundary_snapshot->'candidateBeats' AS candidates
        FROM lsasaf_pixels p JOIN detection_matches m
          ON (m.workspace_id,m.detection_id,m.observed_at) = (p.workspace_id,p.detection_id,p.slot)
        WHERE p.case_id = c.id AND EXISTS (
            SELECT FROM grants g
            WHERE g.workspace_id=c.workspace_id AND g.can_review AND
                match_under_grant(m.hierarchy_unit_paths,g.hierarchy_id,g.unit_path,g.depth)
        )
        ORDER BY p.slot DESC LIMIT 1
    ) p
    JOIN detections d ON (d.id,d.observed_at) = (p.detection_id,p.slot)
    WHERE c.confirmed_at IS NULL AND c.last_slot BETWEEN $4::timestamptz - make_interval(hours => $3) AND $4
    ORDER BY c.last_slot DESC,c.id DESC LIMIT 200
)
SELECT EXISTS (SELECT FROM grants),
       jsonb_build_object('type','FeatureCollection','attribution', jsonb_build_object(
           'provider','EUMETSAT LSA SAF','product','MSG-IODC FRP-PIXEL','status','Demonstration',
           'licence','https://creativecommons.org/licenses/by/4.0/','citation','https://doi.org/10.5194/acp-15-13217-2015'
       ),'features',COALESCE((
           SELECT jsonb_agg(jsonb_build_object(
               'type','Feature','id',id::text,'geometry',ST_AsGeoJSON(footprint)::jsonb,
               'properties',jsonb_build_object('id',id::text,'observedAt',last_slot,
                  'alertId',alert_id::text,'frpMW',frp_mw,'confidence',confidence,'candidateBeats',candidates))
               ORDER BY last_slot DESC,id DESC)
           FROM visible),'[]'::jsonb));
`

func (a api) lsasafWatches(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	hours := 24
	if raw := r.URL.Query().Get("hours"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 720 {
			writeError(w, http.StatusBadRequest, "hours must be an integer from 1 to 720.")
			return
		}
		hours = parsed
	}
	subject := r.Context().Value(identityKey{}).(identity).Subject
	var allowed bool
	var data json.RawMessage
	if err := a.pool.QueryRow(r.Context(), lsasafWatchSQL, subject, chi.URLParam(r, "slug"), hours, time.Now()).Scan(&allowed, &data); err != nil {
		a.logger.Error("Could not read Meteosat watches", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not read Meteosat watches.")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "No role in this workspace.")
		return
	}
	writeJSON(w, http.StatusOK, data)
}
