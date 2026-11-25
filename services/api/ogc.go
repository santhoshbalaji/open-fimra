package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// The GIS link is a read-only OGC API - Features service (Part 1, Core, with
// GeoJSON). Each workspace is its own service at /ogc/workspaces/{slug}.
// Detections and alerts follow the same grant rules as the map tiles and the
// alert queue. Published layers and hierarchy units follow the reference
// map: every member of the workspace sees them.

//go:embed ogc_detections.sql
var ogcDetectionsSQL string

//go:embed ogc_alerts.sql
var ogcAlertsSQL string

const (
	ogcDefaultLimit = 100
	ogcMaxLimit     = 1000
	crs84           = "http://www.opengis.net/def/crs/OGC/1.3/CRS84"
	geoJSONType     = "application/geo+json"
	openAPIType     = "application/vnd.oai.openapi+json;version=3.0"
)

var ogcConformance = []string{
	"http://www.opengis.net/spec/ogcapi-features-1/1.0/conf/core",
	"http://www.opengis.net/spec/ogcapi-features-1/1.0/conf/geojson",
	"http://www.opengis.net/spec/ogcapi-features-1/1.0/conf/oas30",
}

type ogcLink struct {
	Href  string `json:"href"`
	Rel   string `json:"rel"`
	Type  string `json:"type,omitempty"`
	Title string `json:"title,omitempty"`
}

// Collection kinds. An incidents collection would be one more kind with its
// own query, listed in ogcWorkspace.collections.
const (
	ogcDetections = "detections"
	ogcAlerts     = "alerts"
	ogcLayer      = "layer"
	ogcUnits      = "units"
)

// ogcCollection is one collection a workspace offers.
type ogcCollection struct {
	ID          string
	Title       string
	Description string
	kind        string
	// timed collections accept datetime against the observation time.
	timed bool
	// params are the query parameters the items request accepts beyond the
	// common ones.
	params []string
	// For a published layer.
	versionID, role string
	// For one level of a hierarchy.
	hierarchyID, hierarchy, level string
	depth                         int
}

type ogcWorkspace struct {
	id, slug, name string
	bbox           []float64
	// mainHierarchy and mainLevels name the hierarchy whose levels become
	// flat fields on detections and alerts, as in file exports.
	mainHierarchy string
	mainLevels    []string
	collections   []ogcCollection
}

const ogcWorkspaceSQL = `
SELECT w.id::text, w.name,
       CASE WHEN w.boundary IS NOT NULL THEN ARRAY[ST_XMin(w.boundary), ST_YMin(w.boundary),
            ST_XMax(w.boundary), ST_YMax(w.boundary)] END,
       COALESCE(main.slug, ''), COALESCE(main.levels, '{}'::text[]),
       COALESCE((SELECT jsonb_agg(jsonb_build_object('slug', l.slug, 'name', l.name, 'role', l.role,
                     'version', v.id, 'number', v.version) ORDER BY l.slug)
                 FROM layers l JOIN layer_versions v ON v.layer_id = l.id AND v.status = 'published'
                 WHERE l.workspace_id = w.id AND l.role IN ('forest', 'exclusion')), '[]'),
       COALESCE((SELECT jsonb_agg(jsonb_build_object('id', h.id, 'hierarchy', h.slug, 'name', h.name,
                     'depth', l.depth, 'level', l.slug, 'label', l.labels->>'en') ORDER BY h.slug, l.depth)
                 FROM hierarchies h JOIN hierarchy_levels l ON l.hierarchy_id = h.id
                 WHERE h.workspace_id = w.id), '[]')
FROM workspaces w
LEFT JOIN LATERAL (
    SELECT h.slug, ARRAY(SELECT l.slug FROM hierarchy_levels l WHERE l.hierarchy_id = h.id ORDER BY l.depth) AS levels
    FROM hierarchies h WHERE h.workspace_id = w.id
    ORDER BY (h.kind = 'territorial') DESC, h.created_at, h.slug LIMIT 1
) main ON true
WHERE w.slug = $2 AND EXISTS (SELECT 1 FROM alert_grants g WHERE g.workspace_id = w.id AND g.oidc_subject = $1)`

var errNotMember = errors.New("not a member of the workspace")

// ogcWorkspaceFor reads the workspace and its collections, if the caller has
// a role in it.
func (a api) ogcWorkspaceFor(ctx context.Context, subject, slug string) (ogcWorkspace, error) {
	ws := ogcWorkspace{slug: slug}
	var layers, levels []byte
	err := a.pool.QueryRow(ctx, ogcWorkspaceSQL, subject, slug).Scan(&ws.id, &ws.name, &ws.bbox,
		&ws.mainHierarchy, &ws.mainLevels, &layers, &levels)
	if errors.Is(err, pgx.ErrNoRows) {
		return ws, errNotMember
	}
	if err != nil {
		return ws, err
	}
	var published []struct {
		Slug, Name, Role, Version string
		Number                    int
	}
	var steps []struct {
		ID, Hierarchy, Name, Level, Label string
		Depth                             int
	}
	if err := json.Unmarshal(layers, &published); err != nil {
		return ws, err
	}
	if err := json.Unmarshal(levels, &steps); err != nil {
		return ws, err
	}
	ws.collections = []ogcCollection{
		{ID: "detections", Title: "Detections", kind: ogcDetections, timed: true, params: []string{"forest_class"},
			Description: "Satellite fire detections matched to this workspace, with forest class and place, newest first."},
		{ID: "alerts", Title: "Alerts", kind: ogcAlerts, timed: true, params: []string{"forest_class", "queue", "step"},
			Description: "Fire alerts with their queue, current workflow step and whether that step is overdue, newest first."},
	}
	for _, l := range published {
		description := "Published forest boundaries, version " + strconv.Itoa(l.Number) + "."
		if l.Role == "exclusion" {
			description = "Published known heat sources, version " + strconv.Itoa(l.Number) + "."
		}
		ws.collections = append(ws.collections, ogcCollection{ID: "layer-" + l.Slug, Title: l.Name,
			Description: description, kind: ogcLayer, versionID: l.Version, role: l.Role})
	}
	for _, s := range steps {
		ws.collections = append(ws.collections, ogcCollection{ID: "units-" + s.Hierarchy + "-" + s.Level,
			Title: s.Name + ": " + s.Label, Description: "Current " + s.Label + " boundaries in the " + s.Name + " hierarchy.",
			kind: ogcUnits, hierarchyID: s.ID, hierarchy: s.Hierarchy, level: s.Level, depth: s.Depth})
	}
	return ws, nil
}

func (ws ogcWorkspace) collection(id string) (ogcCollection, bool) {
	for _, c := range ws.collections {
		if c.ID == id {
			return c, true
		}
	}
	return ogcCollection{}, false
}

// ogcBase is the absolute address of the workspace's service. Behind a proxy,
// keep the Host header and set X-Forwarded-Proto.
func ogcBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/ogc/workspaces/" + url.PathEscape(chi.URLParam(r, "slug"))
}

// ogcStart reads the workspace for an OGC request. It writes the error
// response when it returns false.
func (a api) ogcStart(w http.ResponseWriter, r *http.Request) (ogcWorkspace, context.Context, context.CancelFunc, bool) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Add("Vary", "Authorization, X-API-Key")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	caller := r.Context().Value(identityKey{}).(identity)
	ws, err := a.ogcWorkspaceFor(ctx, caller.Subject, chi.URLParam(r, "slug"))
	if errors.Is(err, errNotMember) {
		writeError(w, http.StatusForbidden, "A role in this workspace is required.")
		return ws, ctx, cancel, false
	}
	if err != nil {
		a.logger.Error("Could not read the GIS link workspace", "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load the GIS link.")
		return ws, ctx, cancel, false
	}
	// Only JSON and GeoJSON are offered.
	if f := r.URL.Query().Get("f"); f != "" && f != "json" && f != "geojson" {
		writeError(w, http.StatusNotAcceptable, "Only JSON is offered. Use f=json or leave f out.")
		return ws, ctx, cancel, false
	}
	return ws, ctx, cancel, true
}

func (a api) ogcLanding(w http.ResponseWriter, r *http.Request) {
	ws, _, cancel, ok := a.ogcStart(w, r)
	defer cancel()
	if !ok {
		return
	}
	base := ogcBase(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"title":       "Open FIMRA: " + ws.name,
		"description": "Read-only GIS link to the detections, alerts and published layers you can see in this workspace.",
		"links": []ogcLink{
			{Href: base, Rel: "self", Type: "application/json", Title: "This document"},
			{Href: base + "/api", Rel: "service-desc", Type: openAPIType, Title: "The API definition"},
			{Href: base + "/conformance", Rel: "conformance", Type: "application/json", Title: "Conformance classes"},
			{Href: base + "/collections", Rel: "data", Type: "application/json", Title: "Collections"},
		},
	})
}

func (a api) ogcConformance(w http.ResponseWriter, r *http.Request) {
	_, _, cancel, ok := a.ogcStart(w, r)
	defer cancel()
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conformsTo": ogcConformance})
}

func (ws ogcWorkspace) describe(base string, c ogcCollection) map[string]any {
	href := base + "/collections/" + url.PathEscape(c.ID)
	extent := map[string]any{}
	if len(ws.bbox) == 4 {
		extent["spatial"] = map[string]any{"bbox": [][]float64{ws.bbox}, "crs": crs84}
	}
	if c.timed {
		extent["temporal"] = map[string]any{"interval": [][]any{{nil, nil}}, "trs": "http://www.opengis.net/def/uom/ISO-8601/0/Gregorian"}
	}
	item := "feature"
	switch c.kind {
	case ogcDetections:
		item = "detection"
	case ogcAlerts:
		item = "alert"
	case ogcUnits:
		item = "unit"
	}
	return map[string]any{
		"id": c.ID, "title": c.Title, "description": c.Description, "itemType": "feature",
		"crs": []string{crs84}, "storageCrs": crs84, "extent": extent,
		"links": []ogcLink{
			{Href: href, Rel: "self", Type: "application/json", Title: c.Title},
			{Href: href + "/items", Rel: "items", Type: geoJSONType, Title: c.Title + " (each " + item + ")"},
		},
	}
}

func (a api) ogcCollections(w http.ResponseWriter, r *http.Request) {
	ws, _, cancel, ok := a.ogcStart(w, r)
	defer cancel()
	if !ok {
		return
	}
	base := ogcBase(r)
	collections := make([]map[string]any, 0, len(ws.collections))
	for _, c := range ws.collections {
		collections = append(collections, ws.describe(base, c))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"links": []ogcLink{
			{Href: base + "/collections", Rel: "self", Type: "application/json", Title: "Collections"},
		},
		"collections": collections,
	})
}

func (a api) ogcCollection(w http.ResponseWriter, r *http.Request) {
	ws, _, cancel, ok := a.ogcStart(w, r)
	defer cancel()
	if !ok {
		return
	}
	c, found := ws.collection(chi.URLParam(r, "collection"))
	if !found {
		writeError(w, http.StatusNotFound, "Collection not found.")
		return
	}
	writeJSON(w, http.StatusOK, ws.describe(ogcBase(r), c))
}

// ogcQuery is a checked items request.
type ogcQuery struct {
	limit       int
	bbox        []float64
	start, end  *time.Time
	classes     []string
	queue, step string
	// featureID asks for one feature.
	featureID *int64
	// The last row of the previous page.
	afterTime *time.Time
	afterID   *int64
}

var ogcCommonParams = []string{"limit", "bbox", "bbox-crs", "datetime", "cursor", "f", "crs"}

func parseOGCTime(value string) (*time.Time, error) {
	if value == ".." || value == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, errors.New("datetime must use RFC 3339 times, such as 2026-03-01T00:00:00Z, or an interval with a slash.")
	}
	return &t, nil
}

// readOGCQuery checks the items parameters for a collection.
func readOGCQuery(q url.Values, c ogcCollection) (ogcQuery, error) {
	query := ogcQuery{limit: ogcDefaultLimit}
	for name := range q {
		if !slices.Contains(ogcCommonParams, name) && !slices.Contains(c.params, name) {
			return query, errors.New("Unknown query parameter: " + name + ".")
		}
		if len(q[name]) > 1 {
			return query, errors.New("Give " + name + " only once.")
		}
	}
	for _, name := range []string{"crs", "bbox-crs"} {
		if q.Has(name) && q.Get(name) != crs84 {
			return query, errors.New("Only " + crs84 + " is offered for " + name + ".")
		}
	}
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 {
			return query, errors.New("limit must be a positive integer.")
		}
		query.limit = min(n, ogcMaxLimit)
	}
	if q.Has("bbox") {
		parts := strings.Split(q.Get("bbox"), ",")
		values := make([]float64, 0, len(parts))
		for _, p := range parts {
			v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				values = nil
				break
			}
			values = append(values, v)
		}
		if len(values) == 6 {
			values = []float64{values[0], values[1], values[3], values[4]}
		}
		if len(values) != 4 || values[0] > values[2] || values[1] > values[3] ||
			values[1] < -90 || values[3] > 90 || values[0] < -180 || values[2] > 180 {
			return query, errors.New("bbox must be west,south,east,north in degrees, with west at most east and south at most north.")
		}
		query.bbox = values
	}
	if q.Has("datetime") {
		value := q.Get("datetime")
		var err error
		if from, to, interval := strings.Cut(value, "/"); interval {
			if query.start, err = parseOGCTime(from); err == nil {
				query.end, err = parseOGCTime(to)
			}
			if err == nil && query.start != nil && query.end != nil && query.start.After(*query.end) {
				err = errors.New("The datetime interval ends before it starts.")
			}
		} else if value == ".." || value == "" {
			err = errors.New("datetime must be a time or an interval.")
		} else if query.start, err = parseOGCTime(value); err == nil {
			query.end = query.start
		}
		if err != nil {
			return query, err
		}
	}
	if q.Has("forest_class") {
		for _, class := range strings.Split(q.Get("forest_class"), ",") {
			class = strings.TrimSpace(class)
			if !slices.Contains([]string{"inside", "near_edge", "outside", "fixed_heat_source"}, class) {
				return query, errors.New("Unknown forest class.")
			}
			query.classes = append(query.classes, class)
		}
	}
	query.queue, query.step = q.Get("queue"), q.Get("step")
	if !slices.Contains([]string{"", "unit", "edge_review", "triage"}, query.queue) ||
		(query.step != "" && !stepSlug.MatchString(query.step)) {
		return query, errors.New("Unknown queue or workflow step.")
	}
	if q.Has("cursor") {
		raw := q.Get("cursor")
		if c.timed {
			timestamp, id, _ := strings.Cut(raw, ",")
			after, timeErr := time.Parse(time.RFC3339Nano, timestamp)
			n, err := strconv.ParseInt(id, 10, 64)
			if timeErr != nil || err != nil || n < 1 {
				return query, errors.New("Invalid cursor.")
			}
			query.afterTime, query.afterID = &after, &n
		} else {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n < 0 {
				return query, errors.New("Invalid cursor.")
			}
			query.afterID = &n
		}
	}
	return query, nil
}

// prop is one feature property. Properties keep their order, so desktop GIS
// shows the fields in a sensible order.
type prop struct {
	key   string
	value any
}

type props []prop

func (p props) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, kv := range p {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(kv.key)
		value, err := json.Marshal(kv.value)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

type ogcFeature struct {
	Type       string          `json:"type"`
	ID         int64           `json:"id"`
	Geometry   json.RawMessage `json:"geometry"`
	Properties props           `json:"properties"`
	Links      []ogcLink       `json:"links,omitempty"`
	// cursor continues the page after this feature.
	cursor string
}

func utc(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// levelProps turns a hierarchy snapshot into code and name fields for every
// level of the main hierarchy. Missing values are null.
func levelProps(levels []string, snapshot []byte) props {
	var units map[string]struct {
		Code *string `json:"code"`
		Name *string `json:"name"`
	}
	if len(snapshot) > 0 {
		_ = json.Unmarshal(snapshot, &units)
	}
	out := make(props, 0, 2*len(levels))
	for _, level := range levels {
		unit := units[level]
		out = append(out, prop{level + "_code", unit.Code}, prop{level + "_name", unit.Name})
	}
	return out
}

func geometry(raw *string) json.RawMessage {
	if raw == nil {
		return json.RawMessage("null")
	}
	return json.RawMessage(*raw)
}

// ogcFeatures reads up to limit features of one collection.
func (a api) ogcFeatures(ctx context.Context, subject string, ws ogcWorkspace, c ogcCollection, q ogcQuery, limit int) ([]ogcFeature, error) {
	var west, south, east, north *float64
	if q.bbox != nil {
		west, south, east, north = &q.bbox[0], &q.bbox[1], &q.bbox[2], &q.bbox[3]
	}
	var classes []string
	if len(q.classes) > 0 {
		classes = q.classes
	}
	var features []ogcFeature
	var rows pgx.Rows
	var err error
	switch c.kind {
	case ogcDetections:
		rows, err = a.pool.Query(ctx, ogcDetectionsSQL, subject, ws.id, ws.mainHierarchy, west, south, east, north,
			q.start, q.end, classes, q.featureID, q.afterTime, q.afterID, limit)
		if err != nil {
			return nil, err
		}
		features, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ogcFeature, error) {
			var id int64
			var observed time.Time
			var source, product, class, triage, hierarchy, places string
			var satellite, forestCode, confidence, geom *string
			var frp *float32
			var main []byte
			err := row.Scan(&id, &observed, &source, &product, &satellite, &class, &triage, &forestCode, &frp,
				&confidence, &main, &hierarchy, &places, &geom)
			p := props{{"detection_id", id}, {"observed_at", utc(observed)}, {"source", source}, {"product", product},
				{"satellite", satellite}, {"forest_class", class}, {"triage", triage}, {"forest_feature_code", forestCode},
				{"frp_mw", frp}, {"confidence", confidence}, {"place_hierarchy", hierarchy}}
			p = append(append(p, levelProps(ws.mainLevels, main)...), prop{"places", places})
			return ogcFeature{Type: "Feature", ID: id, Geometry: geometry(geom), Properties: p,
				cursor: utc(observed) + "," + strconv.FormatInt(id, 10)}, err
		})
	case ogcAlerts:
		rows, err = a.pool.Query(ctx, ogcAlertsSQL, subject, ws.id, ws.mainHierarchy, west, south, east, north,
			q.start, q.end, classes, q.featureID, q.afterTime, q.afterID, limit, q.queue, q.step)
		if err != nil {
			return nil, err
		}
		features, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ogcFeature, error) {
			var id, detection int64
			var queue, step, label, source, product, class, triage, hierarchy, places string
			var stepTime, created, observed time.Time
			var overdue bool
			var satellite, confidence, geom *string
			var frp *float32
			var main []byte
			err := row.Scan(&id, &detection, &queue, &step, &label, &stepTime, &overdue, &created, &observed,
				&source, &product, &satellite, &class, &triage, &frp, &confidence, &main, &hierarchy, &places, &geom)
			p := props{{"alert_id", id}, {"detection_id", detection}, {"queue", queue}, {"step", step},
				{"step_label", label}, {"step_time", utc(stepTime)}, {"overdue", overdue}, {"created_at", utc(created)},
				{"observed_at", utc(observed)}, {"source", source}, {"product", product}, {"satellite", satellite},
				{"forest_class", class}, {"triage", triage}, {"frp_mw", frp}, {"confidence", confidence},
				{"place_hierarchy", hierarchy}}
			p = append(append(p, levelProps(ws.mainLevels, main)...), prop{"places", places})
			return ogcFeature{Type: "Feature", ID: id, Geometry: geometry(geom), Properties: p,
				cursor: utc(observed) + "," + strconv.FormatInt(id, 10)}, err
		})
	case ogcLayer:
		rows, err = a.pool.Query(ctx, `
			SELECT f.id, f.code, f.name, f.attributes->>'legal_status', ST_AsGeoJSON(f.geom, 7)
			FROM layer_features f
			WHERE f.layer_version_id = $1
			  AND ($2::float8 IS NULL OR f.geom && ST_MakeEnvelope($2, $3::float8, $4::float8, $5::float8, 4326))
			  AND ($6::bigint IS NULL OR f.id = $6) AND ($7::bigint IS NULL OR f.id > $7)
			ORDER BY f.id LIMIT $8`, c.versionID, west, south, east, north, q.featureID, q.afterID, limit)
		if err != nil {
			return nil, err
		}
		features, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ogcFeature, error) {
			var id int64
			var code, name, legal, geom *string
			err := row.Scan(&id, &code, &name, &legal, &geom)
			p := props{{"feature_id", id}, {"code", code}, {"name", name}}
			if c.role == "forest" {
				p = append(p, prop{"legal_status", legal})
			}
			return ogcFeature{Type: "Feature", ID: id, Geometry: geometry(geom), Properties: p,
				cursor: strconv.FormatInt(id, 10)}, err
		})
	case ogcUnits:
		rows, err = a.pool.Query(ctx, `
			SELECT u.id, u.code, u.name, u.path::text, ST_AsGeoJSON(u.geom, 7)
			FROM units u
			WHERE u.hierarchy_id = $1 AND u.depth = $2 AND u.valid_to IS NULL
			  AND ($3::float8 IS NULL OR u.geom && ST_MakeEnvelope($3, $4::float8, $5::float8, $6::float8, 4326))
			  AND ($7::bigint IS NULL OR u.id = $7) AND ($8::bigint IS NULL OR u.id > $8)
			ORDER BY u.id LIMIT $9`, c.hierarchyID, c.depth, west, south, east, north, q.featureID, q.afterID, limit)
		if err != nil {
			return nil, err
		}
		features, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ogcFeature, error) {
			var id int64
			var code, name, path string
			var geom *string
			err := row.Scan(&id, &code, &name, &path, &geom)
			p := props{{"unit_id", id}, {"code", code}, {"name", name}, {"path", path},
				{"hierarchy", c.hierarchy}, {"level", c.level}}
			return ogcFeature{Type: "Feature", ID: id, Geometry: geometry(geom), Properties: p,
				cursor: strconv.FormatInt(id, 10)}, err
		})
	default:
		return nil, errors.New("unknown collection kind " + c.kind)
	}
	return features, err
}

func (a api) ogcItems(w http.ResponseWriter, r *http.Request) {
	ws, ctx, cancel, ok := a.ogcStart(w, r)
	defer cancel()
	if !ok {
		return
	}
	c, found := ws.collection(chi.URLParam(r, "collection"))
	if !found {
		writeError(w, http.StatusNotFound, "Collection not found.")
		return
	}
	query := r.URL.Query()
	q, err := readOGCQuery(query, c)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	caller := r.Context().Value(identityKey{}).(identity)
	features, err := a.ogcFeatures(ctx, caller.Subject, ws, c, q, q.limit+1)
	if err != nil {
		a.logger.Error("Could not read GIS link features", "collection", c.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load the features.")
		return
	}
	base := ogcBase(r) + "/collections/" + url.PathEscape(c.ID) + "/items"
	self := base
	if len(query) > 0 {
		self += "?" + query.Encode()
	}
	links := []ogcLink{
		{Href: self, Rel: "self", Type: geoJSONType, Title: "This page"},
		{Href: ogcBase(r) + "/collections/" + url.PathEscape(c.ID), Rel: "collection", Type: "application/json", Title: c.Title},
	}
	if len(features) > q.limit {
		features = features[:q.limit]
		next := url.Values{}
		for name, values := range query {
			next[name] = values
		}
		next.Set("cursor", features[len(features)-1].cursor)
		next.Set("limit", strconv.Itoa(q.limit))
		links = append(links, ogcLink{Href: base + "?" + next.Encode(), Rel: "next", Type: geoJSONType, Title: "Next page"})
	}
	if features == nil {
		features = []ogcFeature{}
	}
	w.Header().Set("Content-Type", geoJSONType)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "FeatureCollection", "timeStamp": utc(time.Now().Truncate(time.Second)),
		"numberReturned": len(features), "links": links, "features": features,
	})
}

func (a api) ogcItem(w http.ResponseWriter, r *http.Request) {
	ws, ctx, cancel, ok := a.ogcStart(w, r)
	defer cancel()
	if !ok {
		return
	}
	c, found := ws.collection(chi.URLParam(r, "collection"))
	id, err := strconv.ParseInt(chi.URLParam(r, "feature"), 10, 64)
	if !found || err != nil || id < 1 {
		writeError(w, http.StatusNotFound, "Feature not found.")
		return
	}
	for name := range r.URL.Query() {
		if name != "f" && name != "crs" {
			writeError(w, http.StatusBadRequest, "Unknown query parameter: "+name+".")
			return
		}
	}
	if crs := r.URL.Query().Get("crs"); crs != "" && crs != crs84 {
		writeError(w, http.StatusBadRequest, "Only "+crs84+" is offered for crs.")
		return
	}
	caller := r.Context().Value(identityKey{}).(identity)
	features, err := a.ogcFeatures(ctx, caller.Subject, ws, c, ogcQuery{featureID: &id}, 1)
	if err != nil {
		a.logger.Error("Could not read a GIS link feature", "collection", c.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "Could not load the feature.")
		return
	}
	if len(features) == 0 {
		writeError(w, http.StatusNotFound, "Feature not found.")
		return
	}
	collection := ogcBase(r) + "/collections/" + url.PathEscape(c.ID)
	feature := features[0]
	feature.Links = []ogcLink{
		{Href: collection + "/items/" + strconv.FormatInt(id, 10), Rel: "self", Type: geoJSONType, Title: "This feature"},
		{Href: collection, Rel: "collection", Type: "application/json", Title: c.Title},
	}
	w.Header().Set("Content-Type", geoJSONType)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(feature)
}
