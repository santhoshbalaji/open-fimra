package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// First-run setup (PLAN.md section 7).
//
// A fresh install has no workspaces. The first signed-in user who sends the
// one-time setup code becomes the state administrator at the new workspace's
// root. The code is printed in the API log at start-up, or taken from
// FIMRA_SETUP_CODE, so only someone with access to the server can claim the
// install. The claim is a row in install_setup that can exist only once.

const setupLockSQL = "SELECT pg_advisory_xact_lock(hashtext('open-fimra:install-setup'))"

// Wrong codes allowed in each window before bootstrap pauses.
const (
	setupAttempts      = 10
	setupAttemptWindow = 15 * time.Minute
)

type setupGuard struct {
	digest [32]byte
	now    func() time.Time

	mu       sync.Mutex
	failures []time.Time
}

func newSetupGuard(code string) *setupGuard {
	return &setupGuard{digest: sha256.Sum256([]byte(normalSetupCode(code))), now: time.Now}
}

func normalSetupCode(code string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
}

// check compares a code in constant time. It refuses every code, right or
// wrong, while too many wrong codes were sent recently.
func (g *setupGuard) check(code string) (ok, paused bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	recent := g.failures[:0]
	for _, at := range g.failures {
		if now.Sub(at) < setupAttemptWindow {
			recent = append(recent, at)
		}
	}
	g.failures = recent
	if len(g.failures) >= setupAttempts {
		return false, true
	}
	digest := sha256.Sum256([]byte(normalSetupCode(code)))
	if subtle.ConstantTimeCompare(digest[:], g.digest[:]) == 1 {
		return true, false
	}
	g.failures = append(g.failures, now)
	return false, false
}

// generateSetupCode returns about 79 random bits as four groups of letters
// and digits that are hard to confuse.
func generateSetupCode() (string, error) {
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	var out strings.Builder
	buf := make([]byte, 1)
	for out.Len() < 19 {
		if out.Len()%5 == 4 {
			out.WriteByte('-')
			continue
		}
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		// Reject bytes above the largest multiple of the alphabet size.
		if int(buf[0]) >= 256-256%len(alphabet) {
			continue
		}
		out.WriteByte(alphabet[int(buf[0])%len(alphabet)])
	}
	return out.String(), nil
}

func installOpen(ctx context.Context, db rowQuerier) (bool, error) {
	var open bool
	err := db.QueryRow(ctx, `
		SELECT NOT EXISTS (SELECT 1 FROM install_setup) AND NOT EXISTS (SELECT 1 FROM workspaces)
	`).Scan(&open)
	return open, err
}

var (
	stateCodePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_-]{0,38}[A-Za-z0-9])?$`)
	levelSlugPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	languagePattern  = regexp.MustCompile(`^[a-z]{2,3}$`)
)

type bootstrapRequest struct {
	SetupCode     string `json:"setupCode"`
	Mode          string `json:"mode"`
	WorkspaceName string `json:"workspaceName"`
	WorkspaceSlug string `json:"workspaceSlug"`
	StateCode     string `json:"stateCode"`
}

func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, into any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil || decoder.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "Send the request as JSON with the documented fields.")
		return false
	}
	return true
}

func plainText(value string, max int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= max &&
		!strings.ContainsFunc(value, func(r rune) bool { return r < ' ' || r == 0x7f })
}

func (a api) setupError(w http.ResponseWriter, err error) {
	a.logger.Error("Could not handle first-run setup", "error", err)
	writeError(w, http.StatusInternalServerError, "Could not handle first-run setup.")
}

// setupInfo tells a signed-in user whether the install can still be claimed.
func (a api) setupInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	open, err := installOpen(r.Context(), a.pool)
	if err != nil {
		a.setupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bootstrapAvailable": open && a.setup != nil})
}

func (a api) bootstrap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var req bootstrapRequest
	if !decodeJSON(w, r, 4096, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	open, err := installOpen(ctx, a.pool)
	if err != nil {
		a.setupError(w, err)
		return
	}
	if !open || a.setup == nil {
		writeError(w, http.StatusConflict, "This installation is already set up. Ask its administrator for access.")
		return
	}
	switch {
	case req.Mode == "national":
		writeError(w, http.StatusBadRequest, "National mode is not available yet. Phase 1 supports one state per installation.")
		return
	case req.Mode != "state":
		writeError(w, http.StatusBadRequest, "Choose state mode.")
		return
	case !plainText(strings.TrimSpace(req.WorkspaceName), 120):
		writeError(w, http.StatusBadRequest, "Enter a workspace name of up to 120 characters.")
		return
	case !layerSlug.MatchString(req.WorkspaceSlug) || len(req.WorkspaceSlug) > 40:
		writeError(w, http.StatusBadRequest, "Use a workspace address of up to 40 lowercase letters, numbers and hyphens.")
		return
	case !stateCodePattern.MatchString(req.StateCode):
		writeError(w, http.StatusBadRequest, "Enter the state code used in your state boundary file, such as DS.")
		return
	}
	ok, paused := a.setup.check(req.SetupCode)
	if paused {
		writeError(w, http.StatusTooManyRequests, "Too many wrong setup codes. Wait 15 minutes and try again.")
		return
	}
	if !ok {
		a.logger.Warn("A first-run setup attempt used a wrong setup code")
		writeError(w, http.StatusForbidden, "The setup code is not correct. Find it in the API log or in FIMRA_SETUP_CODE.")
		return
	}

	caller := ctx.Value(identityKey{}).(identity)
	name := strings.TrimSpace(req.WorkspaceName)
	var claimed bool
	err = pgx.BeginFunc(ctx, a.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, setupLockSQL); err != nil {
			return fmt.Errorf("lock first-run setup: %w", err)
		}
		open, err := installOpen(ctx, tx)
		if err != nil || !open {
			return err
		}
		var userID, workspaceID, hierarchyID pgtype.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO users (oidc_subject, username, display_name) VALUES ($1, $2, $3)
			ON CONFLICT (oidc_subject) DO UPDATE SET username = EXCLUDED.username,
				display_name = EXCLUDED.display_name, last_seen_at = now()
			RETURNING id
		`, caller.Subject, caller.Username, caller.Name).Scan(&userID)
		if err != nil {
			return fmt.Errorf("save the first administrator: %w", err)
		}
		// Detections outside the boundary plus 5 km are never matched; 500 m
		// outside a forest counts as near the edge. Both match the demo state.
		err = tx.QueryRow(ctx, `
			INSERT INTO workspaces (slug, name, settings)
			VALUES ($1, $2, '{"nearEdgeMetres": 500, "areaMarginMetres": 5000}') RETURNING id
		`, req.WorkspaceSlug, name).Scan(&workspaceID)
		if err != nil {
			return fmt.Errorf("create the workspace: %w", err)
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO hierarchies (workspace_id, slug, name, kind)
			VALUES ($1, 'territorial', 'Territorial', 'territorial') RETURNING id
		`, workspaceID).Scan(&hierarchyID)
		if err != nil {
			return fmt.Errorf("create the hierarchy: %w", err)
		}
		labels, _ := json.Marshal(stateLevel.Labels)
		root := strings.ToLower(req.StateCode)
		// The root unit has no shape until the state boundary is published.
		// Publishing replaces it at the same path, so the grant stays valid.
		if _, err := tx.Exec(ctx, `
			WITH level AS (
				INSERT INTO hierarchy_levels (hierarchy_id, depth, slug, labels) VALUES ($1, 0, $2, $3)
			), root AS (
				INSERT INTO units (hierarchy_id, depth, code, name, path) VALUES ($1, 0, $4, $5, $6::ltree)
			)
			INSERT INTO workspace_setup (workspace_id, mode, hierarchy_id) VALUES ($7, $8, $1)
		`, hierarchyID, stateLevel.Slug, labels, req.StateCode, name, root, workspaceID, req.Mode); err != nil {
			return fmt.Errorf("create the root unit: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO role_assignments (user_id, workspace_id, hierarchy_id, unit_path, role_slug)
			VALUES ($1, $2, $3, $4::ltree, 'state_admin')
		`, userID, workspaceID, hierarchyID, root); err != nil {
			return fmt.Errorf("grant the first administrator: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			WITH steps AS (
				INSERT INTO workspace_setup_steps (workspace_id, step, status, updated_by)
				VALUES ($1, 'mode', 'done', $2), ($1, 'workspace', 'done', $2)
			), claim AS (
				INSERT INTO install_setup (workspace_id, claimed_by) VALUES ($1, $2)
			)
			INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
			VALUES ($1, 'user', $2, 'setup.bootstrap', 'workspace', $1::text,
				jsonb_build_object('mode', $3::text, 'rootPath', $4::text))
		`, workspaceID, caller.Subject, req.Mode, root); err != nil {
			return fmt.Errorf("record the setup claim: %w", err)
		}
		claimed = true
		return nil
	})
	if err != nil {
		a.setupError(w, err)
		return
	}
	if !claimed {
		writeError(w, http.StatusConflict, "This installation is already set up. Ask its administrator for access.")
		return
	}
	a.logger.Info("First-run setup claimed", "workspace", req.WorkspaceSlug)
	writeJSON(w, http.StatusCreated, map[string]any{"workspace": workspace{Slug: req.WorkspaceSlug, Name: name}})
}

type setupStep struct {
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type setupLevel struct {
	Depth    int16       `json:"depth"`
	Slug     string      `json:"slug"`
	Labels   levelLabels `json:"labels"`
	Units    int64       `json:"units"`
	Optional bool        `json:"optional"`
}

type setupUpload struct {
	VersionID pgtype.UUID `json:"versionId"`
	Version   int32       `json:"version"`
	Status    string      `json:"status"`
}

type setupLayer struct {
	Key       string       `json:"key"`
	Slug      string       `json:"slug"`
	Name      string       `json:"name"`
	Role      string       `json:"role"`
	Depth     *int16       `json:"depth,omitempty"`
	Required  bool         `json:"required"`
	Published bool         `json:"published"`
	Latest    *setupUpload `json:"latest"`
}

type setupRoot struct {
	Code      string `json:"code"`
	Path      string `json:"path"`
	Published bool   `json:"published"`
}

type setupState struct {
	Workspace    workspace            `json:"workspace"`
	Mode         string               `json:"mode"`
	Preset       *string              `json:"preset"`
	Hierarchy    string               `json:"hierarchy"`
	Root         *setupRoot           `json:"root"`
	BoundarySet  bool                 `json:"boundarySet"`
	Levels       []setupLevel         `json:"levels"`
	Layers       []setupLayer         `json:"layers"`
	Steps        map[string]setupStep `json:"steps"`
	FIRMS        firmsStatus          `json:"firms"`
	EmailEnabled bool                 `json:"emailEnabled"`
	FinishedAt   *time.Time           `json:"finishedAt"`
	Presets      []hierarchyPreset    `json:"presets"`
}

// wizardLayerSlug names the layer the wizard uses for one hierarchy level.
func wizardLayerSlug(hierarchy, level string) string {
	return hierarchy + "-" + strings.ReplaceAll(level, "_", "-")
}

type setupContext struct {
	workspaceID, hierarchyID pgtype.UUID
	hierarchy                string
}

// setupAdmin checks for a root administrator and returns the workspace's
// setup record. A workspace created before the wizard gets one on first use.
func (a api) setupAdmin(w http.ResponseWriter, r *http.Request) (setupContext, bool) {
	workspaceID, ok := a.layerAdmin(w, r)
	if !ok {
		return setupContext{}, false
	}
	s := setupContext{workspaceID: workspaceID}
	err := a.pool.QueryRow(r.Context(), `
		WITH existing AS (
			SELECT s.hierarchy_id FROM workspace_setup s WHERE s.workspace_id = $1
		), created AS (
			INSERT INTO workspace_setup (workspace_id, mode, hierarchy_id)
			SELECT $1, 'state', h.id FROM hierarchies h
			WHERE h.workspace_id = $1 AND NOT EXISTS (SELECT 1 FROM existing)
			ORDER BY h.kind <> 'territorial', h.created_at, h.slug LIMIT 1
			ON CONFLICT (workspace_id) DO NOTHING
			RETURNING hierarchy_id
		)
		SELECT h.id, h.slug FROM hierarchies h
		WHERE h.id = (SELECT hierarchy_id FROM existing UNION ALL SELECT hierarchy_id FROM created LIMIT 1)
	`, workspaceID).Scan(&s.hierarchyID, &s.hierarchy)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "This workspace has no hierarchy to set up.")
		return s, false
	}
	if err != nil {
		a.setupError(w, err)
		return s, false
	}
	return s, true
}

func (a api) loadSetupState(ctx context.Context, s setupContext) (setupState, error) {
	state := setupState{Hierarchy: s.hierarchy, Steps: map[string]setupStep{}, Presets: hierarchyPresets, EmailEnabled: a.emailEnabled}
	err := a.pool.QueryRow(ctx, `
		SELECT w.slug, w.name, s.mode, s.preset, w.boundary IS NOT NULL, s.finished_at
		FROM workspaces w JOIN workspace_setup s ON s.workspace_id = w.id WHERE w.id = $1
	`, s.workspaceID).Scan(&state.Workspace.Slug, &state.Workspace.Name, &state.Mode, &state.Preset,
		&state.BoundarySet, &state.FinishedAt)
	if err != nil {
		return state, fmt.Errorf("read the setup record: %w", err)
	}
	var root setupRoot
	err = a.pool.QueryRow(ctx, `
		SELECT code, path::text, layer_version_id IS NOT NULL FROM units
		WHERE hierarchy_id = $1 AND depth = 0 AND valid_to IS NULL ORDER BY path LIMIT 1
	`, s.hierarchyID).Scan(&root.Code, &root.Path, &root.Published)
	if err == nil {
		state.Root = &root
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return state, fmt.Errorf("read the root unit: %w", err)
	}

	rows, err := a.pool.Query(ctx, `
		SELECT hl.depth, hl.slug, hl.labels, (SELECT count(*) FROM units u WHERE u.hierarchy_id = hl.hierarchy_id
			AND u.depth = hl.depth AND u.valid_to IS NULL AND u.layer_version_id IS NOT NULL), hl.optional
		FROM hierarchy_levels hl WHERE hl.hierarchy_id = $1 ORDER BY hl.depth
	`, s.hierarchyID)
	if err != nil {
		return state, fmt.Errorf("read the hierarchy levels: %w", err)
	}
	state.Levels, err = pgx.CollectRows(rows, pgx.RowToStructByPos[setupLevel])
	if err != nil {
		return state, fmt.Errorf("collect the hierarchy levels: %w", err)
	}

	for _, level := range state.Levels {
		depth := level.Depth
		state.Layers = append(state.Layers, setupLayer{
			Key: fmt.Sprintf("level:%d", depth), Slug: wizardLayerSlug(s.hierarchy, level.Slug),
			Name: level.Labels["en"] + " boundaries", Role: "hierarchy", Depth: &depth, Required: !level.Optional,
		})
	}
	state.Layers = append(state.Layers,
		setupLayer{Key: "forest", Slug: "forest", Name: "Forest", Role: "forest", Required: true},
		setupLayer{Key: "exclusion", Slug: "exclusions", Name: "Known heat sources", Role: "exclusion"})
	for i := range state.Layers {
		layer := &state.Layers[i]
		var latest setupUpload
		err := a.pool.QueryRow(ctx, `
			SELECT v.id, v.version, CASE WHEN v.status = 'draft' THEN j.status ELSE v.status END,
				EXISTS (SELECT 1 FROM layer_versions p WHERE p.layer_id = l.id AND p.status = 'published')
			FROM layers l JOIN layer_versions v ON v.layer_id = l.id JOIN geo_jobs j ON j.layer_version_id = v.id
			WHERE l.workspace_id = $1 AND l.slug = $2 ORDER BY v.version DESC LIMIT 1
		`, s.workspaceID, layer.Slug).Scan(&latest.VersionID, &latest.Version, &latest.Status, &layer.Published)
		if err == nil {
			layer.Latest = &latest
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return state, fmt.Errorf("read the %s layer: %w", layer.Slug, err)
		}
		if layer.Depth != nil {
			// A level published by another layer, such as the seed, also counts.
			layer.Published = layer.Published || state.Levels[*layer.Depth].Units > 0
		}
	}

	rows, err = a.pool.Query(ctx, `
		SELECT step, status, updated_at FROM workspace_setup_steps WHERE workspace_id = $1
	`, s.workspaceID)
	if err != nil {
		return state, fmt.Errorf("read the setup steps: %w", err)
	}
	var step string
	var item setupStep
	_, err = pgx.ForEachRow(rows, []any{&step, &item.Status, &item.UpdatedAt}, func() error {
		state.Steps[step] = item
		return nil
	})
	if err != nil {
		return state, fmt.Errorf("collect the setup steps: %w", err)
	}
	state.FIRMS, err = a.firmsStatus(ctx, s.workspaceID.String())
	return state, err
}

func (a api) setupState(w http.ResponseWriter, r *http.Request) {
	s, ok := a.setupAdmin(w, r)
	if !ok {
		return
	}
	a.writeSetupState(w, r.Context(), s, http.StatusOK)
}

func (a api) writeSetupState(w http.ResponseWriter, ctx context.Context, s setupContext, status int) {
	state, err := a.loadSetupState(ctx, s)
	if err != nil {
		a.setupError(w, err)
		return
	}
	writeJSON(w, status, state)
}

type hierarchyRequest struct {
	Preset string        `json:"preset"`
	Levels []presetLevel `json:"levels"`
}

func validLabels(labels levelLabels) bool {
	if len(labels) > 6 || !plainText(strings.TrimSpace(labels["en"]), 60) {
		return false
	}
	for language, label := range labels {
		if !languagePattern.MatchString(language) || !plainText(strings.TrimSpace(label), 60) {
			return false
		}
	}
	return true
}

// setupHierarchy saves the levels below the state. Levels that already have
// units keep their identifier; only their labels can change.
func (a api) setupHierarchy(w http.ResponseWriter, r *http.Request) {
	s, ok := a.setupAdmin(w, r)
	if !ok {
		return
	}
	var req hierarchyRequest
	if !decodeJSON(w, r, 16<<10, &req) {
		return
	}
	if _, known := findPreset(req.Preset); !known && req.Preset != "custom" {
		writeError(w, http.StatusBadRequest, "Choose one of the listed presets.")
		return
	}
	if len(req.Levels) < 2 || len(req.Levels) > 10 {
		writeError(w, http.StatusBadRequest, "A hierarchy needs 2 to 10 levels, starting with the state.")
		return
	}
	seen := map[string]bool{}
	for _, level := range req.Levels {
		if !levelSlugPattern.MatchString(level.Slug) || seen[level.Slug] {
			writeError(w, http.StatusBadRequest, "Give each level a different identifier of lowercase letters, numbers and underscores.")
			return
		}
		seen[level.Slug] = true
		for language, label := range level.Labels {
			level.Labels[language] = strings.TrimSpace(label)
		}
		if !validLabels(level.Labels) {
			writeError(w, http.StatusBadRequest, "Give each level an English label and optional translations of up to 60 characters.")
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	caller := ctx.Value(identityKey{}).(identity)
	var conflict string
	err := pgx.BeginFunc(ctx, a.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT 1 FROM workspace_setup WHERE workspace_id = $1 FOR UPDATE", s.workspaceID); err != nil {
			return fmt.Errorf("lock the setup record: %w", err)
		}
		// Units at a level, current or past, pin its identifier.
		var lastUsed int16
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(max(depth), -1) FROM units WHERE hierarchy_id = $1
		`, s.hierarchyID).Scan(&lastUsed); err != nil {
			return fmt.Errorf("read used levels: %w", err)
		}
		rows, err := tx.Query(ctx, "SELECT slug FROM hierarchy_levels WHERE hierarchy_id = $1 ORDER BY depth", s.hierarchyID)
		if err != nil {
			return fmt.Errorf("read the levels: %w", err)
		}
		current, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("collect the levels: %w", err)
		}
		if int(lastUsed) >= len(req.Levels) {
			conflict = "Levels that already have published units cannot be removed."
			return nil
		}
		for depth := 0; depth <= int(lastUsed); depth++ {
			if depth >= len(current) || current[depth] != req.Levels[depth].Slug {
				conflict = fmt.Sprintf("Level %d already has units. Keep its identifier; you can still change its labels.", depth+1)
				return nil
			}
		}
		if _, err := tx.Exec(ctx, "DELETE FROM hierarchy_levels WHERE hierarchy_id = $1 AND depth > $2", s.hierarchyID, lastUsed); err != nil {
			return fmt.Errorf("remove unused levels: %w", err)
		}
		for depth, level := range req.Levels {
			optional := false
			if depth > 0 && depth+1 < len(req.Levels) {
				parent, child := req.Levels[depth-1].Slug, req.Levels[depth+1].Slug
				optional = (parent == "division" && level.Slug == "sub_division" && child == "range") ||
					(parent == "range" && level.Slug == "section" && child == "beat")
			}
			labels, _ := json.Marshal(level.Labels)
			if _, err := tx.Exec(ctx, `
				INSERT INTO hierarchy_levels (hierarchy_id, depth, slug, labels, optional) VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (hierarchy_id, depth) DO UPDATE SET labels = EXCLUDED.labels
			`, s.hierarchyID, depth, level.Slug, labels, optional); err != nil {
				return fmt.Errorf("save level %d: %w", depth, err)
			}
		}
		_, err = tx.Exec(ctx, `
			WITH preset AS (
				UPDATE workspace_setup SET preset = $3 WHERE workspace_id = $1
			), step AS (
				INSERT INTO workspace_setup_steps (workspace_id, step, status, updated_by) VALUES ($1, 'hierarchy', 'done', $2)
				ON CONFLICT (workspace_id, step) DO UPDATE SET status = 'done', updated_at = now(), updated_by = $2
			)
			INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
			VALUES ($1, 'user', $2, 'setup.hierarchy', 'hierarchy', $4::text,
				jsonb_build_object('preset', $3::text, 'levels', $5::int))
		`, s.workspaceID, caller.Subject, req.Preset, s.hierarchyID, len(req.Levels))
		if err != nil {
			return fmt.Errorf("record the hierarchy step: %w", err)
		}
		return nil
	})
	if err != nil {
		a.setupError(w, err)
		return
	}
	if conflict != "" {
		writeError(w, http.StatusConflict, conflict)
		return
	}
	a.writeSetupState(w, ctx, s, http.StatusOK)
}

var setupSteps = []string{"hierarchy", "layers", "firms", "forms", "invitations"}

// setupStepUpdate marks the layers, FIRMS, forms or invitations step as done
// or skipped, after checking that the step's work is in place.
func (a api) setupStepUpdate(w http.ResponseWriter, r *http.Request) {
	s, ok := a.setupAdmin(w, r)
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if !decodeJSON(w, r, 1024, &req) {
		return
	}
	step := chi.URLParam(r, "step")
	if !slices.Contains(setupSteps[1:], step) || (req.Status != "done" && req.Status != "skipped") {
		writeError(w, http.StatusBadRequest, "Choose the layers, firms, forms or invitations step, and done or skipped.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	state, err := a.loadSetupState(ctx, s)
	if err != nil {
		a.setupError(w, err)
		return
	}
	switch {
	case step == "layers" && req.Status == "skipped":
		writeError(w, http.StatusBadRequest, "The alert map needs the boundaries and forest layer. This step cannot be skipped.")
		return
	case step == "layers":
		for _, layer := range state.Layers {
			if layer.Required && !layer.Published {
				writeError(w, http.StatusConflict, "Publish "+strings.ToLower(layer.Name)+" first.")
				return
			}
		}
		if !state.BoundarySet {
			writeError(w, http.StatusConflict, "Publish the state boundary to set the workspace boundary.")
			return
		}
	case step == "firms" && req.Status == "done" && state.FIRMS.Source == keyMissing:
		writeError(w, http.StatusConflict, "Save a FIRMS map key first, or skip this step.")
		return
	case step == "forms" && req.Status == "done":
		enabled, err := starterPackEnabled(ctx, a.pool, s.workspaceID.String())
		if err != nil {
			a.setupError(w, err)
			return
		}
		if !enabled {
			writeError(w, http.StatusConflict, "Switch on the starter forms first, or skip this step.")
			return
		}
	}
	caller := ctx.Value(identityKey{}).(identity)
	if _, err := a.pool.Exec(ctx, `
		WITH step AS (
			INSERT INTO workspace_setup_steps (workspace_id, step, status, updated_by) VALUES ($1, $3, $4, $2)
			ON CONFLICT (workspace_id, step) DO UPDATE SET status = EXCLUDED.status, updated_at = now(), updated_by = $2
		)
		INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id, details)
		VALUES ($1, 'user', $2, 'setup.step', 'workspace', $1::text,
			jsonb_build_object('step', $3::text, 'status', $4::text))
	`, s.workspaceID, caller.Subject, step, req.Status); err != nil {
		a.setupError(w, err)
		return
	}
	a.writeSetupState(w, ctx, s, http.StatusOK)
}

// setupFinish closes the wizard once every step is done or skipped, and
// setupReopen opens it again from settings.
func (a api) setupFinish(w http.ResponseWriter, r *http.Request) {
	s, ok := a.setupAdmin(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	caller := ctx.Value(identityKey{}).(identity)
	reopen := strings.HasSuffix(r.URL.Path, "/reopen")
	if !reopen {
		state, err := a.loadSetupState(ctx, s)
		if err != nil {
			a.setupError(w, err)
			return
		}
		for _, step := range setupSteps {
			if _, done := state.Steps[step]; !done {
				writeError(w, http.StatusConflict, "Finish or skip every step before closing the wizard. Missing: "+step+".")
				return
			}
		}
	}
	action := "setup.finish"
	if reopen {
		action = "setup.reopen"
	}
	if _, err := a.pool.Exec(ctx, `
		WITH changed AS (
			UPDATE workspace_setup SET finished_at = CASE WHEN $3 THEN NULL ELSE now() END WHERE workspace_id = $1
		)
		INSERT INTO audit_log (workspace_id, actor_kind, actor_id, action, target_type, target_id)
		VALUES ($1, 'user', $2, $4, 'workspace', $1::text)
	`, s.workspaceID, caller.Subject, reopen, action); err != nil {
		a.setupError(w, err)
		return
	}
	a.writeSetupState(w, ctx, s, http.StatusOK)
}

type setupUnit struct {
	Depth int16  `json:"depth"`
	Code  string `json:"code"`
	Name  string `json:"name"`
	Path  string `json:"path"`
}

// setupUnits lists the current units that officers can be invited to.
func (a api) setupUnits(w http.ResponseWriter, r *http.Request) {
	s, ok := a.setupAdmin(w, r)
	if !ok {
		return
	}
	rows, err := a.pool.Query(r.Context(), `
		SELECT depth, code, name, path::text FROM units
		WHERE hierarchy_id = $1 AND valid_to IS NULL ORDER BY path LIMIT 5000
	`, s.hierarchyID)
	if err != nil {
		a.setupError(w, err)
		return
	}
	units, err := pgx.CollectRows(rows, pgx.RowToStructByPos[setupUnit])
	if err != nil {
		a.setupError(w, err)
		return
	}
	if units == nil {
		units = []setupUnit{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"units": units})
}

// unfinishedSetups lists the workspaces whose wizard the caller should see:
// those where the caller is a root administrator and setup is not finished.
func unfinishedSetups(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, subject string) ([]string, error) {
	rows, err := db.Query(ctx, `
		SELECT DISTINCT w.slug FROM workspace_setup s JOIN workspaces w ON w.id = s.workspace_id
		JOIN role_assignments a ON a.workspace_id = w.id AND a.role_slug = 'state_admin'
		JOIN users u ON u.id = a.user_id
		JOIN units root ON root.hierarchy_id = a.hierarchy_id AND root.current_path = a.unit_path AND root.depth = 0
		WHERE s.finished_at IS NULL AND u.oidc_subject = $1 ORDER BY w.slug
	`, subject)
	if err != nil {
		return nil, err
	}
	slugs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if slugs == nil {
		slugs = []string{}
	}
	return slugs, err
}
