package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

//go:embed reference.sql
var referenceSQL string

const memberWorkspace = ` FROM workspaces w WHERE w.slug = $2 AND EXISTS (
    SELECT 1 FROM role_assignments a JOIN users u ON u.id = a.user_id
    WHERE a.workspace_id = w.id AND u.oidc_subject = $1)`

const publishedReferenceVersion = `md5(COALESCE((
        SELECT string_agg(v.id::text, ',' ORDER BY v.id) FROM layers l JOIN layer_versions v ON v.layer_id=l.id
        WHERE l.workspace_id=w.id AND l.role IN ('hierarchy','forest','exclusion') AND v.status='published'
        ), ''))`

func (a api) referenceError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "A role in this workspace is required.")
		return
	}
	a.logger.Error("Could not read workspace geography", "error", err)
	writeError(w, http.StatusInternalServerError, "Could not load workspace geography.")
}

func (a api) workspaceMap(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var info json.RawMessage
	err := a.reader.QueryRow(ctx, `SELECT jsonb_build_object(
        'version', `+publishedReferenceVersion+`,
        'boundary', CASE WHEN w.boundary IS NOT NULL THEN ARRAY[ST_XMin(w.boundary), ST_YMin(w.boundary),
            ST_XMax(w.boundary), ST_YMax(w.boundary)] END,
        'levels', COALESCE((SELECT jsonb_agg(jsonb_build_object('hierarchy', h.slug, 'depth', l.depth,
            'slug', l.slug, 'labels', l.labels) ORDER BY h.slug, l.depth)
            FROM hierarchies h JOIN hierarchy_levels l ON l.hierarchy_id = h.id
            WHERE h.workspace_id = w.id), '[]'))`+memberWorkspace,
		r.Context().Value(identityKey{}).(identity).Subject, chi.URLParam(r, "slug")).Scan(&info)
	if err != nil {
		a.referenceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	writeJSON(w, 200, info)
}

func (a api) referenceTile(w http.ResponseWriter, r *http.Request) {
	z, x, y, ok := tileCoordinates(r)
	if !ok {
		writeError(w, 400, "Invalid tile coordinates.")
		return
	}
	outline := r.URL.Query().Get("outline") == "1"
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	// The version check and the tile must see the same publication.
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		a.referenceError(w, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id, version string
	err = tx.QueryRow(ctx, `SELECT w.id::text, `+publishedReferenceVersion+memberWorkspace, r.Context().Value(identityKey{}).(identity).Subject, chi.URLParam(r, "slug")).Scan(&id, &version)
	if err != nil {
		a.referenceError(w, err)
		return
	}
	etag := `W/"ref1-` + version + `"`
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Add("Vary", "Authorization")
	w.Header().Set("ETag", etag)
	for _, tag := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == etag[2:] {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	var tile []byte
	if err := tx.QueryRow(ctx, referenceSQL, id, z, x, y, outline).Scan(&tile); err != nil {
		a.referenceError(w, err)
		return
	}
	w.Header().Set("Content-Type", tileType)
	if len(tile) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, err := w.Write(tile); err != nil {
		a.logger.Warn("Could not send reference tile", "error", err)
	}
}

// offlineReference returns simplified features within the caller's current grants.
func (a api) offlineReference(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var result json.RawMessage
	err := a.reader.QueryRow(ctx, offlineReferenceSQL,
		r.Context().Value(identityKey{}).(identity).Subject, chi.URLParam(r, "slug")).Scan(&result)
	if err != nil {
		a.referenceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, result)
}

//go:embed offline_reference.sql
var offlineReferenceSQL string
