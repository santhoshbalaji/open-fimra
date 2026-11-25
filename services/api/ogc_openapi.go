package main

import (
	"net/http"
)

// ogcAPI serves a short OpenAPI 3.0 definition of the GIS link. Desktop GIS
// reads the page size limits from it.
func (a api) ogcAPI(w http.ResponseWriter, r *http.Request) {
	ws, _, cancel, ok := a.ogcStart(w, r)
	defer cancel()
	if !ok {
		return
	}
	param := func(name, in, description string, schema map[string]any) map[string]any {
		p := map[string]any{"name": name, "in": in, "description": description, "schema": schema}
		if in == "path" {
			p["required"] = true
		} else {
			p["required"] = false
			p["style"] = "form"
			p["explode"] = false
		}
		return p
	}
	ref := func(name string) map[string]any {
		return map[string]any{"$ref": "#/components/parameters/" + name}
	}
	json := func(description string) map[string]any {
		return map[string]any{"description": description, "content": map[string]any{
			"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}}
	}
	geojson := func(description string) map[string]any {
		return map[string]any{"description": description, "content": map[string]any{
			geoJSONType: map[string]any{"schema": map[string]any{"type": "object"}}}}
	}
	errors := map[string]any{
		"400": json("A parameter is not valid."), "401": json("A sign-in or a personal access key is required."),
		"403": json("A role in this workspace is required."), "404": json("Not found."),
	}
	responses := func(ok map[string]any) map[string]any {
		out := map[string]any{"200": ok}
		for code, value := range errors {
			out[code] = value
		}
		return out
	}
	get := func(id, summary string, parameters []any, ok map[string]any) map[string]any {
		return map[string]any{"get": map[string]any{"operationId": id, "summary": summary,
			"parameters": append(parameters, ref("f")), "responses": responses(ok)}}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "Open FIMRA GIS link: " + ws.name,
			"version":     "1.0.0",
			"description": "Read-only OGC API - Features. Every answer contains only what the caller can see in the web app.",
			"license":     map[string]any{"name": "AGPL-3.0", "url": "https://www.gnu.org/licenses/agpl-3.0.html"},
		},
		"servers": []any{map[string]any{"url": ogcBase(r)}},
		"security": []any{map[string]any{"accessKeyPassword": []string{}}, map[string]any{"accessKeyHeader": []string{}},
			map[string]any{"signIn": []string{}}},
		"paths": map[string]any{
			"/":            get("getLandingPage", "Landing page", []any{}, json("Links to the rest of the service.")),
			"/conformance": get("getConformance", "Conformance classes", []any{}, json("The conformance classes.")),
			"/api":         get("getAPI", "This API definition", []any{}, json("The API definition.")),
			"/collections": get("getCollections", "Collections", []any{}, json("The collections you can read.")),
			"/collections/{collectionId}": get("describeCollection", "One collection",
				[]any{ref("collectionId")}, json("The collection.")),
			"/collections/{collectionId}/items": get("getFeatures", "Features of a collection",
				[]any{ref("collectionId"), ref("limit"), ref("bbox"), ref("datetime"), ref("cursor"), ref("forest_class"),
					ref("queue"), ref("step"), ref("crs"), ref("bbox-crs")},
				geojson("A page of features. A next link continues it.")),
			"/collections/{collectionId}/items/{featureId}": get("getFeature", "One feature",
				[]any{ref("collectionId"), ref("featureId"), ref("crs")}, geojson("The feature.")),
		},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"accessKeyPassword": map[string]any{"type": "http", "scheme": "basic",
					"description": "Any user name, with a personal access key as the password."},
				"accessKeyHeader": map[string]any{"type": "apiKey", "in": "header", "name": accessKeyHeader,
					"description": "A personal access key."},
				"signIn": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "JWT",
					"description": "An access token from the workspace's sign-in server."},
			},
			"parameters": map[string]any{
				"collectionId": param("collectionId", "path", "The collection ID.", map[string]any{"type": "string"}),
				"featureId":    param("featureId", "path", "The feature ID.", map[string]any{"type": "integer", "minimum": 1}),
				"limit": param("limit", "query", "Features on one page. Larger values are reduced to the maximum.",
					map[string]any{"type": "integer", "minimum": 1, "maximum": ogcMaxLimit, "default": ogcDefaultLimit}),
				"bbox": param("bbox", "query", "Only features that meet west,south,east,north in degrees.",
					map[string]any{"type": "array", "minItems": 4, "maxItems": 6, "items": map[string]any{"type": "number"}}),
				"datetime": param("datetime", "query", "An RFC 3339 time or interval, such as 2026-03-01T00:00:00Z/.., on observation time. Layers and units ignore it.",
					map[string]any{"type": "string"}),
				"cursor": param("cursor", "query", "Continues from a next link.", map[string]any{"type": "string"}),
				"forest_class": param("forest_class", "query", "Detections and alerts only: forest classes, separated by commas.",
					map[string]any{"type": "array", "items": map[string]any{"type": "string",
						"enum": []string{"inside", "near_edge", "outside", "fixed_heat_source"}}}),
				"queue": param("queue", "query", "Alerts only: one queue.",
					map[string]any{"type": "string", "enum": []string{"unit", "edge_review", "triage"}}),
				"step": param("step", "query", "Alerts only: one current workflow step, such as new.", map[string]any{"type": "string"}),
				"crs":  param("crs", "query", "Only CRS84 is offered.", map[string]any{"type": "string", "enum": []string{crs84}}),
				"bbox-crs": param("bbox-crs", "query", "Only CRS84 is offered.",
					map[string]any{"type": "string", "enum": []string{crs84}}),
				"f": param("f", "query", "Only JSON is offered.", map[string]any{"type": "string", "enum": []string{"json", "geojson"}}),
			},
		},
	})
}
