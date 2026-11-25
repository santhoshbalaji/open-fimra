package main

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/db"
)

// Field phones (PLAN.md sections 5.4 and 6). The field app registers each
// sign-in on a phone as a device, with its push address. Owners list and
// revoke their devices. State administrators and division officers list the
// devices in their part of the hierarchy and can ask one to wipe itself.

// deviceHeader carries the device ID on every call from the field app.
const deviceHeader = "Fimra-Device"

// maxActiveDevices is how many devices one person can have registered at
// once. Registering another revokes the one unused for longest.
const maxActiveDevices = 10

var appVersionPattern = regexp.MustCompile(`^[0-9A-Za-z.+_-]{1,40}$`)

// deviceJSON is how a device row d is shown to its owner and administrators.
const deviceJSON = `jsonb_build_object('id',d.id::text,'platform',d.platform,'appVersion',d.app_version,
    'push',d.revoked_at IS NULL AND d.wipe_requested_at IS NULL AND d.push_endpoint IS NOT NULL,'pushProvider',d.push_provider,'registeredAt',d.registered_at,
    'lastSeenAt',d.last_seen_at,'status',CASE WHEN d.wiped_at IS NOT NULL THEN 'wiped'
        WHEN d.wipe_requested_at IS NOT NULL THEN 'wipe_pending' WHEN d.revoked_reason='signed_out' THEN 'signed_out'
        WHEN d.revoked_at IS NOT NULL THEN 'removed' ELSE 'active' END,
    'wipeRequestedAt',d.wipe_requested_at,'wipedAt',d.wiped_at)`

// deviceGate runs after sign-in on every core API call. It records the
// device's last contact and, for a wiped or removed device, answers with
// what the app must do instead of serving the call.
func (a api) deviceGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller := r.Context().Value(identityKey{}).(identity)
		var device pgtype.UUID
		if raw := r.Header.Get(deviceHeader); raw != "" {
			if err := device.Scan(raw); err != nil {
				writeError(w, http.StatusBadRequest, "The device ID must be a UUID.")
				return
			}
		}
		var session *string
		if caller.SessionID != "" {
			session = &caller.SessionID
		}
		if !device.Valid && session == nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		var action *string
		if err := a.reader.QueryRow(ctx, "SELECT device_contact($1, $2, $3)", caller.Subject, device, session).Scan(&action); err != nil {
			a.deviceError(w, err)
			return
		}
		if action != nil && writeDeviceAction(w, *action) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeDeviceAction answers a call from a wiped or removed device. The app
// reads the code; the status makes other clients end their session.
func writeDeviceAction(w http.ResponseWriter, action string) bool {
	w.Header().Set("Cache-Control", "no-store")
	switch action {
	case "wipe":
		writeJSON(w, http.StatusUnauthorized, map[string]string{"code": "device_wiped",
			"error": "An administrator asked this phone to delete its field data. Sign in again to keep working."})
	case "revoked":
		writeJSON(w, http.StatusUnauthorized, map[string]string{"code": "device_revoked",
			"error": "This phone was removed from your account. Sign in again to add it back."})
	default:
		return false
	}
	return true
}

type devicePush struct {
	Provider  string `json:"provider"`
	Endpoint  string `json:"endpoint"`
	PublicKey string `json:"publicKey,omitempty"`
	Auth      string `json:"auth,omitempty"`
}

type deviceRegistration struct {
	Platform   string      `json:"platform"`
	AppVersion string      `json:"appVersion"`
	Push       *devicePush `json:"push"`
}

// pushColumns are the checked push settings to store, or all nil.
type pushColumns struct {
	provider, endpoint *string
	publicKey, auth    []byte
}

// checkPush validates a registration's push address. A bad key is a client
// error. A push service or server this API does not use is not: the device
// is registered without push and the app is told why.
func (a api) checkPush(p *devicePush) (pushColumns, string, bool) {
	if p == nil {
		return pushColumns{}, "", true
	}
	switch p.Provider {
	case "unifiedpush":
		key, keyErr := decodeBase64URL(p.PublicKey)
		auth, authErr := decodeBase64URL(p.Auth)
		if keyErr != nil || authErr != nil || len(auth) != 16 {
			return pushColumns{}, "", false
		}
		if _, err := ecdh.P256().NewPublicKey(key); err != nil {
			return pushColumns{}, "", false
		}
		if a.push.web == nil {
			return pushColumns{}, "This server does not send notifications through UnifiedPush.", true
		}
		if !a.push.allowedEndpoint(p.Endpoint) {
			return pushColumns{}, "Set your push app to use " + strings.Join(a.push.servers, " or ") +
				". Notifications from other push servers are not accepted.", true
		}
		return pushColumns{provider: &p.Provider, endpoint: &p.Endpoint, publicKey: key, auth: auth}, "", true
	case "fcm":
		if p.Endpoint == "" || len(p.Endpoint) > 4096 || strings.ContainsFunc(p.Endpoint, func(r rune) bool { return r <= ' ' || r > '~' }) ||
			p.PublicKey != "" || p.Auth != "" {
			return pushColumns{}, "", false
		}
		if a.push.fcm == nil {
			return pushColumns{}, "This server does not send notifications through Google's service.", true
		}
		return pushColumns{provider: &p.Provider, endpoint: &p.Endpoint}, "", true
	}
	return pushColumns{}, "", false
}

func decodeBase64URL(value string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
}

// devices serves the caller's own devices: register (PUT), list (GET) and
// revoke (DELETE).
func (a api) devices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	caller := r.Context().Value(identityKey{}).(identity)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if r.Method == http.MethodGet {
		current, _ := currentDevice(r)
		var list json.RawMessage
		err := a.reader.QueryRow(ctx, `
SELECT COALESCE(jsonb_agg(`+deviceJSON+` || jsonb_build_object('current', d.id::text = $2)
    ORDER BY d.revoked_at IS NOT NULL OR d.wipe_requested_at IS NOT NULL, d.last_seen_at DESC), '[]')
FROM (SELECT d.* FROM devices d JOIN users u ON u.id=d.user_id WHERE u.oidc_subject=$1
      ORDER BY d.last_seen_at DESC LIMIT 50) d`, caller.Subject, current).Scan(&list)
		if err != nil {
			a.deviceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": list, "pushProviders": a.push.providers()})
		return
	}

	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusNotFound, "The device was not found.")
		return
	}
	if r.Method == http.MethodDelete {
		reason := "owner"
		if r.URL.Query().Get("reason") == "sign_out" {
			reason = "signed_out"
		}
		a.revokeDevice(ctx, w, r, caller, id, reason)
		return
	}
	a.registerDevice(ctx, w, r, caller, id)
}

func currentDevice(r *http.Request) (string, bool) {
	var id pgtype.UUID
	if err := id.Scan(r.Header.Get(deviceHeader)); err != nil {
		return "", false
	}
	value, _ := id.Value()
	return value.(string), true
}

func (a api) registerDevice(ctx context.Context, w http.ResponseWriter, r *http.Request, caller identity, id pgtype.UUID) {
	var req deviceRegistration
	if !decodeJSON(w, r, 16<<10, &req) {
		return
	}
	if req.Platform != "android" && req.Platform != "ios" && req.Platform != "web" {
		writeError(w, http.StatusBadRequest, "The platform must be android, ios or web.")
		return
	}
	if !appVersionPattern.MatchString(req.AppVersion) {
		writeError(w, http.StatusBadRequest, "The app version must be up to 40 letters, digits and . + _ -")
		return
	}
	push, problem, ok := a.checkPush(req.Push)
	if !ok {
		writeError(w, http.StatusBadRequest, "The push address or its keys are not valid.")
		return
	}
	var session *string
	if caller.SessionID != "" {
		session = &caller.SessionID
	}

	var device json.RawMessage
	var action string
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		// Saving the user locks their row, so the device limit holds.
		userID, err := db.New(tx).UpsertUser(ctx, db.UpsertUserParams{
			OidcSubject: caller.Subject, Username: caller.Username, DisplayName: caller.Name,
		})
		if err != nil {
			return err
		}
		var owner pgtype.UUID
		var revoked, wipe bool
		err = tx.QueryRow(ctx, `SELECT user_id, revoked_at IS NOT NULL, wipe_requested_at IS NOT NULL
FROM devices WHERE id=$1 FOR UPDATE`, id).Scan(&owner, &revoked, &wipe)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case owner != userID:
			action = "other"
			return nil
		case wipe:
			action = "wipe"
			return nil
		case revoked:
			action = "revoked"
			return nil
		}
		// A push address moves with the app: another device that had it is
		// replaced, so one phone never receives someone else's alerts.
		if push.endpoint != nil {
			if _, err := tx.Exec(ctx, `
WITH replaced AS (
    UPDATE devices SET revoked_at=now(), revoked_reason='replaced'
    WHERE push_endpoint=$1 AND id<>$2 AND revoked_at IS NULL AND wipe_requested_at IS NULL RETURNING id
)
INSERT INTO audit_log(actor_kind,actor_id,action,target_type,target_id,details)
SELECT 'user',$3,'device.revoke','device',id::text,'{"reason":"replaced"}' FROM replaced`,
				*push.endpoint, id, caller.Subject); err != nil {
				return err
			}
		}
		var inserted bool
		err = tx.QueryRow(ctx, `
INSERT INTO devices AS d (id,user_id,platform,app_version,push_provider,push_endpoint,push_public_key,push_auth,session_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (id) DO UPDATE SET platform=EXCLUDED.platform, app_version=EXCLUDED.app_version,
    push_provider=EXCLUDED.push_provider, push_endpoint=EXCLUDED.push_endpoint,
    push_public_key=EXCLUDED.push_public_key, push_auth=EXCLUDED.push_auth,
    session_id=COALESCE(EXCLUDED.session_id, d.session_id), last_seen_at=now()
RETURNING `+deviceJSON+` || jsonb_build_object('current', true), xmax=0`,
			id, userID, req.Platform, req.AppVersion, push.provider, push.endpoint, push.publicKey, push.auth, session).
			Scan(&device, &inserted)
		if err != nil {
			return err
		}
		if !inserted {
			return nil
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO audit_log(actor_kind,actor_id,action,target_type,target_id,details,ip_address)
VALUES ('user',$1,'device.register','device',$2::uuid::text,
    jsonb_build_object('platform',$3::text,'appVersion',$4::text,'push',$5::text),$6::inet)`,
			caller.Subject, id, req.Platform, req.AppVersion, push.provider, clientAddress(r)); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
WITH extra AS (
    SELECT id FROM devices WHERE user_id=$1 AND revoked_at IS NULL AND wipe_requested_at IS NULL
    ORDER BY last_seen_at DESC, registered_at DESC OFFSET $2
), replaced AS (
    UPDATE devices d SET revoked_at=now(), revoked_reason='replaced' FROM extra WHERE d.id=extra.id RETURNING d.id
)
INSERT INTO audit_log(actor_kind,actor_id,action,target_type,target_id,details)
SELECT 'user',$3,'device.revoke','device',id::text,'{"reason":"replaced"}' FROM replaced`,
			userID, maxActiveDevices, caller.Subject)
		return err
	})
	if err != nil {
		a.deviceError(w, err)
		return
	}
	if action == "other" {
		writeError(w, http.StatusNotFound, "The device was not found.")
		return
	}
	if writeDeviceAction(w, action) {
		return
	}
	reply := map[string]any{"device": device, "pushProviders": a.push.providers()}
	if problem != "" {
		reply["pushProblem"] = problem
	}
	writeJSON(w, http.StatusOK, reply)
}

func (a api) revokeDevice(ctx context.Context, w http.ResponseWriter, r *http.Request, caller identity, id pgtype.UUID, reason string) {
	var found, changed bool
	err := a.reader.QueryRow(ctx, `
WITH mine AS (
    SELECT d.id, d.revoked_at IS NULL AS active FROM devices d JOIN users u ON u.id=d.user_id
    WHERE d.id=$1 AND u.oidc_subject=$2
), revoked AS (
    UPDATE devices d SET revoked_at=now(), revoked_reason=$3 FROM mine
    WHERE d.id=mine.id AND mine.active RETURNING d.id
), logged AS (
    INSERT INTO audit_log(actor_kind,actor_id,action,target_type,target_id,details,ip_address)
    SELECT 'user',$2,'device.revoke','device',id::text,jsonb_build_object('reason',$3::text),$4::inet FROM revoked
)
SELECT EXISTS (SELECT FROM mine), EXISTS (SELECT FROM revoked)`, id, caller.Subject, reason, clientAddress(r)).Scan(&found, &changed)
	if err != nil {
		a.deviceError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "The device was not found.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deviceWiped records that the app deleted its data after a wipe signal.
// This one call is allowed from a wiped device.
func (a api) deviceWiped(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	caller := r.Context().Value(identityKey{}).(identity)
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusNotFound, "The device was not found.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var found bool
	err := a.reader.QueryRow(ctx, `
WITH mine AS (
    SELECT d.id FROM devices d JOIN users u ON u.id=d.user_id
    WHERE d.id=$1 AND u.oidc_subject=$2 AND d.wipe_requested_at IS NOT NULL
), confirmed AS (
    UPDATE devices d SET wiped_at=now(), wipe_signalled_at=COALESCE(d.wipe_signalled_at, now())
    FROM mine WHERE d.id=mine.id AND d.wiped_at IS NULL RETURNING d.id
), logged AS (
    INSERT INTO audit_log(actor_kind,actor_id,action,target_type,target_id,ip_address)
    SELECT 'user',$2,'device.wipe_confirm','device',id::text,$3::inet FROM confirmed
)
SELECT EXISTS (SELECT FROM mine)`, id, caller.Subject, clientAddress(r)).Scan(&found)
	if err != nil {
		a.deviceError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "The device was not found.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// workspaceDevices lists the devices an administrator manages in a workspace.
func (a api) workspaceDevices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	caller := r.Context().Value(identityKey{}).(identity)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var list json.RawMessage
	err := a.reader.QueryRow(ctx, `
WITH admin AS (
    SELECT EXISTS (SELECT FROM alert_grants g WHERE g.oidc_subject=$1 AND g.slug=$2
        AND (g.role_slug='state_admin' OR (g.role_slug='division_officer' AND g.can_review))) AS ok
), page AS (
    SELECT d.*, u.username, u.display_name, requester.display_name AS requester
    FROM manageable_devices($1, $2) m JOIN devices d ON d.id=m.device_id JOIN users u ON u.id=d.user_id
    LEFT JOIN users requester ON requester.id=d.wipe_requested_by
    ORDER BY d.revoked_at IS NOT NULL OR d.wiped_at IS NOT NULL, d.last_seen_at DESC LIMIT 500
)
SELECT CASE WHEN admin.ok THEN COALESCE((SELECT jsonb_agg(`+deviceJSON+` || jsonb_build_object(
    'owner', jsonb_build_object('username', d.username, 'name', d.display_name), 'wipeRequestedBy', d.requester)
    ORDER BY d.revoked_at IS NOT NULL OR d.wiped_at IS NOT NULL, d.last_seen_at DESC) FROM page d), '[]') END
FROM admin`, caller.Subject, chi.URLParam(r, "slug")).Scan(&list)
	if err != nil {
		a.deviceError(w, err)
		return
	}
	if list == nil {
		writeError(w, http.StatusForbidden, "A state administrator or division officer role is required.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

// wipeDevice asks a device to delete its field data and tokens at its next
// contact. Administrators wipe devices in their part of the hierarchy, from
// /workspaces/{slug}/devices/{id}/wipe; owners wipe their own, from
// /devices/{id}/wipe. A push message prompts the phone to call in soon.
func (a api) wipeDevice(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	caller := r.Context().Value(identityKey{}).(identity)
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusNotFound, "The device was not found.")
		return
	}
	slug := chi.URLParam(r, "slug")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var device json.RawMessage
	err := pgx.BeginFunc(ctx, a.reader, func(tx pgx.Tx) error {
		var workspaceID *string
		var found bool
		if slug == "" {
			err := tx.QueryRow(ctx, `SELECT true FROM devices d JOIN users u ON u.id=d.user_id
WHERE d.id=$1 AND u.oidc_subject=$2 FOR UPDATE OF d`, id, caller.Subject).Scan(&found)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		} else {
			err := tx.QueryRow(ctx, `SELECT m.workspace_id::text FROM manageable_devices($2, $3) m
JOIN devices d ON d.id=m.device_id WHERE m.device_id=$1 FOR UPDATE OF d`, id, caller.Subject, slug).Scan(&workspaceID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			found = workspaceID != nil
		}
		if !found {
			return pgx.ErrNoRows
		}
		var requested bool
		if err := tx.QueryRow(ctx, `
UPDATE devices SET wipe_requested_at=now(), wipe_requested_by=(SELECT id FROM users WHERE oidc_subject=$2)
WHERE id=$1 AND wipe_requested_at IS NULL RETURNING true`, id, caller.Subject).Scan(&requested); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if requested {
			if _, err := tx.Exec(ctx, `
INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,ip_address)
VALUES ($1::uuid,'user',$2,'device.wipe_request','device',$3::uuid::text,$4::inet)`,
				workspaceID, caller.Subject, id, clientAddress(r)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
INSERT INTO push_messages(device_id,kind) SELECT id,'device_check' FROM devices
WHERE id=$1 AND push_endpoint IS NOT NULL`, id); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `SELECT `+deviceJSON+` FROM devices d WHERE d.id=$1`, id).Scan(&device)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "The device was not found.")
		return
	}
	if err != nil {
		a.deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]json.RawMessage{"device": device})
}

func (a api) deviceError(w http.ResponseWriter, err error) {
	a.logger.Error("Could not process devices", "error", err)
	writeError(w, http.StatusInternalServerError, "Could not load or save the device.")
}

func withPush(settings pushSettings) func(*api) {
	return func(a *api) { a.push = settings }
}
