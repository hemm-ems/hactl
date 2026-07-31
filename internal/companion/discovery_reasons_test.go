package companion

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// H-24, quantified. The rule is that a connectivity answer names the cause the
// transport reported — and the causes are a closed set, so the rule is checkable
// over all of them rather than over the ones a fix remembered.
//
// #75 is what a missing row looks like: an authentication failure had no reason
// of its own, fell through to `unreachable`, and was answered with "Check
// Ingress / network, or set COMPANION_URL in .env" — a remediation that cannot
// work, for a cause the same command's `ws_error` field had already named
// correctly.

// TestDiscoveryReasonsMatchTheConstBlock is the derivation. Without it
// DiscoveryReasons() is a hand-list, and a hand-list is what every other gate in
// this repository exists because of: the seventh reason would be added to the
// const block, missed here, and silently exempt from both checks below.
func TestDiscoveryReasonsMatchTheConstBlock(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "discovery.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing discovery.go: %v", err)
	}

	var declared []string
	ast.Inspect(f, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || spec.Type == nil {
			return true
		}
		if id, isID := spec.Type.(*ast.Ident); !isID || id.Name != "DiscoveryReason" {
			return true
		}
		for _, name := range spec.Names {
			declared = append(declared, name.Name)
		}
		return true
	})
	if len(declared) == 0 {
		t.Fatal("no DiscoveryReason constants found — this test has stopped reading the source it audits")
	}

	listed := make([]string, 0, len(DiscoveryReasons()))
	for _, r := range DiscoveryReasons() {
		listed = append(listed, reasonConstName(r))
	}
	slices.Sort(declared)
	slices.Sort(listed)
	if !slices.Equal(declared, listed) {
		t.Errorf("DiscoveryReasons() = %v; the const block declares %v", listed, declared)
	}
}

// reasonConstName maps a reason value back to its constant name, so the
// comparison above is against the identifiers in the source rather than against
// the strings, which is the half a rename would not disturb.
func reasonConstName(r DiscoveryReason) string {
	switch r {
	case ReasonAuthDenied:
		return "ReasonAuthDenied"
	case ReasonAuthInvalid:
		return "ReasonAuthInvalid"
	case ReasonAddonMissing:
		return "ReasonAddonMissing"
	case ReasonUnreachable:
		return "ReasonUnreachable"
	case ReasonRedirected:
		return "ReasonRedirected"
	case ReasonProtocolMismatch:
		return "ReasonProtocolMismatch"
	}
	return "unmapped:" + string(r)
}

// TestEveryDiscoveryReasonNamesItsOwnFix — no reason may answer with another
// reason's remediation.
//
// The fallback text is the one #75 was reported against, so the assertion is
// that only ReasonUnreachable produces it: every other reason that does has,
// by construction, told the reader to check a network that is fine.
func TestEveryDiscoveryReasonNamesItsOwnFix(t *testing.T) {
	fallback := newDiscoveryError(ReasonUnreachable, nil).Error()
	seen := map[string]DiscoveryReason{}
	for _, r := range DiscoveryReasons() {
		hint := newDiscoveryError(r, nil).Error()
		if r != ReasonUnreachable && hint == fallback {
			t.Errorf("%q falls through to the unreachable hint, which tells the reader to check their network", r)
		}
		if prev, dup := seen[hint]; dup {
			t.Errorf("%q and %q give the same remediation", r, prev)
		}
		seen[hint] = r
		if !strings.Contains(hint, "Fix:") {
			t.Errorf("%q names no next step:\n%s", r, hint)
		}
	}
}
