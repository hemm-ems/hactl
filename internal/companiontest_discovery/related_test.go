//go:build companion_discovery

package companiontest_discovery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/companiontestutil"
	"github.com/hemm-ems/hactl/internal/haapi"
)

func TestRelatedEntityThroughDiscoveredIngress(t *testing.T) {
	fakeSup.SetRequireSession(true)
	defer fakeSup.SetRequireSession(false)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws := haapi.NewWSClient(fakeSup.BaseURL(), "any-token-accepted")
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("WSClient.Connect: %v", err)
	}
	defer ws.Close() //nolint:errcheck

	cfg := minimalConfig(fakeSup.BaseURL())
	url, err := companion.Discover(ctx, cfg, ws)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	plain := companion.New(url, "any-token-accepted")
	if _, err := plain.RelatedEntity(ctx, companiontestutil.RelatedSourceEntityID, false); err == nil {
		t.Fatal("plain RelatedEntity unexpectedly succeeded without Ingress auth")
	} else if !strings.Contains(err.Error(), "401") {
		t.Fatalf("plain RelatedEntity error = %v, want 401", err)
	}

	authed := companion.New(url, "any-token-accepted").WithIngressAuth(ws)
	source, err := authed.RelatedEntity(ctx, companiontestutil.RelatedSourceEntityID, false)
	if err != nil {
		t.Fatalf("RelatedEntity source through Ingress: %v", err)
	}
	assertRelatedEntry(t, source.Related,
		companiontestutil.RelatedGeneratedEntityID,
		"config-entry-reference",
		"config_entry="+companiontestutil.RelatedGeneratedConfigEntryID,
	)
	assertRelatedEntry(t, source.Related,
		companiontestutil.RelatedYAMLPeerEntityID,
		"yaml-reference",
		"configuration.yaml",
	)

	reverse, err := authed.RelatedEntity(ctx, companiontestutil.RelatedGeneratedEntityID, false)
	if err != nil {
		t.Fatalf("RelatedEntity reverse through Ingress: %v", err)
	}
	assertRelatedEntry(t, reverse.Related,
		companiontestutil.RelatedSourceEntityID,
		"referenced-entity",
		"config_entry="+companiontestutil.RelatedGeneratedConfigEntryID,
	)
}

func assertRelatedEntry(t *testing.T, entries []companion.RelatedEntityEntry, entityID, relationship, detail string) {
	t.Helper()
	for _, entry := range entries {
		if entry.EntityID == entityID && entry.Relationship == relationship && entry.Detail == detail {
			return
		}
	}
	t.Fatalf("missing related entry entity=%q relationship=%q detail=%q in %+v", entityID, relationship, detail, entries)
}
