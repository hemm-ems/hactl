package haapi

import (
	"encoding/json"
	"testing"
)

func TestParseViewSummary_Full(t *testing.T) {
	raw := json.RawMessage(`{
		"title": "Home",
		"path": "home",
		"icon": "mdi:home",
		"type": "sections",
		"cards": [{"type": "tile"}],
		"sections": [{"cards": []}, {"cards": []}],
		"badges": [{"entity": "sensor.temp"}]
	}`)
	s := ParseViewSummary(raw)
	if s.Title != "Home" {
		t.Errorf("Title = %q, want %q", s.Title, "Home")
	}
	if s.Path != "home" {
		t.Errorf("Path = %q, want %q", s.Path, "home")
	}
	if s.Icon != "mdi:home" {
		t.Errorf("Icon = %q, want %q", s.Icon, "mdi:home")
	}
	if s.Type != "sections" {
		t.Errorf("Type = %q, want %q", s.Type, "sections")
	}
	if s.Cards != 1 {
		t.Errorf("Cards = %d, want 1", s.Cards)
	}
	if s.Sections != 2 {
		t.Errorf("Sections = %d, want 2", s.Sections)
	}
	if s.Badges != 1 {
		t.Errorf("Badges = %d, want 1", s.Badges)
	}
}

func TestParseViewSummary_Minimal(t *testing.T) {
	raw := json.RawMessage(`{}`)
	s := ParseViewSummary(raw)
	if s.Title != "" {
		t.Errorf("Title = %q, want empty", s.Title)
	}
	if s.Cards != 0 {
		t.Errorf("Cards = %d, want 0", s.Cards)
	}
}

func TestLovelaceConfig_RoundTrip(t *testing.T) {
	input := `{"views":[{"title":"Test","path":"test","cards":[{"type":"tile","entity":"light.kitchen","custom_field":"preserved"}]}]}`
	var cfg LovelaceConfig
	if err := json.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Views) != 1 {
		t.Fatalf("Views = %d, want 1", len(cfg.Views))
	}

	// Re-marshal and verify unknown fields are preserved
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTripped map[string]any
	if err := json.Unmarshal(out, &roundTripped); err != nil {
		t.Fatalf("unmarshal round-tripped: %v", err)
	}
	views, ok := roundTripped["views"].([]any)
	if !ok {
		t.Fatal("views is not []any")
	}
	view, ok := views[0].(map[string]any)
	if !ok {
		t.Fatal("view is not map[string]any")
	}
	cards, ok := view["cards"].([]any)
	if !ok {
		t.Fatal("cards is not []any")
	}
	card, ok := cards[0].(map[string]any)
	if !ok {
		t.Fatal("card is not map[string]any")
	}
	if card["custom_field"] != "preserved" {
		t.Errorf("custom_field lost during round-trip: %v", card)
	}
}

func TestLovelaceDashboard_JSON(t *testing.T) {
	input := `{"id":"abc","url_path":"my-dash","mode":"storage","title":"My Dash","icon":"mdi:home","require_admin":false,"show_in_sidebar":true}`
	var d LovelaceDashboard
	if err := json.Unmarshal([]byte(input), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.URLPath != "my-dash" {
		t.Errorf("URLPath = %q, want %q", d.URLPath, "my-dash")
	}
	if d.Mode != "storage" {
		t.Errorf("Mode = %q, want %q", d.Mode, "storage")
	}
	if !d.ShowInSidebar {
		t.Error("ShowInSidebar = false, want true")
	}
}
