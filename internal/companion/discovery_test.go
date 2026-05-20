package companion

import (
	"context"
	"testing"

	"github.com/hemm-ems/hactl/internal/config"
)

func TestDiscover_ExplicitURL(t *testing.T) {
	cfg := &config.Config{
		URL:          "http://homeassistant.local",
		CompanionURL: "http://companion.local",
	}
	got, err := Discover(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("Discover with explicit URL returned error: %v", err)
	}
	if got != "http://companion.local" {
		t.Errorf("Discover = %q, want %q", got, "http://companion.local")
	}
}

func TestDiscover_NilWSNoURL(t *testing.T) {
	cfg := &config.Config{
		URL:          "http://homeassistant.local",
		CompanionURL: "",
	}
	_, err := Discover(context.Background(), cfg, nil)
	if err == nil {
		t.Fatal("Discover with nil ws and no CompanionURL should return error")
	}
}
