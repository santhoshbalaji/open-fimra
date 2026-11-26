package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type operationsAccess struct {
	workspace, user, owner, kind string
	officer                      bool
}

func isReferral(kind string) bool { return kind == "offence" || kind == "relief" }

func fullOperationsAccess(ctx context.Context, tx pgx.Tx, subject, slug, id string) (operationsAccess, error) {
	var access operationsAccess
	if !uuidPattern.MatchString(id) {
		return access, pgx.ErrNoRows
	}
	err := tx.QueryRow(ctx, `SELECT r.workspace_id::text, g.user_id::text, r.owner_id::text, r.kind, g.officer
FROM operations_records r CROSS JOIN LATERAL operations_roles($1, $2, r.id) g
WHERE r.id = $3 AND g.full_access`, subject, slug, id).Scan(
		&access.workspace, &access.user, &access.owner, &access.kind, &access.officer)
	return access, err
}

func (access operationsAccess) canWrite() bool {
	return isReferral(access.kind) || access.officer || access.user == access.owner
}

func auditOperations(ctx context.Context, tx pgx.Tx, subject, id, action string, alert *int64, submission *string) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_log(workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
SELECT workspace_id, 'user', $2, $3, 'operations_record', id::text,
    jsonb_strip_nulls(jsonb_build_object('kind', kind, 'alertId', $4::bigint, 'submissionId', $5::uuid))
FROM operations_records WHERE id = $1`, id, subject, action, alert, submission)
	return err
}

func lockOperationsRecord(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `SELECT 1 FROM operations_records WHERE id = $1 FOR UPDATE`, id)
	return err
}

// Summary-only callers get an allowlisted object. The existence of a pending
// referral and its submission IDs, status and owner are private as well.
const operationsSummary = `
LEFT JOIN LATERAL (
    SELECT s.answers->>'public_summary' AS summary
    FROM submissions s JOIN approvals a ON a.submission_id = s.id AND a.decision = 'approved'
    WHERE s.operations_record_id = r.id AND r.kind IN ('offence', 'relief')
    ORDER BY s.received_at DESC, s.id DESC LIMIT 1
) released ON true`

const operationsCard = `jsonb_build_object('id', r.id, 'kind', r.kind, 'incidentId', r.incident_id::text,
    'summary', released.summary, 'summaryOnly', NOT g.full_access)
|| CASE WHEN g.full_access THEN jsonb_build_object(
    'unitPath', r.unit_path::text, 'owner', owner.display_name, 'completed', EXISTS (
        SELECT FROM submissions s JOIN approvals ap ON ap.submission_id = s.id AND ap.decision = 'approved'
        WHERE s.operations_record_id = r.id AND s.completes_record
    )) ELSE '{}'::jsonb END`

const operationsVisibility = `(g.full_access OR (released.summary IS NOT NULL
    AND EXISTS (SELECT FROM incident_alert_roles($1, $2, r.incident_id))))`

func (a api) operationsRecords(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	if r.Method == http.MethodPost {
		a.createOperationsRecord(ctx, w, r, subject, slug)
		return
	}
	id := chi.URLParam(r, "record")
	if id != "" {
		a.operationsDetail(ctx, w, r, subject, slug, id)
		return
	}
	var after *string
	if value := r.URL.Query().Get("cursor"); value != "" {
		if !uuidPattern.MatchString(value) {
			writeError(w, http.StatusBadRequest, "Invalid records cursor.")
			return
		}
		after = &value
	}
	var incident *int64
	if value := r.URL.Query().Get("incident"); value != "" {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "Invalid case ID.")
			return
		}
		incident = &n
	}
	var items []json.RawMessage
	var next string
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, slug)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT r.id::text, g.full_access AND r.kind IN ('offence', 'relief'), `+operationsCard+`
FROM operations_records r JOIN users owner ON owner.id = r.owner_id
CROSS JOIN LATERAL operations_roles($1, $2, r.id) g`+operationsSummary+`
WHERE r.workspace_id = $3 AND ($4::uuid IS NULL OR r.id > $4)
  AND ($5::bigint IS NULL OR r.incident_id = $5) AND `+operationsVisibility+`
ORDER BY r.id LIMIT 51`, subject, slug, access.workspaceID, after, incident)
		if err != nil {
			return err
		}
		defer rows.Close()
		items = make([]json.RawMessage, 0, 50)
		var restricted []string
		var last string
		for rows.Next() {
			var id string
			var private bool
			var item json.RawMessage
			if err := rows.Scan(&id, &private, &item); err != nil {
				return err
			}
			if len(items) == 50 {
				next = last
				break
			}
			items = append(items, item)
			last = id
			if private {
				restricted = append(restricted, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(restricted) == 0 {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_log(workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
SELECT workspace_id, 'user', $2, 'referral.read', 'operations_record', id::text, jsonb_build_object('kind', kind)
FROM operations_records WHERE id = ANY($1::uuid[])`, restricted, subject)
		return err
	})
	if err != nil {
		a.writeProblem(w, err, "Records not found.", "Could not load records")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []json.RawMessage `json:"items"`
		Next  string            `json:"nextCursor,omitempty"`
	}{items, next})
}

func (a api) operationsDetail(ctx context.Context, w http.ResponseWriter, r *http.Request, subject, slug, id string) {
	var item json.RawMessage
	var before *string
	if cursor := r.URL.Query().Get("before"); cursor != "" {
		if !uuidPattern.MatchString(cursor) {
			writeError(w, http.StatusBadRequest, "Invalid entries cursor.")
			return
		}
		before = &cursor
	}
	if !uuidPattern.MatchString(id) {
		writeError(w, http.StatusNotFound, "Record not found.")
		return
	}
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		var kind string
		var full bool
		err := tx.QueryRow(ctx, `SELECT r.kind, g.full_access, `+operationsCard+`
|| CASE WHEN g.full_access THEN jsonb_build_object(
    'canWrite', r.kind IN ('offence', 'relief') OR g.officer OR r.owner_id = g.user_id,
    'canLink', r.kind = 'burn' AND g.officer AND burn.approved,
    'burnAuthorised', r.kind = 'burn' AND burn.approved,
    'completionPending', EXISTS (
        SELECT FROM submissions s WHERE s.operations_record_id = r.id AND s.completes_record
          AND NOT EXISTS (SELECT FROM approvals ap WHERE ap.submission_id = s.id)
    ),
    'alerts', COALESCE((SELECT jsonb_agg(b.alert_id::text ORDER BY b.alert_id)
        FROM prescribed_burn_alerts b WHERE b.record_id = r.id
          AND EXISTS (SELECT FROM alert_roles($1, $2, b.alert_id))), '[]'),
    'entries', COALESCE((SELECT jsonb_agg(to_jsonb(entry) ORDER BY entry."receivedAt" DESC, entry.id DESC) FROM (
        SELECT s.id, s.received_at AS "receivedAt", f.form_key AS form, v.version, v.definition->'title' AS title,
               s.completes_record AS "completesRecord", ap.decision
        FROM submissions s JOIN form_versions v ON v.id = s.form_version_id JOIN forms f ON f.id = v.form_id
        LEFT JOIN approvals ap ON ap.submission_id = s.id
        WHERE s.operations_record_id = r.id AND ($4::uuid IS NULL OR (s.received_at, s.id) <
            (SELECT cursor.received_at, cursor.id FROM submissions cursor WHERE cursor.operations_record_id = r.id AND cursor.id = $4))
        ORDER BY s.received_at DESC, s.id DESC LIMIT 51
    ) entry), '[]')) ELSE '{}'::jsonb END
FROM operations_records r JOIN users owner ON owner.id = r.owner_id
CROSS JOIN LATERAL operations_roles($1, $2, r.id) g`+operationsSummary+`
CROSS JOIN LATERAL (SELECT EXISTS (SELECT FROM submissions s JOIN approvals ap ON ap.submission_id = s.id
    WHERE r.kind = 'burn' AND s.operations_record_id = r.id AND NOT s.completes_record AND ap.decision = 'approved') AS approved) burn
WHERE r.id = $3 AND `+operationsVisibility, subject, slug, id, before).Scan(&kind, &full, &item)
		if err == nil && full && isReferral(kind) {
			return auditOperations(ctx, tx, subject, id, "referral.read", nil, nil)
		}
		return err
	})
	if err != nil {
		a.writeProblem(w, err, "Record not found.", "Could not load the record")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

type operationsRequest struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Unit       string `json:"unit"`
	Owner      string `json:"owner"`
	IncidentID string `json:"incidentId"`
}

func decodeOperations(w http.ResponseWriter, r *http.Request, dest any, limit int64) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	return d.Decode(dest) == nil && d.Decode(new(any)) == io.EOF
}

func (a api) createOperationsRecord(ctx context.Context, w http.ResponseWriter, r *http.Request, subject, slug string) {
	var in operationsRequest
	if !decodeOperations(w, r, &in, 16<<10) || !uuidPattern.MatchString(in.ID) ||
		!validOperationsUnit(in.Unit) || (in.Owner != "" && !uuidPattern.MatchString(in.Owner)) {
		writeError(w, http.StatusBadRequest, "Provide a record kind, a record ID and a unit ID.")
		return
	}
	referral := isReferral(in.Kind)
	var incident *int64
	if referral {
		n, err := strconv.ParseInt(in.IncidentID, 10, 64)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "A referral needs a case ID.")
			return
		}
		incident = &n
	} else if in.IncidentID != "" {
		writeError(w, http.StatusBadRequest, "Only referrals take a case ID.")
		return
	}
	status := http.StatusCreated
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, slug)
		if err != nil {
			return err
		}
		var hierarchy, path string
		var officer bool
		err = tx.QueryRow(ctx, `SELECT u.hierarchy_id::text, u.path::text, g.officer
FROM units u JOIN hierarchies h ON h.id = u.hierarchy_id
CROSS JOIN LATERAL operations_scope($1, h.workspace_id, h.id, u.path) g
WHERE h.workspace_id = $2 AND u.id = $3::text::bigint AND u.valid_to IS NULL`, subject, access.workspaceID, in.Unit).Scan(&hierarchy, &path, &officer)
		if err != nil {
			return err
		}
		var published bool
		err = tx.QueryRow(ctx, `SELECT EXISTS (
SELECT FROM operations_forms o JOIN forms f USING (form_key) JOIN form_versions v ON v.form_id = f.id
WHERE o.kind = $1 AND NOT o.completion_only AND f.workspace_id = $2 AND v.published_at IS NOT NULL
AND (f.level <> 'division' OR (f.owner_hierarchy_id = $3 AND $4::ltree <@ f.owner_unit_path))
)`, in.Kind, access.workspaceID, hierarchy, path).Scan(&published)
		if err != nil {
			return err
		}
		if !published {
			return problem(http.StatusUnprocessableEntity, "No published form for this record kind covers the unit.")
		}
		if referral {
			var permitted, caseVisible bool
			err := tx.QueryRow(ctx, `SELECT EXISTS (
    SELECT FROM referral_grants WHERE workspace_id = $3 AND user_id = $4 AND kind = $5
      AND hierarchy_id = $6 AND unit_path @> $7::ltree
), EXISTS (
    SELECT FROM incident_alert_roles($1, $2, $8) v JOIN alerts a ON a.id = v.alert_id
    WHERE match_under_grant(a.hierarchy_unit_paths, $6, $7::ltree, CASE WHEN nlevel($7::ltree) = 1 THEN 0 ELSE 1 END::smallint)
       OR (a.hierarchy_id = $6 AND a.unit_path <@ $7::ltree)
)`, subject, slug, access.workspaceID, access.userID, in.Kind, hierarchy, path, incident).Scan(&permitted, &caseVisible)
			if err != nil {
				return err
			}
			if !permitted {
				return problem(http.StatusForbidden, "A scoped grant is required for this referral kind.")
			}
			if !caseVisible {
				return problem(http.StatusNotFound, "Case not found in this unit.")
			}
		}
		if in.Owner == "" {
			in.Owner = access.userID
		}
		if in.Owner != access.userID {
			if !officer {
				return problem(http.StatusForbidden, "Only a supervising officer can assign another owner.")
			}
			var allowed bool
			err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM users u
    CROSS JOIN LATERAL operations_scope(u.oidc_subject, $1, $2, $3::ltree) g WHERE u.id = $4)`,
				access.workspaceID, hierarchy, path, in.Owner).Scan(&allowed)
			if err != nil {
				return err
			}
			if !allowed {
				return problem(http.StatusUnprocessableEntity, "The owner needs a role covering this unit.")
			}
		}
		tag, err := tx.Exec(ctx, `INSERT INTO operations_records(id, workspace_id, hierarchy_id, unit_path, kind, incident_id, owner_id)
VALUES ($1, $2, $3, $4::ltree, $5, $6, $7) ON CONFLICT (id) DO NOTHING`,
			in.ID, access.workspaceID, hierarchy, path, in.Kind, incident, in.Owner)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var same bool
			err = tx.QueryRow(ctx, `SELECT workspace_id = $2 AND hierarchy_id = $3 AND unit_path = $4::ltree
    AND kind = $5 AND incident_id IS NOT DISTINCT FROM $6::bigint AND owner_id = $7
FROM operations_records WHERE id = $1`, in.ID, access.workspaceID, hierarchy, path, in.Kind, incident, in.Owner).Scan(&same)
			if err != nil {
				return err
			}
			if !same {
				return problem(http.StatusConflict, "This record ID already belongs to another record.")
			}
			status = http.StatusOK
			return nil
		}
		return auditOperations(ctx, tx, subject, in.ID, "operations.create", nil, nil)
	})
	if err != nil {
		a.writeProblem(w, err, "Unit not found.", "Could not create the record")
		return
	}
	w.Header().Set("Location", "/api/v1/workspaces/"+slug+"/operations-records/"+in.ID)
	writeJSON(w, status, map[string]string{"id": in.ID})
}

type operationsSubmissionRequest struct {
	submissionRequest
	CompletesRecord bool `json:"completesRecord"`
}

func (a api) operationsSubmission(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	id := chi.URLParam(r, "record")
	subject := ctx.Value(identityKey{}).(identity).Subject
	slug := chi.URLParam(r, "slug")
	var in operationsSubmissionRequest
	if !decodeOperations(w, r, &in, maxSubmissionBytes) || !uuidPattern.MatchString(in.ID) ||
		!formKeyPattern.MatchString(in.Form) || in.Version < 1 || len(in.Values) == 0 || in.Step != "" || in.Location != nil {
		writeError(w, http.StatusBadRequest, "Provide a submission ID, a form, its version and the values.")
		return
	}
	canonical, _ := json.Marshal([]any{id, subject, in})
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	var result json.RawMessage
	status := http.StatusOK
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		access, err := fullOperationsAccess(ctx, tx, subject, slug, id)
		if err != nil {
			return err
		}
		if !access.canWrite() {
			return problem(http.StatusForbidden, "Only the owner or a supervising officer can update this record.")
		}
		if err := lockOperationsRecord(ctx, tx, id); err != nil {
			return err
		}
		var noAlert int64
		found, err := earlierSubmission(ctx, tx, in.ID, hash, access.user, &noAlert)
		if err != nil {
			return err
		}
		if !found {
			if err := storeOperationsSubmission(ctx, tx, access, id, in, hash); err != nil {
				return err
			}
			status = http.StatusCreated
		}
		if isReferral(access.kind) {
			if err := auditOperations(ctx, tx, subject, id, "referral.read", nil, &in.ID); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `SELECT `+submissionJSON+submissionFrom+` WHERE s.id = $1`, in.ID).Scan(&result)
	})
	if err != nil {
		a.writeProblem(w, err, "Record not found.", "Could not save the entry")
		return
	}
	w.Header().Set("Location", "/api/v1/workspaces/"+slug+"/submissions/"+in.ID)
	writeJSON(w, status, result)
}

func storeOperationsSubmission(ctx context.Context, tx pgx.Tx, access operationsAccess, id string, in operationsSubmissionRequest, hash string) error {
	var formVersion int64
	var source string
	var completionOnly, openForEntries, authorisationApproved bool
	err := tx.QueryRow(ctx, `SELECT v.id, v.source, o.completion_only,
    NOT EXISTS (SELECT FROM submissions s LEFT JOIN approvals ap ON ap.submission_id = s.id
                WHERE s.operations_record_id = r.id AND s.completes_record AND ap.decision IS DISTINCT FROM 'returned'),
    EXISTS (SELECT FROM submissions s JOIN approvals ap ON ap.submission_id = s.id
            WHERE s.operations_record_id = r.id AND NOT s.completes_record AND ap.decision = 'approved')
FROM operations_records r JOIN operations_forms o ON o.kind = r.kind
JOIN forms f ON f.form_key = o.form_key AND f.workspace_id = r.workspace_id
JOIN form_versions v ON v.form_id = f.id
WHERE r.id = $1 AND o.form_key = $2 AND v.version = $3 AND v.published_at IS NOT NULL
  AND (f.level <> 'division' OR (f.owner_hierarchy_id = r.hierarchy_id AND r.unit_path <@ f.owner_unit_path))`,
		id, in.Form, in.Version).Scan(&formVersion, &source, &completionOnly, &openForEntries, &authorisationApproved)
	if errors.Is(err, pgx.ErrNoRows) {
		return problem(http.StatusUnprocessableEntity, "This form version is unavailable for the record.")
	}
	if err != nil {
		return err
	}
	if !openForEntries {
		return problem(http.StatusConflict, "A completion is already recorded or awaiting review.")
	}
	if access.kind == "burn" && (in.CompletesRecord != completionOnly || (completionOnly && !authorisationApproved)) {
		return problem(http.StatusUnprocessableEntity, "A burn needs an approved authorisation before completion.")
	}
	tree, err := parseFormTree([]byte(source))
	if err != nil {
		return err
	}
	if err := checkOperationsForm(access.kind, tree); err != nil {
		return err
	}
	check := makeAnswerCheck(ctx, tx, tree, -1)
	check.now = time.Now()
	if in.DeviceTime != nil && in.DeviceTime.After(check.now.Add(clockSkew)) {
		return problem(http.StatusUnprocessableEntity, "The device time cannot be in the future.")
	}
	if !isReferral(access.kind) {
		if err := loadAnswerAttachments(ctx, tx, check, 0, &id); err != nil {
			return err
		}
	}
	answers, problems := checkAnswers(check, in.Values)
	if len(problems) > 0 {
		return problem(http.StatusUnprocessableEntity, "The answers do not fit the form.", problems...)
	}
	if isReferral(access.kind) {
		if err := validateReferralAnswers(access.kind, answers); err != nil {
			return err
		}
	}
	if access.kind == "burn" && !completionOnly {
		start, hasStart := answers["window_start"].(string)
		end, hasEnd := answers["window_end"].(string)
		if hasStart && hasEnd && end < start {
			return problem(http.StatusUnprocessableEntity, "The burn window must end on or after its first date.")
		}
	}
	return storeSubmission(ctx, tx, in.submissionRequest, storedSubmission{
		workspaceID: access.workspace, versionID: formVersion, operationsRecord: &id,
		user: access.user, hash: hash, answers: answers, used: check.used, completesRecord: in.CompletesRecord,
	})
}

func checkOperationsForm(kind string, tree *formTree) error {
	if tree.hasStages {
		return problem(http.StatusUnprocessableEntity, "An operations form cannot have workflow stages.")
	}
	for _, node := range tree.byKey {
		if node.kind == "alert-value" {
			return problem(http.StatusUnprocessableEntity, "An operations form cannot fill in values from an alert.")
		}
		if isReferral(kind) && node.isField() && (node.kind != "text" || node.repeat != nil || !referralField(kind, node.key)) {
			return problem(http.StatusUnprocessableEntity, "A referral form can hold only its summary and external reference fields.")
		}
	}
	return nil
}

func referralField(kind, key string) bool {
	switch key {
	case "public_summary", "case_reference":
		return true
	case "evidence_reference", "enquiry_reference":
		return kind == "offence"
	case "beneficiary_reference", "payment_reference":
		return kind == "relief"
	}
	return false
}

var referralReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,119}$`)

func validateReferralAnswers(kind string, answers map[string]any) error {
	summary, _ := answers["public_summary"].(string)
	caseRef, _ := answers["case_reference"].(string)
	if strings.TrimSpace(summary) == "" || len([]rune(summary)) > 500 || !referralReference.MatchString(caseRef) {
		return problem(http.StatusUnprocessableEntity, "Provide a case reference and a summary for approval.")
	}
	for key, answer := range answers {
		if key == "public_summary" {
			continue
		}
		value, _ := answer.(string)
		if !referralField(kind, key) || !referralReference.MatchString(value) {
			return problem(http.StatusUnprocessableEntity, "Use external reference IDs in the restricted fields.")
		}
		if strings.Contains(strings.ToLower(summary), strings.ToLower(value)) {
			return problem(http.StatusUnprocessableEntity, "Leave external references out of the summary.")
		}
	}
	return nil
}
