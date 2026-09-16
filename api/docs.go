package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"gopkg.in/yaml.v3"
)

// openAPISpec is the hand-written API description. It lives in this package
// directory (rather than the repo root) so go:embed can reach it.
//
//go:embed openapi.yaml
var openAPISpec []byte

// openAPIJSON converts the spec once, on first request. A conversion failure
// is a build-time bug (see docs_test.go), so it is cached rather than retried.
var openAPIJSON = sync.OnceValues(func() ([]byte, error) {
	return yamlToJSON(openAPISpec)
})

// yamlToJSON re-encodes a YAML document as JSON. yaml.v3 decodes mappings with
// all-string keys into map[string]any, which encoding/json can marshal; a
// non-string key (an unquoted `200:` response code, say) would fail here.
func yamlToJSON(src []byte) ([]byte, error) {
	var doc any
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("parse openapi.yaml: %w", err)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode openapi.json: %w", err)
	}
	return out, nil
}

func handleOpenAPIYAML(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(openAPISpec)
}

func handleOpenAPIJSON(w http.ResponseWriter, r *http.Request) {
	data, err := openAPIJSON()
	if err != nil {
		log.Printf("places: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

const docsHTML = `<!DOCTYPE html>
<html>
<head>
  <title>Places API Docs</title>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link href="https://fonts.googleapis.com/css?family=Montserrat:300,400,700|Roboto:300,400,700" rel="stylesheet">
  <style>body { margin: 0; padding: 0; }</style>
</head>
<body>
  <redoc spec-url="/api/openapi.yaml"></redoc>
  <script src="https://cdn.redoc.ly/redoc/latest/bundles/redoc.standalone.js"></script>
</body>
</html>
`

func handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(docsHTML))
}
