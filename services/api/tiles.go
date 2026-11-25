package main

import (
	"context"
	_ "embed"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

//go:embed tiles.sql
var tilesSQL string

const tileType = "application/vnd.mapbox-vector-tile"

func (a api) detectionTile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	z, x, y, ok := tileCoordinates(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "Invalid tile coordinates.")
		return
	}

	query := r.URL.Query()
	hours := 168
	if query.Has("hours") {
		var err error
		hours, err = strconv.Atoi(query.Get("hours"))
		if err != nil || hours < 1 || hours > 720 {
			writeError(w, http.StatusBadRequest, "hours must be an integer from 1 to 720.")
			return
		}
	}
	classes := []string{"inside", "near_edge", "fixed_heat_source"}
	if query.Has("classes") {
		classes = strings.Split(query.Get("classes"), ",")
		for i, class := range classes {
			classes[i] = strings.TrimSpace(class)
			switch classes[i] {
			case "inside", "near_edge", "fixed_heat_source", "outside":
			default:
				writeError(w, http.StatusBadRequest, "Unknown forest class.")
				return
			}
		}
	}

	var path *string
	if query.Has("unitPath") {
		value := query.Get("unitPath")
		if !unitPrefix.MatchString(value) {
			writeError(w, 400, "Invalid unit path.")
			return
		}
		path = &value
	}
	caller := r.Context().Value(identityKey{}).(identity)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var allowed bool
	var tile []byte
	err := a.reader.QueryRow(ctx, tilesSQL, caller.Subject, chi.URLParam(r, "slug"),
		z, x, y, hours, classes, path).Scan(&allowed, &tile)
	if err != nil {
		a.logger.Error("Could not read the detection tile", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load the detection tile.")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "A role in this workspace is required.")
		return
	}

	w.Header().Set("Content-Type", tileType)
	if len(tile) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, err := w.Write(tile); err != nil {
		a.logger.Warn("Could not send the detection tile", "error", err)
	}
}

const devOrigins = "http://127.0.0.1:4200,http://localhost:4200"

func allowOrigins(origins string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool)
	for _, origin := range strings.Split(origins, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			allowed[origin] = true
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("Vary", "Origin")
			origin := r.Header.Get("Origin")
			if allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				switch r.Method {
				case http.MethodPost:
					w.Header().Set("Access-Control-Expose-Headers", "Location")
				case http.MethodGet:
					// Attachment downloads name their file here.
					w.Header().Set("Access-Control-Expose-Headers", "Content-Disposition")
				}
			}
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.Header().Add("Vary", "Access-Control-Request-Method")
				method := r.Header.Get("Access-Control-Request-Method")
				switch method {
				case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete:
				default:
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if !allowed[origin] {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				w.Header().Set("Access-Control-Allow-Methods", method)
				// The field app names its device on every call.
				headers := "Authorization, " + deviceHeader
				if method == http.MethodPost || method == http.MethodPut {
					headers += ", Content-Type"
				}
				w.Header().Set("Access-Control-Allow-Headers", headers)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func tileCoordinates(r *http.Request) (int32, int32, int32, bool) {
	z, errZ := strconv.ParseUint(chi.URLParam(r, "z"), 10, 5)
	x, errX := strconv.ParseUint(chi.URLParam(r, "x"), 10, 31)
	y, errY := strconv.ParseUint(strings.TrimSuffix(chi.URLParam(r, "y"), ".mvt"), 10, 31)
	if errZ != nil || errX != nil || errY != nil || z > 30 || x >= 1<<z || y >= 1<<z ||
		!strings.HasSuffix(chi.URLParam(r, "y"), ".mvt") {
		return 0, 0, 0, false
	}

	return int32(z), int32(x), int32(y), true
}
