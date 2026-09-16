package main

import (
	"encoding/json"
	"testing"
)

// TestOpenAPISpecConvertsToJSON guards /api/openapi.json: the embedded YAML
// must parse and re-encode, which fails on any non-string mapping key.
func TestOpenAPISpecConvertsToJSON(t *testing.T) {
	data, err := yamlToJSON(openAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q, want 3.1.0", doc.OpenAPI)
	}
	for _, p := range []string{
		"/health", "/api/health", "/api/tags",
		"/api/places", "/api/places/slug/{slug}", "/api/places/{id}",
		"/api/trips", "/api/trips/slug/{slug}", "/api/trips/{id}",
		"/api/trips/{id}/places", "/api/trips/{id}/places/{placeId}",
	} {
		if _, ok := doc.Paths[p]; !ok {
			t.Errorf("spec is missing path %s", p)
		}
	}
}
