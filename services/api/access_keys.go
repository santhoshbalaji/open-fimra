package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/db"
)

const (
	// accessKeyPrefix starts every personal access key, so a leaked key is easy
	// to recognise in a file or a log.
	accessKeyPrefix = "fimra_"
	// accessKeyHintLength is how much of the key, after the prefix, the owner
	// sees again in the key list.
	accessKeyHintLength = 6
	// maxAccessKeys is how many unrevoked keys one person can hold.
	maxAccessKeys = 20
	// maxAccessKeyDays is the longest expiry a person can choose.
	maxAccessKeyDays = 730
	// accessKeyHeader is the header that carries a key without HTTP Basic.
	accessKeyHeader = "X-API-Key"
	// accessKeyUseInterval is how often one key's use is written to the
	// audit log and its last-used time.
	accessKeyUseInterval = time.Minute
)

// accessKey is one key as its owner sees it in the list. The key itself is
// only in the response that creates it.
type accessKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Hint       string     `json:"hint"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	Expired    bool       `json:"expired"`
	Key        string     `json:"key,omitempty"`
}

// newAccessKey returns a random key and the hash that is stored.
func newAccessKey() (key string, hash []byte, hint string, err error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, "", err
	}
	key = accessKeyPrefix + base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(key))
	return key, sum[:], key[len(accessKeyPrefix) : len(accessKeyPrefix)+accessKeyHintLength], nil
}

func hashAccessKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

// looksLikeAccessKey checks the form of a key before the database is asked.
func looksLikeAccessKey(key string) bool {
	secret, ok := strings.CutPrefix(key, accessKeyPrefix)
	if !ok || len(secret) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(secret)
	return err == nil
}

type accessKeyRequest struct {
	Name          string `json:"name"`
	ExpiresInDays *int   `json:"expiresInDays"`
}

func readAccessKeyRequest(body io.Reader) (accessKeyRequest, error) {
	var req accessKeyRequest
	decoder := json.NewDecoder(io.LimitReader(body, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || decoder.Decode(new(any)) != io.EOF {
		return req, errors.New("Send the key request as one JSON object.")
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || utf8.RuneCountInString(req.Name) > 100 {
		return req, errors.New("Give the key a name of up to 100 characters.")
	}
	if req.ExpiresInDays != nil && (*req.ExpiresInDays < 1 || *req.ExpiresInDays > maxAccessKeyDays) {
		return req, errors.New("expiresInDays must be from 1 to 730, or left out for a key that does not expire.")
	}
	return req, nil
}

func (a api) accessKeyError(w http.ResponseWriter, message string, err error) {
	a.logger.Error(message, "error", err)
	writeError(w, http.StatusInternalServerError, "Could not handle the access key.")
}

// accessKeys lists, creates and revokes the caller's own keys. It is only
// reachable with a signed-in bearer token, never with a key.
func (a api) accessKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	caller := r.Context().Value(identityKey{}).(identity)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	switch r.Method {
	case http.MethodGet:
		a.listAccessKeys(ctx, w, caller)
	case http.MethodPost:
		a.createAccessKey(ctx, w, r, caller)
	case http.MethodDelete:
		a.revokeAccessKey(ctx, w, r, caller)
	}
}

func (a api) listAccessKeys(ctx context.Context, w http.ResponseWriter, caller identity) {
	rows, err := a.pool.Query(ctx, `
		SELECT k.id::text, k.name, k.hint, k.created_at, k.expires_at, k.last_used_at,
		       COALESCE(k.expires_at <= now(), false)
		FROM personal_access_keys k JOIN users u ON u.id = k.user_id
		WHERE u.oidc_subject = $1 AND k.revoked_at IS NULL
		ORDER BY k.created_at DESC, k.id`, caller.Subject)
	if err != nil {
		a.accessKeyError(w, "Could not list access keys", err)
		return
	}
	keys, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (accessKey, error) {
		var k accessKey
		err := row.Scan(&k.ID, &k.Name, &k.Hint, &k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt, &k.Expired)
		return k, err
	})
	if err != nil {
		a.accessKeyError(w, "Could not list access keys", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": keys, "limit": maxAccessKeys})
}

func (a api) createAccessKey(ctx context.Context, w http.ResponseWriter, r *http.Request, caller identity) {
	req, err := readAccessKeyRequest(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	key, hash, hint, err := newAccessKey()
	if err != nil {
		a.accessKeyError(w, "Could not make an access key", err)
		return
	}
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		a.accessKeyError(w, "Could not start the access key transaction", err)
		return
	}
	defer tx.Rollback(ctx)
	// A person without a role can still make a key; it then shows nothing.
	// Saving the user also locks their row, so the key limit holds.
	userID, err := db.New(tx).UpsertUser(ctx, db.UpsertUserParams{
		OidcSubject: caller.Subject, Username: caller.Username, DisplayName: caller.Name,
	})
	if err != nil {
		a.accessKeyError(w, "Could not save the signed-in user", err)
		return
	}
	var held int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM personal_access_keys
		WHERE user_id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, userID).Scan(&held); err != nil {
		a.accessKeyError(w, "Could not count access keys", err)
		return
	}
	if held >= maxAccessKeys {
		writeError(w, http.StatusConflict, "You already have 20 keys. Revoke one you no longer use.")
		return
	}
	var expires pgtype.Interval
	if req.ExpiresInDays != nil {
		expires = pgtype.Interval{Days: int32(*req.ExpiresInDays), Valid: true}
	}
	var created accessKey
	err = tx.QueryRow(ctx, `
		INSERT INTO personal_access_keys (user_id, name, key_hash, hint, expires_at)
		VALUES ($1, $2, $3, $4, now() + $5::interval)
		RETURNING id::text, name, hint, created_at, expires_at, last_used_at`,
		userID, req.Name, hash, hint, expires).Scan(&created.ID, &created.Name, &created.Hint,
		&created.CreatedAt, &created.ExpiresAt, &created.LastUsedAt)
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO audit_log (actor_kind, actor_id, action, target_type, target_id, details, ip_address)
			VALUES ('user', $1, 'access_key.create', 'access_key', $2, jsonb_build_object('name', $3::text, 'expiresAt', $4::timestamptz), $5::inet)`,
			caller.Subject, created.ID, created.Name, created.ExpiresAt, clientAddress(r))
	}
	if err != nil {
		a.accessKeyError(w, "Could not save the access key", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		a.accessKeyError(w, "Could not save the access key", err)
		return
	}
	created.Key = key
	w.Header().Set("Location", "/api/v1/access-keys/"+created.ID)
	writeJSON(w, http.StatusCreated, created)
}

func (a api) revokeAccessKey(ctx context.Context, w http.ResponseWriter, r *http.Request, caller identity) {
	var id pgtype.UUID
	if err := id.Scan(chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusNotFound, "The access key was not found.")
		return
	}
	var revoked string
	err := a.pool.QueryRow(ctx, `
		WITH revoked AS (
		    UPDATE personal_access_keys k SET revoked_at = now() FROM users u
		    WHERE k.id = $1 AND k.user_id = u.id AND u.oidc_subject = $2 AND k.revoked_at IS NULL
		    RETURNING k.id, k.name
		), logged AS (
		    INSERT INTO audit_log (actor_kind, actor_id, action, target_type, target_id, details, ip_address)
		    SELECT 'user', $2, 'access_key.revoke', 'access_key', id::text, jsonb_build_object('name', name), $3::inet
		    FROM revoked
		)
		SELECT id::text FROM revoked`, id, caller.Subject, clientAddress(r)).Scan(&revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "The access key was not found.")
		return
	}
	if err != nil {
		a.accessKeyError(w, "Could not revoke the access key", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// clientAddress is the connecting address for the audit log, or nil.
func clientAddress(r *http.Request) *string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil {
		return nil
	}
	return &host
}

// gisAccessKey reads a personal access key from HTTP Basic (any user name,
// the key as the password) or from the X-API-Key header. The second result
// is false when the request has neither.
func gisAccessKey(r *http.Request) (string, bool) {
	if key := r.Header.Values(accessKeyHeader); len(key) > 0 {
		if len(key) != 1 {
			return "", true
		}
		return strings.TrimSpace(key[0]), true
	}
	if _, password, ok := r.BasicAuth(); ok {
		return password, true
	}
	return "", false
}

// gisChallenge asks a desktop GIS for a user name and password. Any user
// name is accepted; the password is the key.
const gisChallenge = `Basic realm="Open FIMRA GIS link", charset="UTF-8"`

// requireGIS accepts a signed-in bearer token, like every other route, or a
// personal access key. It is used only on the OGC API routes.
func (a api) requireGIS(auth authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		bearer := auth.require(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, found := gisAccessKey(r)
			if !found {
				if len(r.Header.Values("Authorization")) == 0 {
					w.Header().Set("WWW-Authenticate", gisChallenge)
					writeError(w, http.StatusUnauthorized, "Sign in, or use a personal access key as the password.")
					return
				}
				bearer.ServeHTTP(w, r)
				return
			}
			caller, ok, err := a.checkAccessKey(r, key)
			if err != nil {
				a.logger.Error("Could not check an access key", "error", err)
				writeError(w, http.StatusInternalServerError, "Could not check the access key.")
				return
			}
			if !ok {
				w.Header().Set("WWW-Authenticate", gisChallenge)
				writeError(w, http.StatusUnauthorized, "The access key is not valid. It may be revoked or expired.")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, caller)))
		})
	}
}

// checkAccessKey finds the key's owner. It records the key's use at most once
// a minute, in its last-used time and in the audit log.
func (a api) checkAccessKey(r *http.Request, key string) (identity, bool, error) {
	if !looksLikeAccessKey(key) {
		return identity{}, false, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var caller identity
	err := a.pool.QueryRow(ctx, `
		WITH owner_key AS (
		    SELECT k.id, u.oidc_subject, u.username, u.display_name
		    FROM personal_access_keys k JOIN users u ON u.id = k.user_id
		    WHERE k.key_hash = $1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > now())
		), used AS (
		    UPDATE personal_access_keys k SET last_used_at = now() FROM owner_key
		    WHERE k.id = owner_key.id AND k.revoked_at IS NULL
		      AND (k.last_used_at IS NULL OR k.last_used_at <= now() - $2::interval)
		    RETURNING k.id
		), logged AS (
		    INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details, ip_address)
		    SELECT (SELECT id FROM workspaces WHERE slug = $3), 'api_key', owner_key.id::text, 'access_key.use',
		           'access_key', owner_key.id::text, jsonb_build_object('owner', owner_key.oidc_subject, 'path', $4::text), $5::inet
		    FROM used JOIN owner_key ON owner_key.id = used.id
		)
		SELECT oidc_subject, username, display_name FROM owner_key`,
		hashAccessKey(key), pgtype.Interval{Microseconds: accessKeyUseInterval.Microseconds(), Valid: true},
		chi.URLParam(r, "slug"), r.URL.Path, clientAddress(r),
	).Scan(&caller.Subject, &caller.Username, &caller.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity{}, false, nil
	}
	if err != nil {
		return identity{}, false, err
	}
	return caller, true, nil
}
