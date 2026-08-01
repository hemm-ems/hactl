package cmd

import (
	"errors"
	"testing"

	"github.com/hemm-ems/hactl/pkg/ids"
)

// WP9 — `trace show` and the identifier contract it was already named in.
//
// Live-fire #66. docs/manual.md's automation paragraph says: "every command
// that takes an automation — auto show|cat|diff|apply|delete|rollback, trace
// show — accepts any of its interchangeable names: the config id:, the alias,
// the entity_id, or the entity_id's object id". All four were refused, with
// `invalid trace ID format`. D-1 decided that pole in July and H-17 asserts it;
// the surface that enforces it derived its membership from the PARAMETER NAME
// of each command entrypoint, and this one's is `traceID`.

// TestResolveTraceIDSeparatesAddressFromReference — the distinction the fix
// rests on.
//
// A trace address names a RUN and can be wrong (an unknown trc: id is an
// error). Anything in neither address form is not malformed: it is an
// automation reference, and the caller resolves it. Conflating the two is what
// made "invalid" the answer to four valid identifiers.
func TestResolveTraceIDSeparatesAddressFromReference(t *testing.T) {
	reg := ids.NewRegistry(t.TempDir() + "/ids.json")

	for _, ref := range []string{
		"24v_booster_schalten",            // object id
		"automation.24v_booster_schalten", // entity_id
		"1729607981113",                   // config id
		"24V Booster schalten",            // alias
	} {
		_, _, _, err := resolveTraceID(reg, ref)
		if !errors.Is(err, errNotATraceReference) {
			t.Errorf("resolveTraceID(%q) = %v, want the sentinel that sends it to the automation resolver", ref, err)
		}
	}

	// A composite key is an address and resolves as one.
	domain, itemID, runID, err := resolveTraceID(reg, "automation.abc/run-1")
	if err != nil {
		t.Fatalf("a composite key must resolve: %v", err)
	}
	if domain != "automation" || itemID != "abc" || runID != "run-1" {
		t.Errorf("composite key parsed as %q/%q/%q", domain, itemID, runID)
	}

	// A trc: id IS an address, so an unknown one is an error rather than a
	// reference to look up: it named a run, and that run is not there.
	if _, _, _, err := resolveTraceID(reg, "trc:nope"); err == nil || errors.Is(err, errNotATraceReference) {
		t.Errorf("an unknown trc: id must stay a hard error, got %v", err)
	}
}
