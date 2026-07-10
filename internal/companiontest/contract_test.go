//go:build companion

package companiontest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
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

// clientEndpoints is every (method, path) the hactl companion client calls
// (internal/companion/client.go). The vendored spec must document each — this is
// the real cross-repo contract, and it is maintained here rather than as a
// hardcoded path count, so adding a client call without a spec entry fails.
var clientEndpoints = []struct{ method, path string }{
	{"GET", "/v1/health"},
	{"GET", "/v1/status"},
	{"GET", "/v1/config/files"},
	{"GET", "/v1/config/file"},
	{"PUT", "/v1/config/file"},
	{"GET", "/v1/config/block"},
	{"GET", "/v1/related/entity"},
	{"GET", "/v1/ref/scan"},
	{"GET", "/v1/ref/entities"},
	{"POST", "/v1/ref/replace"},
	{"GET", "/v1/config/templates"},
	{"GET", "/v1/config/template"},
	{"PUT", "/v1/config/template"},
	{"POST", "/v1/config/template"},
	{"DELETE", "/v1/config/template"},
	{"GET", "/v1/config/scripts"},
	{"GET", "/v1/config/script"},
	{"PUT", "/v1/config/script"},
	{"POST", "/v1/config/script"},
	{"DELETE", "/v1/config/script"},
	{"GET", "/v1/config/automations"},
	{"GET", "/v1/config/automation"},
	{"PUT", "/v1/config/automation"},
	{"POST", "/v1/config/automation"},
	{"DELETE", "/v1/config/automation"},
	{"GET", "/v1/config/helpers"},
	{"GET", "/v1/config/helper"},
	{"POST", "/v1/config/helper"},
	{"PUT", "/v1/config/helper"},
	{"DELETE", "/v1/config/helper"},
	{"POST", "/v1/ha/reload/{domain}"},
	{"POST", "/v1/ha/check-config"},
	{"POST", "/v1/wireguard/config"},
	{"POST", "/v1/wireguard/start"},
	{"POST", "/v1/wireguard/stop"},
	{"GET", "/v1/wireguard/status"},
	{"GET", "/v1/logs"},
}

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
