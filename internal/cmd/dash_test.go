package cmd

import "testing"

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
