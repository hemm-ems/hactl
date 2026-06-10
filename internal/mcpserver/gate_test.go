package mcpserver

import (
	"strings"
	"testing"
)

func TestGate(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		allowWrites bool
		want        Decision
	}{
		{"read allowed read-only", "hactl ent ls", false, Allowed},
		{"read allowed with writes", "hactl ent ls", true, Allowed},
		{"rtfm allowed", "hactl rtfm", false, Allowed},
		{"write blocked read-only", "hactl svc call", false, BlockedReadOnly},
		{"write allowed with flag", "hactl svc call", true, Allowed},
		{"auto apply blocked read-only", "hactl auto apply", false, BlockedReadOnly},
		{"auto apply allowed with flag", "hactl auto apply", true, Allowed},
		{"setup blocked always", "hactl setup", false, BlockedAlways},
		{"setup blocked despite writes", "hactl setup", true, BlockedAlways},
		{"mcp blocked always", "hactl mcp", true, BlockedAlways},
		{"group allowed", "hactl ent", false, Allowed},
		{"root allowed", "hactl", false, Allowed},
		{"unknown blocked", "hactl frobnicate", true, BlockedUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := Gate(tt.path, tt.allowWrites)
			if got != tt.want {
				t.Errorf("Gate(%q, %v) = %v, want %v", tt.path, tt.allowWrites, got, tt.want)
			}
			if got != Allowed && reason == "" {
				t.Errorf("Gate(%q, %v) blocked without a reason", tt.path, tt.allowWrites)
			}
		})
	}
}

func TestGateReadOnlyReasonNamesFlag(t *testing.T) {
	_, reason := Gate("hactl svc call", false)
	if !strings.Contains(reason, "--allow-writes") {
		t.Errorf("read-only block reason should mention --allow-writes, got %q", reason)
	}
}

// TestGateSetsDisjoint ensures no command path is classified twice; the
// Gate switch would silently shadow the later set.
func TestGateSetsDisjoint(t *testing.T) {
	seen := map[string]string{}
	for set, paths := range ClassifiedPaths() {
		for _, p := range paths {
			if prev, dup := seen[p]; dup {
				t.Errorf("path %q classified in both %q and %q", p, prev, set)
			}
			seen[p] = set
		}
	}
}
