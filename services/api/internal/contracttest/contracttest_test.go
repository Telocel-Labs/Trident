package contracttest

import (
	"net/http"
	"testing"
)

// TestLoadSpec_Valid is the baseline check that api/openapi.yaml itself is
// well-formed and internally consistent (issue #242) — every other contract
// test depends on this succeeding.
func TestLoadSpec_Valid(t *testing.T) {
	doc := LoadSpec(t)
	if doc.Info == nil || doc.Info.Title == "" {
		t.Fatal("loaded spec has no info.title")
	}
	if _, ok := doc.Paths.Map()["/v1/events"]; !ok {
		t.Fatal("loaded spec is missing /v1/events")
	}
}

// TestNewRouter_ResolvesKnownRoute verifies the router can match a
// documented path/method pair.
func TestNewRouter_ResolvesKnownRoute(t *testing.T) {
	doc := LoadSpec(t)
	router := NewRouter(t, doc)

	req, err := http.NewRequest(http.MethodGet, "http://localhost:3000/v1/events", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	route, _, err := router.FindRoute(req)
	if err != nil {
		t.Fatalf("FindRoute(GET /v1/events): %v", err)
	}
	if route.Operation.OperationID != "listEvents" {
		t.Errorf("want operationId listEvents, got %s", route.Operation.OperationID)
	}
}
