package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// Invitations give an officer a role before they have ever signed in. The
// account itself lives in the agency's identity provider. An invitation is a
// pending grant keyed by email address. The first sign-in with a token whose
// email claim matches and is verified turns it into a role grant. Invited
// addresses are personal data: the row is deleted when it is claimed or
// expires, audit entries never include the address, and nothing logs it.

const (
	invitationTTL        = 14 * 24 * time.Hour
	invitationEmailQueue = "invitation_email"
	invitationCleanup    = "invitation_cleanup"
)

var invitationRoles = map[string]string{
	"state_admin":      "State administrator",
	"division_officer": "Division officer",
	"field_staff":      "Field staff",
}

type invitationRequest struct {
	Email    string `json:"email"`
	Role     string `json:"role"`
	UnitPath string `json:"unitPath"`
}

type invitation struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	UnitPath    string    `json:"unitPath"`
	UnitName    string    `json:"unitName"`
	ExpiresAt   time.Time `json:"expiresAt"`
	EmailStatus string    `json:"emailStatus"`
}

// maskEmail keeps the first character and the domain, which is enough for an
// administrator to recognise an invitation in a list.
func maskEmail(address string) string {
	local, domain, ok := strings.Cut(address, "@")
	if !ok || local == "" {
		return "…"
	}
	return local[:1] + "…@" + domain
}

func (a api) invitationError(w http.ResponseWriter, err error) {
	a.logger.Error("Could not handle invitations", "error", err)
	writeError(w, http.StatusInternalServerError, "Could not handle invitations.")
}

func (a api) invitations(w http.ResponseWriter, r *http.Request) {
	s, ok := a.setupAdmin(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	switch r.Method {
	case http.MethodPost:
		a.createInvitation(w, r.WithContext(ctx), s)
		return
	case http.MethodDelete:
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id < 1 {
			writeError(w, http.StatusNotFound, "The invitation was not found.")
			return
		}
		caller := ctx.Value(identityKey{}).(identity)
		var removed bool
		err = a.pool.QueryRow(ctx, `
			WITH removed AS (
				DELETE FROM invitations WHERE id = $1 AND workspace_id = $2 RETURNING id
			), audited AS (
				INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id)
				SELECT $2, 'user', $3, 'invitation.revoke', 'invitation', id::text FROM removed
			)
			SELECT EXISTS (SELECT 1 FROM removed)
		`, id, s.workspaceID, caller.Subject).Scan(&removed)
		if err != nil {
			a.invitationError(w, err)
			return
		}
		if !removed {
			writeError(w, http.StatusNotFound, "The invitation was not found.")
			return
		}
	}
	if _, err := expireInvitations(ctx, a.pool); err != nil {
		a.invitationError(w, err)
		return
	}
	rows, err := a.pool.Query(ctx, `
		SELECT i.id::text, i.email, i.role_slug, i.unit_path::text, COALESCE(u.name, ''), i.expires_at, i.email_status
		FROM invitations i LEFT JOIN units u ON u.hierarchy_id = i.hierarchy_id AND u.current_path = i.unit_path
		WHERE i.workspace_id = $1 ORDER BY i.created_at, i.id
	`, s.workspaceID)
	if err != nil {
		a.invitationError(w, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[invitation])
	if err != nil {
		a.invitationError(w, err)
		return
	}
	for i := range list {
		list[i].Email = maskEmail(list[i].Email)
	}
	if list == nil {
		list = []invitation{}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"invitations": list, "emailEnabled": a.emailEnabled})
}

func (a api) createInvitation(w http.ResponseWriter, r *http.Request, s setupContext) {
	ctx := r.Context()
	var req invitationRequest
	if !decodeJSON(w, r, 4096, &req) {
		return
	}
	address := strings.ToLower(notificationAddress(strings.TrimSpace(req.Email)))
	if address == "" {
		writeError(w, http.StatusBadRequest, "Enter one plain email address, such as officer@example.invalid.")
		return
	}
	if _, known := invitationRoles[req.Role]; !known {
		writeError(w, http.StatusBadRequest, "Choose the state administrator, division officer or field staff role.")
		return
	}
	if !unitPrefix.MatchString(req.UnitPath) {
		writeError(w, http.StatusBadRequest, "Choose a unit.")
		return
	}
	caller := ctx.Value(identityKey{}).(identity)
	sendEmail := a.emailEnabled && a.jobs != nil
	var id int64
	var missing bool
	err := pgx.BeginFunc(ctx, a.pool, func(tx pgx.Tx) error {
		var unitFound bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM units WHERE hierarchy_id = $1 AND current_path = $2::ltree)
		`, s.hierarchyID, req.UnitPath).Scan(&unitFound); err != nil {
			return fmt.Errorf("check the invited unit: %w", err)
		}
		if !unitFound {
			missing = true
			return nil
		}
		status := "off"
		if sendEmail {
			status = "pending"
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO invitations (workspace_id, hierarchy_id, unit_path, role_slug, email, invited_by, expires_at, email_status)
			VALUES ($1, $2, $3::ltree, $4, $5, (SELECT id FROM users WHERE oidc_subject = $6), now() + $7::bigint * interval '1 second', $8)
			ON CONFLICT (workspace_id, hierarchy_id, unit_path, role_slug, email) DO UPDATE
				SET expires_at = EXCLUDED.expires_at, email_status = EXCLUDED.email_status, invited_by = EXCLUDED.invited_by
			RETURNING id
		`, s.workspaceID, s.hierarchyID, req.UnitPath, req.Role, address, caller.Subject,
			int64(invitationTTL/time.Second), status).Scan(&id)
		if err != nil {
			return fmt.Errorf("save the invitation: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
			VALUES ($1, 'user', $2, 'invitation.create', 'invitation', $3::bigint::text,
				jsonb_build_object('role', $4::text, 'unitPath', $5::text, 'email', $6::boolean))
		`, s.workspaceID, caller.Subject, id, req.Role, req.UnitPath, sendEmail); err != nil {
			return fmt.Errorf("audit the invitation: %w", err)
		}
		if sendEmail {
			if _, err := a.jobs.InsertTx(ctx, tx, invitationEmailArgs{InvitationID: id}, nil); err != nil {
				return fmt.Errorf("queue the invitation email: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		a.invitationError(w, err)
		return
	}
	if missing {
		writeError(w, http.StatusBadRequest, "The unit was not found. Publish the hierarchy layers first.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": strconv.FormatInt(id, 10), "emailStatus": map[bool]string{true: "pending", false: "off"}[sendEmail]})
}

// claimInvitations turns the caller's live invitations into role grants. It
// needs a verified email claim. It deletes every invitation for the address,
// including expired ones, and returns the number of grants made.
func claimInvitations(ctx context.Context, db ingestDB, caller identity, userID pgtype.UUID) (int, error) {
	if caller.VerifiedEmail == "" {
		return 0, nil
	}
	var claimed int
	err := db.QueryRow(ctx, `
		WITH matched AS (
			DELETE FROM invitations WHERE email = $2
			RETURNING id, workspace_id, hierarchy_id, unit_path, role_slug, invited_by, expires_at > now() AS live
		), usable AS (
			SELECT m.* FROM matched m WHERE m.live AND EXISTS (
				SELECT 1 FROM units u WHERE u.hierarchy_id = m.hierarchy_id AND u.current_path = m.unit_path)
		), granted AS (
			INSERT INTO role_assignments (user_id, workspace_id, hierarchy_id, unit_path, role_slug, granted_by)
			SELECT $1, workspace_id, hierarchy_id, unit_path, role_slug, invited_by FROM usable
			ON CONFLICT DO NOTHING
		), audited AS (
			INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
			SELECT m.workspace_id, CASE WHEN m.live THEN 'user' ELSE 'system' END, CASE WHEN m.live THEN $3 END,
				CASE WHEN u.id IS NOT NULL THEN 'invitation.claim' WHEN m.live THEN 'invitation.discard'
				     ELSE 'invitation.expire' END,
				'invitation', m.id::text, jsonb_build_object('role', m.role_slug, 'unitPath', m.unit_path::text)
			FROM matched m LEFT JOIN usable u ON u.id = m.id
		)
		SELECT count(*) FROM usable
	`, userID, caller.VerifiedEmail, caller.Subject).Scan(&claimed)
	if err != nil {
		return 0, fmt.Errorf("claim invitations: %w", err)
	}
	return claimed, nil
}

// expireInvitations deletes invitations past their expiry time.
func expireInvitations(ctx context.Context, db ingestDB) (int64, error) {
	var expired int64
	err := db.QueryRow(ctx, `
		WITH expired AS (
			DELETE FROM invitations WHERE expires_at <= now() RETURNING id, workspace_id, role_slug, unit_path
		), audited AS (
			INSERT INTO audit_log (workspace_id, actor_kind, action, target_type, target_id, details)
			SELECT workspace_id, 'system', 'invitation.expire', 'invitation', id::text,
				jsonb_build_object('role', role_slug, 'unitPath', unit_path::text)
			FROM expired
		)
		SELECT count(*) FROM expired
	`).Scan(&expired)
	if err != nil {
		return 0, fmt.Errorf("expire invitations: %w", err)
	}
	return expired, nil
}

type invitationEmailArgs struct {
	InvitationID int64 `json:"invitation_id"`
}

func (invitationEmailArgs) Kind() string { return invitationEmailQueue }

func (invitationEmailArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: invitationEmailQueue, MaxAttempts: maxEmailAttempts}
}

type invitationMessage struct {
	id                            int64
	address, workspace, roleLabel string
	unitName                      string
	expiresAt                     time.Time
}

type invitationSender interface {
	sendInvitation(context.Context, invitationMessage) (string, error)
}

type invitationEmailWorker struct {
	river.WorkerDefaults[invitationEmailArgs]
	db     *pgxpool.Pool
	sender invitationSender
}

func (w *invitationEmailWorker) Timeout(*river.Job[invitationEmailArgs]) time.Duration {
	return time.Minute
}

// The address is read from the invitation at send time, so the job queue
// never holds it, and a claimed or revoked invitation sends nothing.
func (w *invitationEmailWorker) Work(ctx context.Context, job *river.Job[invitationEmailArgs]) error {
	var m invitationMessage
	var role string
	err := w.db.QueryRow(ctx, `
		SELECT i.id, i.email, ws.name, i.role_slug, COALESCE(u.name, i.unit_path::text), i.expires_at
		FROM invitations i JOIN workspaces ws ON ws.id = i.workspace_id
		LEFT JOIN units u ON u.hierarchy_id = i.hierarchy_id AND u.current_path = i.unit_path
		WHERE i.id = $1 AND i.email_status = 'pending' AND i.expires_at > now()
	`, job.Args.InvitationID).Scan(&m.id, &m.address, &m.workspace, &role, &m.unitName, &m.expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the invitation: %w", err)
	}
	m.roleLabel = invitationRoles[role]
	status, sendErr := w.sender.sendInvitation(ctx, m)
	final := status == "delivered" || status == "failed" || status == "uncertain" || job.Attempt >= job.MaxAttempts
	if !final {
		return sendErr
	}
	result := "sent"
	if status != "delivered" {
		result = "failed"
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := w.db.Exec(finish, `
		WITH changed AS (
			UPDATE invitations SET email_status = $2 WHERE id = $1 AND email_status = 'pending' RETURNING workspace_id
		)
		INSERT INTO audit_log (workspace_id, actor_kind, action, target_type, target_id, details)
		SELECT workspace_id, 'system', 'invitation.email', 'invitation', $1::text, jsonb_build_object('status', $2::text)
		FROM changed
	`, m.id, result); err != nil {
		return fmt.Errorf("record the invitation email: %w", err)
	}
	if sendErr != nil && status != "delivered" {
		// Keep the SMTP step and reply code. They never include the address.
		return river.JobCancel(sendErr)
	}
	return nil
}

type invitationCleanupArgs struct{}

func (invitationCleanupArgs) Kind() string { return invitationCleanup }

type invitationCleanupWorker struct {
	river.WorkerDefaults[invitationCleanupArgs]
	db *pgxpool.Pool
}

func (w *invitationCleanupWorker) Work(ctx context.Context, _ *river.Job[invitationCleanupArgs]) error {
	_, err := expireInvitations(ctx, w.db)
	return err
}
