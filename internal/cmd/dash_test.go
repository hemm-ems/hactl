package cmd

import (
	"encoding/json"
	"testing"
)

func TestViewType_Default(t *testing.T) {
	if got := viewType(""); got != "masonry" {
		t.Errorf("viewType('') = %q, want %q", got, "masonry")
	}
}

func TestViewType_Explicit(t *testing.T) {
	if got := viewType("sections"); got != "sections" {
		t.Errorf("viewType('sections') = %q, want %q", got, "sections")
	}
}

func TestDashDisplayPath_Default(t *testing.T) {
	if got := dashDisplayPath(""); got != "(default)" {
		t.Errorf("dashDisplayPath('') = %q, want %q", got, "(default)")
	}
}

func TestDashDisplayPath_Named(t *testing.T) {
	if got := dashDisplayPath("my-dash"); got != "my-dash" {
		t.Errorf("dashDisplayPath('my-dash') = %q, want %q", got, "my-dash")
	}
}

func TestSelectDashboardViewRaw_PathFirst(t *testing.T) {
	raw := json.RawMessage(`{"views":[{"title":"Alpha","path":"alpha","cards":[]},{"title":"Alpha","path":"beta","cards":[{"type":"markdown","content":"beta only"}]}]}`)
	selected, err := selectDashboardViewRaw(raw, "beta")
	if err != nil {
		t.Fatalf("selectDashboardViewRaw: %v", err)
	}
	if string(selected) == "" {
		t.Fatal("selected view is empty")
	}
	if got := string(selected); !json.Valid(selected) || got == string(raw) {
		t.Errorf("selected = %s, want one valid view object", got)
	}
	var view map[string]any
	if err := json.Unmarshal(selected, &view); err != nil {
		t.Fatalf("unmarshal selected: %v", err)
	}
	if view["path"] != "beta" {
		t.Errorf("path = %v, want beta", view["path"])
	}
}

func TestSelectDashboardViewRaw_TitleFallback(t *testing.T) {
	raw := json.RawMessage(`{"views":[{"title":"Alpha","path":"alpha","cards":[]},{"title":"Beta","path":"beta","cards":[]}]}`)
	selected, err := selectDashboardViewRaw(raw, "Beta")
	if err != nil {
		t.Fatalf("selectDashboardViewRaw: %v", err)
	}
	var view map[string]any
	if err := json.Unmarshal(selected, &view); err != nil {
		t.Fatalf("unmarshal selected: %v", err)
	}
	if view["path"] != "beta" {
		t.Errorf("path = %v, want beta", view["path"])
	}
}

func TestSelectDashboardViewRaw_NotFound(t *testing.T) {
	raw := json.RawMessage(`{"views":[{"title":"Alpha","path":"alpha","cards":[]}]}`)
	if _, err := selectDashboardViewRaw(raw, "missing"); err == nil {
		t.Fatal("expected not found error")
	}
}
