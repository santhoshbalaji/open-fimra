package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/db"
)

type api struct {
	pool         *pgxpool.Pool
	queries      *db.Queries
	logger       *slog.Logger
	reader       ingestDB
	uploadDir    string
	exports      exportSettings
	attachments  attachmentSettings
	feedDB       feedDB
	jobs         *river.Client[pgx.Tx]
	emailEnabled bool
	setup        *setupGuard
	secrets      *settingsCipher
	firms        *firmsKeys
	push         pushSettings
	// now is the clock for feed health. It is nil outside tests.
	now func() time.Time
}

func newHandler(pool *pgxpool.Pool, auth authenticator, logger *slog.Logger, origins, uploadDir string, jobs *river.Client[pgx.Tx], options ...func(*api)) http.Handler {
	a := api{pool: pool, queries: db.New(pool), logger: logger, reader: pool, uploadDir: uploadDir, feedDB: pool, jobs: jobs, emailEnabled: auth.emailEnabled}
	for _, option := range options {
		option(&a)
	}
	return a.handler(auth, origins)
}

func (a api) handler(auth authenticator, origins string) http.Handler {
	router := chi.NewRouter()
	router.Use(allowOrigins(origins))
	router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	router.Get("/readyz", a.ready)
	// Every signed-in core API call also checks the calling device, so a
	// wiped phone is told to wipe on its next contact.
	authed := router.With(auth.require, a.deviceGate)
	authed.Get("/api/v1/me", a.me)
	authed.Get("/api/v1/setup", a.setupInfo)
	authed.Post("/api/v1/setup/bootstrap", a.bootstrap)
	authed.Get("/api/v1/workspaces/{slug}/setup", a.setupState)
	authed.Post("/api/v1/workspaces/{slug}/setup/hierarchy", a.setupHierarchy)
	authed.Post("/api/v1/workspaces/{slug}/setup/steps/{step}", a.setupStepUpdate)
	authed.Post("/api/v1/workspaces/{slug}/setup/finish", a.setupFinish)
	authed.Post("/api/v1/workspaces/{slug}/setup/reopen", a.setupFinish)
	authed.Get("/api/v1/workspaces/{slug}/setup/units", a.setupUnits)
	authed.Get("/api/v1/workspaces/{slug}/settings/firms", a.firmsSettings)
	authed.Put("/api/v1/workspaces/{slug}/settings/firms", a.firmsSettings)
	authed.Delete("/api/v1/workspaces/{slug}/settings/firms", a.firmsSettings)
	authed.Post("/api/v1/workspaces/{slug}/settings/firms/test", a.firmsTest)
	authed.Get("/api/v1/workspaces/{slug}/invitations", a.invitations)
	authed.Post("/api/v1/workspaces/{slug}/invitations", a.invitations)
	authed.Delete("/api/v1/workspaces/{slug}/invitations/{id}", a.invitations)
	authed.Get("/api/v1/notifications", a.notifications)
	authed.Post("/api/v1/notifications/{id}/read", a.notifications)
	authed.Get("/api/v1/notifications/preferences", a.notificationPreferences)
	authed.Post("/api/v1/notifications/preferences", a.notificationPreferences)
	authed.Get("/api/v1/workspaces/{slug}/notification-rules", a.notificationRules)
	authed.Post("/api/v1/workspaces/{slug}/notification-rules", a.notificationRules)
	authed.Get("/api/v1/workspaces/{slug}/alerts", a.alerts)
	authed.Get("/api/v1/workspaces/{slug}/alerts/{id}", a.alerts)
	authed.Get("/api/v1/workspaces/{slug}/alerts/{id}/history", a.workflow)
	authed.Post("/api/v1/workspaces/{slug}/alerts/{id}/steps", a.workflow)
	authed.Get("/api/v1/workspaces/{slug}/incidents", a.incidents)
	authed.Get("/api/v1/workspaces/{slug}/incidents/{id}", a.incidents)
	authed.Get("/api/v1/workspaces/{slug}/incidents/{id}/history", a.incidentWorkflow)
	authed.Post("/api/v1/workspaces/{slug}/incidents/{id}/steps", a.incidentWorkflow)
	authed.Get("/api/v1/workspaces/{slug}/settings/incidents", a.incidentSettings)
	authed.Put("/api/v1/workspaces/{slug}/settings/incidents", a.incidentSettings)
	authed.Get("/api/v1/workspaces/{slug}/alerts/{id}/forms", a.alertForms)
	authed.Get("/api/v1/workspaces/{slug}/alerts/{id}/submissions", a.submissions)
	authed.Post("/api/v1/workspaces/{slug}/alerts/{id}/submissions", a.submissions)
	authed.Put("/api/v1/workspaces/{slug}/alerts/{id}/attachments/{attachment}", a.uploadAttachment)
	authed.Get("/api/v1/workspaces/{slug}/alerts/{id}/attachments/{attachment}", a.attachmentReceipt)
	authed.Get("/api/v1/workspaces/{slug}/offline-reference", a.offlineReference)
	authed.Get("/api/v1/workspaces/{slug}/attachments/{attachment}", a.downloadAttachment)
	authed.Get("/api/v1/workspaces/{slug}/submissions", a.submissionList)
	authed.Get("/api/v1/workspaces/{slug}/submissions/{submission}", a.submission)
	authed.Post("/api/v1/workspaces/{slug}/submissions/{submission}/approval", a.approve)
	authed.Get("/api/v1/workspaces/{slug}/forms", a.forms)
	authed.Post("/api/v1/workspaces/{slug}/forms", a.forms)
	authed.Get("/api/v1/workspaces/{slug}/forms/{form}/versions/{version}", a.forms)
	authed.Put("/api/v1/workspaces/{slug}/forms/{form}/versions/{version}", a.forms)
	authed.Post("/api/v1/workspaces/{slug}/forms/{form}/versions/{version}/publish", a.publishForm)
	authed.Put("/api/v1/workspaces/{slug}/forms/{form}/assignments/{step}", a.formAssignment)
	authed.Delete("/api/v1/workspaces/{slug}/forms/{form}/assignments/{step}", a.formAssignment)
	authed.Get("/api/v1/workspaces/{slug}/starter-pack", a.starterPack)
	authed.Post("/api/v1/workspaces/{slug}/starter-pack", a.starterPack)
	authed.Post("/api/v1/workspaces/{slug}/manual-fire-reports", a.manualFireReport)
	authed.Get("/api/v1/workspaces/{slug}", a.workspaceMap)
	authed.Get("/api/v1/workspaces/{slug}/tiles/reference/{z}/{x}/{y}", a.referenceTile)
	authed.Get("/api/v1/workspaces/{slug}/tiles/detections/{z}/{x}/{y}", a.detectionTile)
	authed.Get("/api/v1/workspaces/{slug}/layer-imports", a.layerCatalog)
	authed.Post("/api/v1/workspaces/{slug}/layer-imports", a.uploadLayer)
	authed.Get("/api/v1/workspaces/{slug}/layer-imports/{version}", a.layerDraft)
	authed.Get("/api/v1/workspaces/{slug}/summary", a.summary)
	authed.Get("/api/v1/workspaces/{slug}/feeds", a.feeds)
	authed.Get("/api/v1/workspaces/{slug}/situation-report", a.situationReport)
	authed.Get("/api/v1/workspaces/{slug}/situation-report/units", a.reportUnits)
	authed.Post("/api/v1/workspaces/{slug}/situation-reports", a.createSituationReport)
	authed.Get("/api/v1/workspaces/{slug}/situation-reports/{id}", a.situationReportStatus)
	authed.Get("/api/v1/workspaces/{slug}/situation-reports/{id}/file", a.situationReportFile)
	authed.Get("/api/v1/workspaces/{slug}/layer-imports/{version}/preview", a.layerPreview)
	authed.Get("/api/v1/workspaces/{slug}/layer-imports/{version}/map", a.layerPreviewMap)
	authed.Post("/api/v1/workspaces/{slug}/layer-imports/{version}/publish", a.layerPublish)
	authed.Get("/api/v1/workspaces/{slug}/exports", a.listExports)
	authed.Post("/api/v1/workspaces/{slug}/exports", a.createExport)
	authed.Get("/api/v1/workspaces/{slug}/exports/{id}", a.exportStatus)
	authed.Get("/api/v1/workspaces/{slug}/exports/{id}/file", a.exportFile)
	authed.Get("/api/v1/devices", a.devices)
	authed.Put("/api/v1/devices/{id}", a.devices)
	authed.Delete("/api/v1/devices/{id}", a.devices)
	authed.Post("/api/v1/devices/{id}/wipe", a.wipeDevice)
	// A wiped phone reports that it deleted its data, so this call skips the gate.
	router.With(auth.require).Post("/api/v1/devices/{id}/wiped", a.deviceWiped)
	authed.Get("/api/v1/workspaces/{slug}/devices", a.workspaceDevices)
	authed.Post("/api/v1/workspaces/{slug}/devices/{id}/wipe", a.wipeDevice)
	authed.Get("/api/v1/access-keys", a.accessKeys)
	authed.Post("/api/v1/access-keys", a.accessKeys)
	authed.Delete("/api/v1/access-keys/{id}", a.accessKeys)
	// The GIS link is the only place a personal access key is accepted.
	router.Route("/ogc/workspaces/{slug}", func(gis chi.Router) {
		gis.Use(a.requireGIS(auth))
		gis.Get("/", a.ogcLanding)
		gis.Get("/conformance", a.ogcConformance)
		gis.Get("/api", a.ogcAPI)
		gis.Get("/collections", a.ogcCollections)
		gis.Get("/collections/{collection}", a.ogcCollection)
		gis.Get("/collections/{collection}/items", a.ogcItems)
		gis.Get("/collections/{collection}/items/{feature}", a.ogcItem)
	})
	return router
}

func (a api) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := a.pool.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "The database is unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type workspace struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type unit struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	LevelSlug string `json:"levelSlug"`
	Path      string `json:"path"`
	// Bounds is the unit's extent as [west, south, east, north] in degrees,
	// or null when the unit has no geometry. The field app fits its map to it.
	Bounds []float64 `json:"bounds"`
}

type roleAssignment struct {
	Workspace workspace `json:"workspace"`
	Hierarchy string    `json:"hierarchy"`
	Role      string    `json:"role"`
	Unit      unit      `json:"unit"`
	BeatCount int64     `json:"beatCount"`
}

type meResponse struct {
	Subject         string           `json:"subject"`
	Username        string           `json:"username"`
	RoleAssignments []roleAssignment `json:"roleAssignments"`
	Setup           meSetup          `json:"setup"`
}

// meSetup says which first-run screens the web app should offer the caller.
type meSetup struct {
	BootstrapAvailable bool     `json:"bootstrapAvailable"`
	Unfinished         []string `json:"unfinished"`
	Claimed            int      `json:"claimedInvitations"`
}

func withSetup(guard *setupGuard, secrets *settingsCipher, keys *firmsKeys) func(*api) {
	return func(a *api) { a.setup, a.secrets, a.firms = guard, secrets, keys }
}

func (a api) me(w http.ResponseWriter, r *http.Request) {
	caller := r.Context().Value(identityKey{}).(identity)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	userID, err := a.queries.UpsertUser(ctx, db.UpsertUserParams{
		OidcSubject: caller.Subject,
		Username:    caller.Username,
		DisplayName: caller.Name,
	})
	if err != nil {
		a.logger.Error("Could not save the signed-in user", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load the signed-in user.")
		return
	}

	if a.emailEnabled {
		if err := saveNotificationEmail(ctx, a.reader, caller); err != nil {
			a.notificationError(w, err)
			return
		}
	}

	var setup meSetup
	setup.Claimed, err = claimInvitations(ctx, a.reader, caller, userID)
	if err == nil {
		setup.Unfinished, err = unfinishedSetups(ctx, a.pool, caller.Subject)
	}
	if err == nil {
		setup.BootstrapAvailable, err = installOpen(ctx, a.pool)
		setup.BootstrapAvailable = setup.BootstrapAvailable && a.setup != nil
	}
	if err != nil {
		a.logger.Error("Could not read first-run setup for the signed-in user", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load the signed-in user.")
		return
	}

	rows, err := a.queries.ListRoleAssignments(ctx, userID)
	if err != nil {
		a.logger.Error("Could not read the user's role assignments", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load the signed-in user.")
		return
	}

	assignments := make([]roleAssignment, 0, len(rows))
	for _, row := range rows {
		assignments = append(assignments, roleAssignment{
			Workspace: workspace{Slug: row.WorkspaceSlug, Name: row.WorkspaceName},
			Hierarchy: row.HierarchySlug,
			Role:      row.RoleSlug,
			Unit: unit{Code: row.UnitCode, Name: row.UnitName, LevelSlug: row.LevelSlug, Path: row.UnitPath,
				Bounds: row.Bounds},
			BeatCount: row.BeatCount,
		})
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, meResponse{
		Subject: caller.Subject, Username: caller.Username, RoleAssignments: assignments, Setup: setup,
	})
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
