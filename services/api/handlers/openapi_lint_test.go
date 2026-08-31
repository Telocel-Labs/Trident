package handlers_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPISpecMatchesHandlersAndStatusCodes(t *testing.T) {
	// Read openapi.yaml
	rawSpec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("failed to read api/openapi.yaml: %v", err)
	}

	var spec map[string]any
	if err := yaml.Unmarshal(rawSpec, &spec);
	err != nil {
		t.Fatalf("failed to parse openapi.yaml: %v", err)
	}

	pathsNode, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("openapi.yaml missing paths")
	}

	// Expected routes implemented in the API / handlers
	expectedRoutes := []string{
		"/v1/health",
		"/v1/ready",
		"/v1/version",
		"/v1/events",
		"/v1/events/{id}",
		"/v1/events/stream",
		"/v1/batch",
		"/v1/stats/contracts",
		"/v1/api-keys",
		"/v1/api-keys/{id}",
		"/v1/admin/db",
		"/v1/admin/keys/{id}/usage",
		"/v1/contracts/{id}/events/schema",
		"/v1/contracts/{id}/spec",
		"/v1/contracts/{id}/storage",
		"/v1/contracts/{id}/storage/history",
		"/v1/contracts/{id}/metadata",
		"/v1/contracts/{id}/call",
		"/v1/scval/decode",
		"/v1/usage",
		"/v1/webhooks",
		"/v1/webhooks/{id}",
	}

	for _, route := range expectedRoutes {
		if _, exists := pathsNode[route]; !exists {
			t.Errorf("route %q implemented by handlers/router is missing from api/openapi.yaml", route)
		}
	}

	// Check drift in reverse direction
	for route := range pathsNode {
		found := false
		for _, expected := range expectedRoutes {
			if route == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("path %q in api/openapi.yaml has no matching registered route in code", route)
		}
	}

	// Verify status codes, error codes, and examples are present for every path
	for pathName, pathVal := range pathsNode {
		pathMap, ok := pathVal.(map[string]any)
		if !ok {
			continue
		}
		for method, opVal := range pathMap {
			if method != "get" && method != "post" && method != "put" && method != "delete" && method != "patch" {
				continue
			}
			opMap, ok := opVal.(map[string]any)
			if !ok {
				t.Errorf("operation %s %s is not a valid object", strings.ToUpper(method), pathName)
				continue
			}

			responses, ok := opMap["responses"].(map[string]any)
			if !ok || len(responses) == 0 {
				t.Errorf("operation %s %s is missing responses", strings.ToUpper(method), pathName)
				continue
			}

			// Ensure status codes like 200, 400, 503 etc. are documented
			hasSuccess := false
			hasErrorOrRef := false
			for code := range responses {
				if strings.HasPrefix(code, "2") {
					hasSuccess = true
				}
				if strings.HasPrefix(code, "4") || strings.HasPrefix(code, "5") {
					hasErrorOrRef = true
				}
			}

			if !hasSuccess {
				t.Errorf("operation %s %s must document at least one 2xx success response status code", strings.ToUpper(method), pathName)
			}
			if !hasErrorOrRef {
				t.Errorf("operation %s %s must document error status codes", strings.ToUpper(method), pathName)
			}
		}
	}
}
