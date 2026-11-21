package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// TestPolicyStoredConfigFallsBackToHA — D-207's stored-config read: the
// companion first; when it cannot be connected, Home Assistant's config API
// (a script's bare body keyed by the script, as the companion keys it); a 404
// there is a config only the companion can read, and stays an error; an error
// of a connected companion is not retried on the config API.
func TestPolicyStoredConfigFallsBackToHA(t *testing.T) {
	var haHits []string
	ha := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		haHits = append(haHits, r.URL.Path)
		switch r.URL.Path {
		case "/api/config/script/config/goodnight":
			_, _ = w.Write([]byte(`{"alias":"Goodnight","sequence":[{"action":"lock.lock","target":{"entity_id":"lock.front_door"}}]}`))
		case "/api/config/automation/config/night_lockup":
			_, _ = w.Write([]byte(`{"id":"night_lockup","actions":[{"action":"lock.lock"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Resource not found"}`))
		}
	}))
	defer ha.Close()
	comp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/config/script":
			_, _ = w.Write([]byte(`{"id":"pkg_script","content":"pkg_script:\n  sequence: []\n"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		}
	}))
	defer comp.Close()
	noComp := func(context.Context) (*companion.Client, error) {
		return nil, &companion.DiscoveryError{Reason: companion.ReasonAddonMissing}
	}
	withComp := func(context.Context) (*companion.Client, error) { return companion.New(comp.URL, "t"), nil }
	haClient := func() *haapi.Client { return haapi.New(ha.URL, "t") }

	ctx := context.Background()
	read := policyStoredConfig(noComp, haClient)
	script, scriptErr := read(ctx, "script", "goodnight")
	if scriptErr != nil {
		t.Fatalf("script without a companion: %v", scriptErr)
	}
	var doc map[string]any
	if yerr := yaml.Unmarshal(script, &doc); yerr != nil {
		t.Fatal(yerr)
	}
	if inner, ok := doc["goodnight"].(map[string]any); !ok || inner["sequence"] == nil {
		t.Errorf("the script's body is not keyed by the script: %s", script)
	}
	if auto, autoErr := read(ctx, "automation", "night_lockup"); autoErr != nil || !strings.Contains(string(auto), `"id":"night_lockup"`) {
		t.Errorf("automation without a companion: %s, %v", auto, autoErr)
	}
	for _, c := range []struct{ kind, id string }{{"script", "pkg_script"}, {"automation", "pkg_auto"}} {
		_, pkgErr := read(ctx, c.kind, c.id)
		if pkgErr == nil || !strings.Contains(pkgErr.Error(), "is not readable without the companion") || !strings.Contains(pkgErr.Error(), c.kind+"s.yaml") {
			t.Errorf("%s %s defined in a package: %v, want not readable without the companion", c.kind, c.id, pkgErr)
		}
	}
	if _, noHAErr := policyStoredConfig(noComp, func() *haapi.Client { return nil })(ctx, "script", "goodnight"); noHAErr == nil {
		t.Error("no companion and no HA connection: read, want an error")
	}

	haHits = nil
	read = policyStoredConfig(withComp, haClient)
	if viaComp, compErr := read(ctx, "script", "pkg_script"); compErr != nil || !strings.Contains(string(viaComp), "pkg_script:") {
		t.Errorf("with a companion: %s, %v", viaComp, compErr)
	}
	if _, compErr := read(ctx, "automation", "night_lockup"); compErr == nil {
		t.Error("a connected companion's error was not an error")
	}
	if len(haHits) != 0 {
		t.Errorf("with a companion, Home Assistant's config API was asked: %v", haHits)
	}
	if _, kindErr := read(ctx, "scene", "x"); kindErr == nil {
		t.Error("an unknown kind: read, want an error")
	}
}
