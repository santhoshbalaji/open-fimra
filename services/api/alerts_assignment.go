package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

//go:embed alerts_assignment.sql
var assignmentChoicesSQL string

const assignmentOptions = `
SELECT COALESCE(jsonb_agg(jsonb_build_object('id', id::text, 'name', name, 'level', level,
    'parent', parent_name, 'suggested', matched AND depth = (SELECT max(depth) FROM choices WHERE matched))
    ORDER BY matched DESC, CASE WHEN matched THEN depth END DESC, name, hierarchy, path), '[]')
FROM choices
WHERE $5::text IS NULL
HAVING EXISTS (SELECT FROM officer_grants)
`

const assignmentWrite = `
, audited AS (
    INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
    SELECT a.workspace_id, 'user', $1, CASE WHEN a.queue = 'unit' THEN 'alert.transfer' ELSE 'alert.assign' END,
           'alert', a.id::text,
           jsonb_build_object('previous', jsonb_build_object('queue', a.queue, 'hierarchyId', a.hierarchy_id,
               'unitPath', a.unit_path, 'unitPaths', a.unit_paths, 'hierarchyUnitPaths', a.hierarchy_unit_paths,
               'lead', a.dispatch_lead, 'assignment', a.assignment),
               'unitId', c.id::text, 'unitPath', c.path::text, 'hierarchyId', c.hierarchy_id, 'reason', $5::text)
    FROM alerts a CROSS JOIN choices c WHERE a.id = $3
    RETURNING id, occurred_at
), updated AS (
    SELECT c.*, a.queue AS previous_queue, support.units,
           dispatch_paths(c.unit, support.units, a.original_dispatch->'candidatePaths') AS paths
    FROM choices c JOIN alerts a ON a.id = $3
    CROSS JOIN LATERAL (
        SELECT COALESCE(jsonb_agg(value ORDER BY ordinal), '[]') AS units
        FROM jsonb_array_elements(a.dispatch_support) WITH ORDINALITY s(value, ordinal)
        WHERE (value->>'hierarchyId', value->>'path') <> (c.hierarchy_id::text, c.path::text)
    ) support
)
UPDATE alerts a SET
    queue = 'unit', hierarchy_id = c.hierarchy_id, unit_path = c.path,
    unit_paths = dispatch_flat_paths(c.paths), hierarchy_unit_paths = c.paths,
    dispatch_lead = c.unit, dispatch_support = c.units,
    assignment = jsonb_build_object('unitId', c.id::text, 'unitName', c.name, 'reason', $5,
        'actor', c.display_name, 'at', log.occurred_at, 'auditId', log.id::text,
        'action', CASE WHEN c.previous_queue = 'unit' THEN 'transfer' ELSE 'assign' END)
FROM updated c CROSS JOIN audited log WHERE a.id = $3
RETURNING assignment - 'auditId'
`

const supportWrite = `
INSERT INTO alert_support_requests (alert_id, target, reason, requested_by)
SELECT $3, unit, $5, user_id FROM choices
ON CONFLICT DO NOTHING
RETURNING id, target, requested_at
`

type alertAssignmentRequest struct {
	UnitID string `json:"unitId"`
	Reason string `json:"reason"`
}

func (a api) alertAssignment(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "Invalid alert ID.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	support := strings.HasSuffix(r.URL.Path, "/support")
	var result json.RawMessage
	if r.Method == http.MethodGet {
		err = a.reader.QueryRow(ctx, assignmentChoicesSQL+assignmentOptions, subject, slug, id, nil, nil, support).Scan(&result)
	} else {
		var input alertAssignmentRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			writeError(w, 400, "Provide a unit and a reason for assigning this alert.")
			return
		}
		input.Reason = strings.TrimSpace(input.Reason)
		unit, parseErr := strconv.ParseInt(input.UnitID, 10, 64)
		if parseErr != nil || unit < 1 || input.Reason == "" || utf8.RuneCountInString(input.Reason) > 500 || strings.ContainsRune(input.Reason, 0) {
			writeError(w, 400, "Choose a unit and enter a reason of up to 500 characters.")
			return
		}
		err = pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
			return changeAlertDispatch(ctx, tx, subject, slug, id, unit, input.Reason, support, &result)
		})
	}
	var problem *stepError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, 404, "Alert not found.")
	case errors.As(err, &problem):
		writeError(w, problem.status, problem.message)
	case err != nil:
		a.logger.Error("Could not process alert assignment", "error", err)
		writeError(w, 500, "Could not load or save this assignment.")
	default:
		writeJSON(w, 200, result)
	}
}

func changeAlertDispatch(ctx context.Context, tx pgx.Tx, subject, slug string, id, unit int64, reason string, support bool, result *json.RawMessage) error {
	// Serialize assignments and workflow moves. Check access again after obtaining the lock.
	var locked int64
	err := tx.QueryRow(ctx, `
SELECT i.alert_id FROM workflow_instances i
WHERE i.alert_id = $3 AND EXISTS (SELECT FROM alert_roles($1, $2, $3))
FOR UPDATE OF i`, subject, slug, id).Scan(&locked)
	if err != nil {
		return err
	}
	var queue string
	var officer, same, sameUnit bool
	err = tx.QueryRow(ctx, `
SELECT a.queue, EXISTS (SELECT FROM alert_roles($1, $2, $3) WHERE officer),
    COALESCE((a.dispatch_lead->>'id')::bigint = $4, false),
    COALESCE(a.assignment->>'unitId' = $4::bigint::text AND a.assignment->>'reason' = $5::text
        AND log.actor_id = $1, false), a.assignment - 'auditId'
FROM alerts a LEFT JOIN audit_log log ON log.id = (a.assignment->>'auditId')::bigint
WHERE a.id = $3 AND EXISTS (SELECT FROM alert_roles($1, $2, $3))`,
		subject, slug, id, unit, reason).Scan(&queue, &officer, &sameUnit, &same, result)
	if err != nil {
		return err
	}
	if !officer {
		return &stepError{http.StatusForbidden, "An officer in this area must assign the alert."}
	}
	if !support && same {
		return nil
	}
	if support && queue != "unit" {
		return &stepError{http.StatusConflict, "Assign the alert before requesting support."}
	}
	if sameUnit {
		return &stepError{http.StatusConflict, "This unit already leads the alert."}
	}
	if support {
		err = requestAlertSupport(ctx, tx, subject, slug, id, unit, reason, result)
	} else {
		err = tx.QueryRow(ctx, assignmentChoicesSQL+assignmentWrite, subject, slug, id, unit, reason, false).Scan(result)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return &stepError{http.StatusUnprocessableEntity, "Choose a unit you can route this alert to."}
	}
	if err != nil {
		return err
	}
	// Reuse the configured delivery rules. Existing recipients and repeated
	// notifications for the incident are deduplicated by the same outbox.
	_, err = tx.Exec(ctx, `SELECT enqueue_alert_notifications(a, 'alert_new', a.id) FROM alerts a WHERE a.id = $1`, id)
	return err
}

func requestAlertSupport(ctx context.Context, tx pgx.Tx, subject, slug string, id, unit int64, reason string, result *json.RawMessage) error {
	var requestID int64
	var target json.RawMessage
	var at time.Time
	err := tx.QueryRow(ctx, assignmentChoicesSQL+supportWrite, subject, slug, id, unit, reason, true).
		Scan(&requestID, &target, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		// A retry must still be authorized and must refer to the same request.
		return tx.QueryRow(ctx, assignmentChoicesSQL+`
SELECT jsonb_build_object('id', r.id::text, 'unit', r.target, 'reason', r.reason, 'at', r.requested_at)
FROM choices c JOIN alert_support_requests r ON r.alert_id = $3
  AND (r.target->>'hierarchyId', r.target->>'path') = (c.hierarchy_id::text, c.path::text)
WHERE r.requested_by = c.user_id AND r.reason = $5`, subject, slug, id, unit, reason, true).Scan(result)
	}
	if err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `
WITH updated AS (
    SELECT a.id, a.workspace_id, dispatch_paths(NULL, jsonb_build_array($2::jsonb), a.hierarchy_unit_paths) AS added,
           a.hierarchy_unit_paths AS existing
    FROM alerts a WHERE a.id = $1
), merged AS (
    SELECT jsonb_object_agg(hierarchy, paths) AS paths
    FROM (
        SELECT hierarchy, jsonb_agg(DISTINCT p.path ORDER BY p.path) AS paths
        FROM updated CROSS JOIN LATERAL (
            SELECT key, value FROM jsonb_each(existing)
            UNION ALL SELECT key, value FROM jsonb_each(added)
        ) h(hierarchy, paths)
        CROSS JOIN LATERAL jsonb_array_elements_text(h.paths) p(path)
        GROUP BY hierarchy
    ) scopes
), audited AS (
    INSERT INTO audit_log(workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
    SELECT workspace_id, 'user', $3, 'alert.support', 'alert', id::text,
           jsonb_build_object('requestId', $4::bigint::text, 'unit', $2::jsonb, 'reason', $5::text)
    FROM updated
)
UPDATE alerts a SET hierarchy_unit_paths = m.paths, unit_paths = dispatch_flat_paths(m.paths),
    dispatch_support = CASE WHEN EXISTS (
        SELECT FROM jsonb_array_elements(a.dispatch_support) s
        WHERE (s->>'hierarchyId', s->>'path') = ($2::jsonb->>'hierarchyId', $2::jsonb->>'path')
    ) THEN a.dispatch_support ELSE a.dispatch_support || jsonb_build_array($2::jsonb) END
FROM merged m WHERE a.id = $1
RETURNING jsonb_build_object('id', $4::bigint::text, 'unit', $2::jsonb, 'reason', $5::text, 'at', $6::timestamptz)
`, id, target, subject, requestID, reason, at).Scan(result)
	return err
}
