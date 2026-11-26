package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type dutyShiftInput struct {
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
	Members  []string  `json:"memberIds"`
	Units    []string  `json:"unitIds"`
}

type dispatchRuleInput struct {
	Name          string     `json:"name"`
	Priority      int32      `json:"priority"`
	EffectiveFrom time.Time  `json:"effectiveFrom"`
	EffectiveTo   *time.Time `json:"effectiveTo"`
	OverlayLayer  string     `json:"overlayLayer"`
	OverlayCode   string     `json:"overlayCode"`
	OverlayZone   string     `json:"overlayZone"`
	Lead          string     `json:"leadUnitId"`
	Supporting    []string   `json:"supportingUnitIds"`
}

const dutyRosterRead = `
SELECT jsonb_build_object(
    'members', (SELECT COALESCE(jsonb_agg(jsonb_build_object('id', u.id, 'name', u.display_name,
        'username', u.username) ORDER BY u.display_name, u.username), '[]')
        FROM users u WHERE EXISTS (
            SELECT FROM role_assignments a WHERE a.workspace_id = $1 AND a.user_id = u.id)),
    'units', (SELECT COALESCE(jsonb_agg(jsonb_build_object('id', u.id::text, 'name', u.name, 'path', u.path::text,
        'hierarchy', h.slug) ORDER BY h.slug, u.path), '[]')
        FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
        WHERE h.workspace_id = $1 AND u.valid_to IS NULL AND u.depth > 0),
    'shifts', (SELECT COALESCE(jsonb_agg(jsonb_build_object('id', s.id::text, 'name', s.name, 'kind', s.kind,
        'startsAt', s.starts_at, 'endsAt', s.ends_at, 'cancelledAt', s.cancelled_at,
        'members', (SELECT jsonb_agg(u.display_name ORDER BY u.display_name)
            FROM duty_members m JOIN users u ON u.id = m.user_id WHERE m.shift_id = s.id),
        'units', (SELECT jsonb_agg(COALESCE(u.name, c.unit_path::text) ORDER BY c.unit_path)
            FROM duty_service_units c LEFT JOIN units u ON (u.hierarchy_id, u.current_path) = (c.hierarchy_id, c.unit_path)
            WHERE c.shift_id = s.id)) ORDER BY s.starts_at DESC, s.id DESC), '[]')
        FROM duty_shifts s WHERE s.workspace_id = $1))
`

func (a api) dutyRoster(w http.ResponseWriter, r *http.Request) {
	workspace, ok := a.layerAdmin(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	var result json.RawMessage
	var err error
	status := http.StatusOK
	switch r.Method {
	case http.MethodGet:
		err = a.reader.QueryRow(ctx, dutyRosterRead, workspace).Scan(&result)
	case http.MethodDelete:
		id, parseErr := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if parseErr != nil || id < 1 {
			writeError(w, 400, "Invalid shift ID.")
			return
		}
		err = a.reader.QueryRow(ctx, `
WITH cancelled AS (
    UPDATE duty_shifts SET cancelled_at = statement_timestamp()
    WHERE workspace_id = $1 AND id = $2 AND cancelled_at IS NULL AND ends_at > statement_timestamp()
    RETURNING id
), audited AS (
    INSERT INTO audit_log(workspace_id, actor_kind, actor_id, action, target_type, target_id)
    SELECT $1, 'user', $3, 'duty.cancel', 'duty_shift', id::text FROM cancelled
)
SELECT jsonb_build_object('id', id::text) FROM cancelled`, workspace, id, subject).Scan(&result)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 404, "No upcoming or active shift with that ID.")
			return
		}
	case http.MethodPost:
		var input dutyShiftInput
		if !decodeDispatchInput(w, r, &input) {
			return
		}
		input.Name = strings.TrimSpace(input.Name)
		ids, valid := dispatchUnitIDs(input.Units)
		members, validMembers := dutyMemberIDs(input.Members)
		if !valid || len(ids) == 0 || !validMembers || !validDispatchName(input.Name) ||
			!slices.Contains([]string{"control_room", "squad", "watcher"}, input.Kind) ||
			input.StartsAt.IsZero() || !input.StartsAt.Before(input.EndsAt) {
			writeError(w, 400, "Provide a name, duty type, valid shift window, members and service units.")
			return
		}
		status = http.StatusCreated
		err = a.reader.QueryRow(ctx, `
WITH members AS MATERIALIZED (
    SELECT DISTINCT user_id FROM role_assignments
    WHERE workspace_id = $1 AND user_id = ANY($6::uuid[])
), covered AS MATERIALIZED (
    SELECT u.* FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
    WHERE h.workspace_id = $1 AND u.valid_to IS NULL AND u.depth > 0 AND u.id = ANY($7::bigint[])
), shift AS (
    INSERT INTO duty_shifts(workspace_id, name, kind, starts_at, ends_at)
    SELECT $1, $2, $3, $4, $5 WHERE (SELECT count(*) FROM members) = cardinality($6)
                                   AND (SELECT count(*) FROM covered) = cardinality($7)
    RETURNING id
), assigned AS (
    INSERT INTO duty_members(shift_id, user_id) SELECT shift.id, members.user_id FROM shift CROSS JOIN members
), service_area AS (
    INSERT INTO duty_service_units(workspace_id, shift_id, hierarchy_id, unit_path)
    SELECT $1, shift.id, covered.hierarchy_id, covered.path FROM shift CROSS JOIN covered
), audited AS (
    INSERT INTO audit_log(workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
    SELECT $1, 'user', $8, 'duty.schedule', 'duty_shift', id::text,
           jsonb_build_object('name', $2::text, 'kind', $3::text, 'startsAt', $4::timestamptz,
               'endsAt', $5::timestamptz, 'memberIds', $6::uuid[],
               'serviceUnits', (SELECT jsonb_agg(dispatch_unit(covered) ORDER BY id) FROM covered))
    FROM shift
)
SELECT jsonb_build_object('id', id::text) FROM shift`,
			workspace, input.Name, input.Kind, input.StartsAt, input.EndsAt, members, ids, subject).Scan(&result)
	}
	a.writeDispatchSettings(w, status, result, err)
}

func (a api) dispatchRules(w http.ResponseWriter, r *http.Request) {
	workspace, ok := a.layerAdmin(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var result json.RawMessage
	if r.Method == http.MethodGet {
		err := a.reader.QueryRow(ctx, `
SELECT COALESCE(jsonb_agg(jsonb_build_object('id', r.id::text, 'name', r.name, 'priority', r.priority,
    'effectiveFrom', r.effective_from, 'effectiveTo', r.effective_to, 'overlayLayer', r.overlay_layer,
    'overlayCode', r.overlay_code, 'overlayZone', r.overlay_zone,
    'lead', jsonb_build_object('hierarchyId', r.lead_hierarchy_id, 'path', r.lead_path::text),
    'supporting', (SELECT COALESCE(jsonb_agg(jsonb_build_object('hierarchyId', s.hierarchy_id,
        'path', s.unit_path::text) ORDER BY s.hierarchy_id, s.unit_path), '[]')
        FROM dispatch_rule_support s WHERE s.rule_id = r.id)) ORDER BY r.priority DESC, r.id), '[]')
FROM dispatch_rules r WHERE r.workspace_id = $1`, workspace).Scan(&result)
		a.writeDispatchSettings(w, http.StatusOK, result, err)
		return
	}

	var input dispatchRuleInput
	if !decodeDispatchInput(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	lead, parseErr := strconv.ParseInt(input.Lead, 10, 64)
	support, valid := dispatchUnitIDs(input.Supporting)
	if parseErr != nil || lead < 1 || !valid || slices.Contains(support, lead) || !validDispatchName(input.Name) ||
		input.EffectiveFrom.IsZero() || (input.EffectiveTo != nil && !input.EffectiveFrom.Before(*input.EffectiveTo)) ||
		!validDispatchSelector(input) {
		writeError(w, 400, "Provide a name, effective dates, a lead unit and a valid overlay selector.")
		return
	}
	subject := ctx.Value(identityKey{}).(identity).Subject
	err := a.reader.QueryRow(ctx, `
WITH targets AS MATERIALIZED (
    SELECT u.* FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
    WHERE h.workspace_id = $1 AND u.valid_to IS NULL AND u.depth > 0
      AND (u.id = $9 OR u.id = ANY($10::bigint[]))
), rule AS (
    INSERT INTO dispatch_rules(workspace_id, name, priority, effective_from, effective_to,
        overlay_layer, overlay_code, overlay_zone, lead_hierarchy_id, lead_path)
    SELECT $1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''), hierarchy_id, path
    FROM targets WHERE id = $9 AND (SELECT count(*) FROM targets) = 1 + cardinality($10)
      AND ($6 = '' OR EXISTS (SELECT FROM layers l JOIN layer_versions v ON v.layer_id = l.id
         WHERE l.workspace_id = $1 AND l.slug = $6 AND v.overlay_kind IS NOT NULL))
    RETURNING id
), supporting AS (
    INSERT INTO dispatch_rule_support(workspace_id, rule_id, hierarchy_id, unit_path)
    SELECT $1, rule.id, targets.hierarchy_id, targets.path FROM rule CROSS JOIN targets WHERE targets.id <> $9
), audited AS (
    INSERT INTO audit_log(workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
    SELECT $1, 'user', $11, 'dispatch.rule.create', 'dispatch_rule', id::text,
           jsonb_build_object('name', $2::text, 'priority', $3::int, 'effectiveFrom', $4::timestamptz,
               'effectiveTo', $5::timestamptz, 'overlayLayer', NULLIF($6, ''), 'overlayCode', NULLIF($7, ''),
               'overlayZone', NULLIF($8, ''), 'leadUnitId', $9::bigint::text, 'supportingUnitIds', $10::bigint[])
    FROM rule
)
SELECT jsonb_build_object('id', id::text) FROM rule`, workspace, input.Name, input.Priority, input.EffectiveFrom,
		input.EffectiveTo, input.OverlayLayer, input.OverlayCode, input.OverlayZone, lead, support, subject).Scan(&result)
	a.writeDispatchSettings(w, http.StatusCreated, result, err)
}

func (a api) endDispatchRule(w http.ResponseWriter, r *http.Request) {
	workspace, ok := a.layerAdmin(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "Invalid rule ID.")
		return
	}
	var input struct {
		EffectiveTo time.Time `json:"effectiveTo"`
	}
	if !decodeDispatchInput(w, r, &input) {
		return
	}
	if input.EffectiveTo.IsZero() {
		writeError(w, 400, "Provide an end time.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	var result json.RawMessage
	err = a.reader.QueryRow(ctx, `
WITH ended AS (
    UPDATE dispatch_rules SET effective_to = $3
    WHERE workspace_id = $1 AND id = $2 AND $3 > effective_from AND $3 >= statement_timestamp()
      AND (effective_to IS NULL OR $3 < effective_to)
    RETURNING id, effective_to
), audited AS (
    INSERT INTO audit_log(workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
    SELECT $1, 'user', $4, 'dispatch.rule.end', 'dispatch_rule', id::text,
           jsonb_build_object('effectiveTo', effective_to) FROM ended
)
SELECT jsonb_build_object('id', id::text, 'effectiveTo', effective_to) FROM ended`,
		workspace, id, input.EffectiveTo, subject).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 422, "Choose an active rule and a future end time within its window.")
		return
	}
	a.writeDispatchSettings(w, http.StatusOK, result, err)
}

func decodeDispatchInput(w http.ResponseWriter, r *http.Request, input any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if d.Decode(input) == nil && d.Decode(new(any)) == io.EOF {
		return true
	}
	writeError(w, 400, "Provide a valid settings record.")
	return false
}

func validDispatchName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= 120 && !strings.ContainsRune(name, 0)
}

func validDispatchSelector(input dispatchRuleInput) bool {
	return len(input.OverlayLayer) <= 120 && len(input.OverlayCode) <= 120 &&
		!strings.ContainsRune(input.OverlayLayer+input.OverlayCode, 0) &&
		slices.Contains([]string{"", "core", "buffer"}, input.OverlayZone) &&
		(input.OverlayLayer != "" || (input.OverlayCode == "" && input.OverlayZone == ""))
}

func dispatchUnitIDs(input []string) ([]int64, bool) {
	ids := make([]int64, 0, len(input))
	if len(input) > 100 {
		return nil, false
	}
	for _, value := range input {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id < 1 || slices.Contains(ids, id) {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

func dutyMemberIDs(input []string) ([]pgtype.UUID, bool) {
	ids := make([]pgtype.UUID, 0, len(input))
	if len(input) == 0 || len(input) > 100 {
		return nil, false
	}
	for _, value := range input {
		var id pgtype.UUID
		if id.Scan(value) != nil || !id.Valid || slices.Contains(ids, id) {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

func (a api) writeDispatchSettings(w http.ResponseWriter, status int, result json.RawMessage, err error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, 422, "Choose current units, workspace members and layers from this workspace.")
	case err != nil:
		a.logger.Error("Could not load or save dispatch settings", "error", err)
		writeError(w, 500, "Could not load or save dispatch settings.")
	default:
		writeJSON(w, status, result)
	}
}
