package handler

import (
	_ "embed"
	"net/http"
)

// The workflow API's OpenAPI 3.1 spec, served as a static document so
// integrators can point tooling straight at a deployed instance. The spec is
// documentation, not the runtime contract — response parsing on clients still
// goes through lenient schemas per the API compatibility rules.
//
//go:embed workflow_openapi.yaml
var workflowOpenAPISpec []byte

// WorkflowOpenAPISpec serves GET /api/workflows/openapi.yaml. Public on
// purpose: the document contains no secrets and spec tooling rarely supports
// custom auth headers for the initial fetch.
func (h *Handler) WorkflowOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(workflowOpenAPISpec)
}
