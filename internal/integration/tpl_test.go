//go:build integration

package integration

import (
	"strings"
	"testing"
)

func TestTplEval(t *testing.T) {
	out := runHactl(t, "tpl", "eval", "{{ 1 + 1 }}")
	trimmed := strings.TrimSpace(stripTokenHeader(out))
	if trimmed != "2" {
		t.Errorf("tpl eval '{{ 1 + 1 }}' = %q, want '2'", trimmed)
	}
}

func TestTplEvalStates(t *testing.T) {
	// sun.sun always exists; state should be "above_horizon" or "below_horizon"
	out := runHactl(t, "tpl", "eval", "{{ states('sun.sun') }}")
	trimmed := strings.TrimSpace(stripTokenHeader(out))
	if trimmed != "above_horizon" && trimmed != "below_horizon" {
		t.Errorf("tpl eval states('sun.sun') = %q, want above_horizon or below_horizon", trimmed)
	}
}

func TestTplEvalFilter(t *testing.T) {
	out := runHactl(t, "tpl", "eval", "{{ 3.14159 | round(2) }}")
	trimmed := strings.TrimSpace(stripTokenHeader(out))
	if trimmed != "3.14" {
		t.Errorf("tpl eval round = %q, want '3.14'", trimmed)
	}
}
