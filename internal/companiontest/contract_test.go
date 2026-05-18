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

func TestAllSpecPathsCovered(t *testing.T) {
	spec := loadSpec(t)

	expectedPaths := []string{
		"/v1/health",
		"/v1/config/files",
		"/v1/config/file",
		"/v1/config/block",
		"/v1/config/templates",
		"/v1/config/template",
		"/v1/config/scripts",
		"/v1/config/script",
		"/v1/config/automations",
		"/v1/config/automation",
	}

	for _, p := range expectedPaths {
		if spec.Paths.Find(p) == nil {
			t.Errorf("path %s missing from OpenAPI spec", p)
		}
	}

	// Verify no unexpected paths
	if spec.Paths.Len() != len(expectedPaths) {
		t.Errorf("spec has %d paths, want %d", spec.Paths.Len(), len(expectedPaths))
	}
}

func TestSpecEndpointMethods(t *testing.T) {
	spec := loadSpec(t)

	cases := []struct {
		path   string
		method string
	}{
		{"/v1/health", "GET"},
		{"/v1/config/files", "GET"},
		{"/v1/config/file", "GET"},
		{"/v1/config/file", "PUT"},
		{"/v1/config/block", "GET"},
		{"/v1/config/templates", "GET"},
		{"/v1/config/template", "GET"},
		{"/v1/config/template", "PUT"},
		{"/v1/config/template", "POST"},
		{"/v1/config/template", "DELETE"},
		{"/v1/config/scripts", "GET"},
		{"/v1/config/script", "GET"},
		{"/v1/config/script", "PUT"},
		{"/v1/config/script", "POST"},
		{"/v1/config/script", "DELETE"},
		{"/v1/config/automations", "GET"},
		{"/v1/config/automation", "GET"},
		{"/v1/config/automation", "PUT"},
		{"/v1/config/automation", "POST"},
		{"/v1/config/automation", "DELETE"},
	}

	for _, tc := range cases {
		pathItem := spec.Paths.Find(tc.path)
		if pathItem == nil {
			t.Errorf("path %s not found in spec", tc.path)
			continue
		}
		var op *openapi3.Operation
		switch tc.method {
		case "GET":
			op = pathItem.Get
		case "POST":
			op = pathItem.Post
		case "PUT":
			op = pathItem.Put
		case "DELETE":
			op = pathItem.Delete
		}
		if op == nil {
			t.Errorf("path %s has no %s operation", tc.path, tc.method)
		}
	}
}
