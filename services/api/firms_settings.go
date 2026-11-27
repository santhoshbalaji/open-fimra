package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

// FIRMS map keys are 32 hexadecimal characters. Accept a little more so a
// future key format still fits, but nothing that could break a URL path.
var firmsKeyPattern = regexp.MustCompile(`^[A-Za-z0-9]{16,64}$`)

const firmsTestProduct = "VIIRS_NOAA20_NRT"

// firmsKeys finds the map key for a workspace: the encrypted key saved in its
// settings first, then FIMRA_FIRMS_MAP_KEY for installations set up before
// settings existed. Every client for the same key shares one rate limiter, so
// polls, backfills and connection tests stay inside NASA's limit together.
type firmsKeys struct {
	db        rowQuerier
	cipher    *settingsCipher
	fallback  string
	transport http.RoundTripper

	pollSettings firmsPollSettings

	mu       sync.Mutex
	limiters map[[32]byte]*firms.Limiter
}

func newFIRMSKeys(db rowQuerier, c *settingsCipher, fallback string, transport http.RoundTripper) *firmsKeys {
	return &firmsKeys{
		db: db, cipher: c, fallback: fallback, transport: transport,
		limiters:     map[[32]byte]*firms.Limiter{},
		pollSettings: firmsPollSettings{interval: defaultFIRMSPollInterval, products: defaultFIRMSProducts()},
	}
}

// Sources of a workspace's FIRMS key.
const (
	keyFromSettings    = "settings"
	keyFromEnvironment = "environment"
	keyMissing         = "none"
)

func (k *firmsKeys) key(ctx context.Context, workspaceID string) (string, string, error) {
	return k.keyFrom(ctx, k.db, workspaceID)
}

func (k *firmsKeys) keyFrom(ctx context.Context, db rowQuerier, workspaceID string) (string, string, error) {
	value, found, err := loadSecret(ctx, db, k.cipher, workspaceID, firmsSecret)
	if found || err != nil {
		return value, keyFromSettings, err
	}
	if k.fallback != "" {
		return k.fallback, keyFromEnvironment, nil
	}
	return "", keyMissing, nil
}

func (k *firmsKeys) client(key string) firms.Area {
	digest := sha256.Sum256([]byte(key))
	k.mu.Lock()
	defer k.mu.Unlock()
	limiter := k.limiters[digest]
	if limiter == nil {
		limiter = firms.NewLimiter(firms.TransactionLimit, firms.TransactionWindow)
		k.limiters[digest] = limiter
	}
	return firms.NewArea(key, k.transport).WithLimiter(limiter)
}

// area returns a FIRMS client for the workspace, or ok = false without a key.
func (k *firmsKeys) area(ctx context.Context, workspaceID string) (firms.Area, bool, error) {
	key, source, err := k.key(ctx, workspaceID)
	if err != nil || source == keyMissing {
		return firms.Area{}, false, err
	}
	return k.client(key), true, nil
}

type firmsStatus struct {
	secretStatus
	Source     string `json:"source"`
	Encryption bool   `json:"encryption"`
}

func (a api) firmsStatus(ctx context.Context, workspaceID string) (firmsStatus, error) {
	saved, err := readSecretStatus(ctx, a.pool, workspaceID, firmsSecret)
	status := firmsStatus{secretStatus: saved, Source: keyMissing, Encryption: a.secrets != nil}
	switch {
	case saved.Set:
		status.Source = keyFromSettings
	case a.firms != nil && a.firms.fallback != "":
		status.Source = keyFromEnvironment
	}
	return status, err
}

func (a api) settingsError(w http.ResponseWriter, err error) {
	a.logger.Error("Could not handle the workspace settings", "error", err)
	writeError(w, http.StatusInternalServerError, "Could not handle the workspace settings.")
}

// firmsSettings serves GET, PUT and DELETE for the workspace's FIRMS key.
func (a api) firmsSettings(w http.ResponseWriter, r *http.Request) {
	workspace, ok := a.layerAdmin(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	caller := ctx.Value(identityKey{}).(identity)
	switch r.Method {
	case http.MethodPut:
		if a.secrets == nil {
			writeError(w, http.StatusServiceUnavailable,
				"Encrypted settings are off. Ask the system operator to set FIMRA_SETTINGS_KEY.")
			return
		}
		var body struct {
			MapKey string `json:"mapKey"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil || decoder.Decode(new(any)) != io.EOF {
			writeError(w, http.StatusBadRequest, "Send the map key as JSON.")
			return
		}
		key := strings.TrimSpace(body.MapKey)
		if !firmsKeyPattern.MatchString(key) {
			writeError(w, http.StatusBadRequest, "A FIRMS map key has 16 to 64 letters and numbers.")
			return
		}
		err := pgx.BeginFunc(ctx, a.pool, func(tx pgx.Tx) error {
			return saveSecret(ctx, tx, a.secrets, workspace.String(), firmsSecret, key, caller.Subject)
		})
		if err != nil {
			a.settingsError(w, err)
			return
		}
	case http.MethodDelete:
		_, err := a.pool.Exec(ctx, `
			WITH removed AS (
				DELETE FROM workspace_settings_secrets WHERE workspace_id = $1 AND name = $3 RETURNING workspace_id
			)
			INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id)
			SELECT workspace_id, 'user', $2, 'settings.secret_delete', 'workspace_secret', $3 FROM removed
		`, workspace, caller.Subject, firmsSecret)
		if err != nil {
			a.settingsError(w, err)
			return
		}
	}
	status, err := a.firmsStatus(ctx, workspace.String())
	if err != nil {
		a.settingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

type firmsTestResult struct {
	OK      bool   `json:"ok"`
	Source  string `json:"source"`
	Message string `json:"message"`
}

// firmsTest asks FIRMS which days it holds for one product, with the key the
// poller would use. Failures are reported without the key.
func (a api) firmsTest(w http.ResponseWriter, r *http.Request) {
	workspace, ok := a.layerAdmin(w, r)
	if !ok {
		return
	}
	if a.firms == nil {
		writeError(w, http.StatusServiceUnavailable, "FIRMS connections are not configured in this service.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	key, source, err := a.firms.key(ctx, workspace.String())
	if err != nil {
		a.logger.Error("Could not read the FIRMS key", "workspace", workspace.String(), "error", err)
		writeJSON(w, http.StatusOK, firmsTestResult{Source: source,
			Message: "The saved key could not be read. Ask the system operator to check the settings key."})
		return
	}
	if source == keyMissing {
		writeJSON(w, http.StatusOK, firmsTestResult{Source: source, Message: "Enter a FIRMS map key first."})
		return
	}
	result := testFIRMSConnection(ctx, a.firms.client(key))
	result.Source = source
	if !result.OK {
		a.logger.Warn("FIRMS connection test failed", "workspace", workspace.String(), "reason", result.Message)
	}
	caller := ctx.Value(identityKey{}).(identity)
	if _, err := a.pool.Exec(ctx, `
		INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
		VALUES ($1, 'user', $2, 'settings.firms_test', 'workspace_secret', $3, jsonb_build_object('ok', $4::boolean))
	`, workspace, caller.Subject, firmsSecret, result.OK); err != nil {
		a.settingsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func testFIRMSConnection(ctx context.Context, area firms.Area) firmsTestResult {
	first, last, err := area.Availability(ctx, firmsTestProduct)
	if err != nil {
		// The area client removes the key from every error it returns.
		return firmsTestResult{Message: "FIRMS did not accept the request: " + err.Error()}
	}
	if last.Before(firms.Day(time.Now()).AddDate(0, 0, -1)) {
		return firmsTestResult{Message: fmt.Sprintf("FIRMS answered, but %s availability is stale. The latest date is %s.",
			firmsTestProduct, last.Format(time.DateOnly))}
	}
	return firmsTestResult{OK: true, Message: fmt.Sprintf("FIRMS answered. %s has data from %s to %s.",
		firmsTestProduct, first.Format(time.DateOnly), last.Format(time.DateOnly))}
}

func (k *firmsKeys) liveProducts() []string {
	if k == nil {
		return defaultFIRMSProducts()
	}
	return k.pollSettings.products
}

// backfillClient returns the backfill fetchers for a workspace.
func (k *firmsKeys) backfillClient(ctx context.Context, workspaceID string) (fetchRange, availableDays, error) {
	area, ok, err := k.area(ctx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, errors.New("no FIRMS map key is saved for this workspace and FIMRA_FIRMS_MAP_KEY is empty")
	}
	return area.FetchRange, area.Availability, nil
}
