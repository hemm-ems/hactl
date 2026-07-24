//go:build companion

package companiontest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/hemm-ems/hactl/internal/companion"
)

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	// Find the spec file relative to the test
	candidates := []string{
		filepath.Join("..", "..", "testdata", "companion-v1.yaml"),
		filepath.Join("testdata", "companion-v1.yaml"),
	}
	var specPath string
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if _, statErr := os.Stat(abs); statErr == nil {
			specPath = abs
			break
		}
	}
	if specPath == "" {
		t.Fatal("companion-v1.yaml not found in testdata")
	}

	loader := openapi3.NewLoader()
	spec, err := loader.LoadFromFile(specPath)
	if err != nil {
		t.Fatalf("loading spec: %v", err)
	}
	return spec
}

func TestOpenAPISpecValid(t *testing.T) {
	spec := loadSpec(t)
	if err := spec.Validate(context.Background()); err != nil {
		t.Fatalf("spec validation failed: %v", err)
	}
}

// clientEndpoints is every (method, path) the hactl companion client calls. It
// is derived from the single companion.Endpoints table (which also drives the
// unit-tier field-conformance sweep, contract_conformance_test.go) rather than
// hand-maintained here — one list, so a new client call is covered by both
// sweeps and cannot be added to one and forgotten in the other (TC-7).
var clientEndpoints = func() []struct{ method, path string } {
	out := make([]struct{ method, path string }, 0, len(companion.Endpoints))
	for _, ep := range companion.Endpoints {
		out = append(out, struct{ method, path string }{ep.Method, ep.Path})
	}
	return out
}()

func operationFor(pathItem *openapi3.PathItem, method string) *openapi3.Operation {
	switch method {
	case "GET":
		return pathItem.Get
	case "POST":
		return pathItem.Post
	case "PUT":
		return pathItem.Put
	case "DELETE":
		return pathItem.Delete
	default:
		return nil
	}
}

// TestClientEndpointsInSpec asserts every operation the CLI calls is documented.
func TestClientEndpointsInSpec(t *testing.T) {
	spec := loadSpec(t)
	for _, ep := range clientEndpoints {
		pathItem := spec.Paths.Find(ep.path)
		if pathItem == nil {
			t.Errorf("path %s missing from OpenAPI spec", ep.path)
			continue
		}
		if operationFor(pathItem, ep.method) == nil {
			t.Errorf("path %s has no %s operation in spec", ep.path, ep.method)
		}
	}
}

// TestSpecPathCountMatchesClient derives the expected path count from the client
// endpoint list rather than a hardcoded number, so drift in either direction
// (a spec path the client doesn't use, or a client path missing from the spec)
// is caught after `make sync-spec`.
func TestSpecPathCountMatchesClient(t *testing.T) {
	spec := loadSpec(t)
	uniquePaths := map[string]bool{}
	for _, ep := range clientEndpoints {
		uniquePaths[ep.path] = true
	}
	if got, want := spec.Paths.Len(), len(uniquePaths); got != want {
		t.Errorf("spec has %d paths, client covers %d — vendored spec may be stale (run: make sync-spec)", got, want)
	}
}
