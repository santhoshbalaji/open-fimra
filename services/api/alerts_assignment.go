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
HAVING EXISTS (SELECT FROM officer_grants)
`

const assignmentWrite = `
, audited AS (
    INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
    SELECT a.workspace_id, 'user', $1, 'alert.assign', 'alert', a.id::text,
           jsonb_build_object('previous', jsonb_build_object('queue', a.queue, 'hierarchyId', a.hierarchy_id,
               'unitPath', a.unit_path, 'unitPaths', a.unit_paths, 'hierarchyUnitPaths', a.hierarchy_unit_paths),
               'unitId', c.id::text, 'unitPath', c.path::text, 'hierarchyId', c.hierarchy_id, 'reason', $5::text)
    FROM alerts a CROSS JOIN choices c WHERE a.id = $3
    RETURNING id, occurred_at
)
UPDATE alerts a SET
    queue = 'unit', hierarchy_id = c.hierarchy_id, unit_path = c.path, unit_paths = ARRAY[c.path],
    hierarchy_unit_paths = jsonb_build_object(c.hierarchy_id::text, jsonb_build_array(c.path::text)),
    assignment = jsonb_build_object('unitId', c.id::text, 'unitName', c.name, 'reason', $5,
        'actor', c.display_name, 'at', log.occurred_at, 'auditId', log.id::text)
FROM choices c CROSS JOIN audited log WHERE a.id = $3
RETURNING assignment - 'auditId'
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
	var result json.RawMessage
	if r.Method == http.MethodGet {
		err = a.reader.QueryRow(ctx, assignmentChoicesSQL+assignmentOptions, subject, slug, id, nil).Scan(&result)
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
			return assignAlert(ctx, tx, subject, slug, id, unit, input.Reason, &result)
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

func assignAlert(ctx context.Context, tx pgx.Tx, subject, slug string, id, unit int64, reason string, result *json.RawMessage) error {
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
	var officer, same bool
	err = tx.QueryRow(ctx, `
SELECT a.queue, EXISTS (SELECT FROM alert_roles($1, $2, $3) WHERE officer),
    COALESCE(a.assignment->>'unitId' = $4::bigint::text AND a.assignment->>'reason' = $5::text
        AND log.actor_id = $1, false), a.assignment - 'auditId'
FROM alerts a LEFT JOIN audit_log log ON log.id = (a.assignment->>'auditId')::bigint
WHERE a.id = $3 AND EXISTS (SELECT FROM alert_roles($1, $2, $3))`,
		subject, slug, id, unit, reason).Scan(&queue, &officer, &same, result)
	if err != nil {
		return err
	}
	if !officer {
		return &stepError{http.StatusForbidden, "An officer in this area must assign the alert."}
	}
	if same {
		return nil
	}
	if queue == "unit" {
		return &stepError{http.StatusConflict, "This alert is already in a unit queue."}
	}
	err = tx.QueryRow(ctx, assignmentChoicesSQL+assignmentWrite, subject, slug, id, unit, reason).Scan(result)
	if errors.Is(err, pgx.ErrNoRows) {
		return &stepError{http.StatusUnprocessableEntity, "Choose a current unit in your assigned area."}
	}
	return err
}
