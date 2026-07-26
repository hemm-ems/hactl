package degeneracy_test

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/degeneracy"
)

// These pin the behaviour every Identity declaration in the tree relies on. The
// package had none: it was committed with a doc comment claiming a sweep test
// that did not exist.

// record stands in for a decoded wire record with an identity (an id) and an
// optional field (a note) that is legitimately empty.
type record struct {
	ID   string `json:"id"`
	Note string `json:"note"`
}

func (r *record) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "id", Value: &r.ID}}
}

// conditional stands in for ValidateResult: it only has an identity when it
// claims something went wrong.
type conditional struct {
	Error string `json:"error"`
	OK    bool   `json:"ok"`
}

func (c *conditional) Identity() []degeneracy.Field {
	if c.OK {
		return nil
	}
	return []degeneracy.Field{{Name: "error", Value: &c.Error}}
}

// nested holds records the way a real payload does: behind a slice, a map and a
// pointer.
type nested struct {
	ByKey map[string]record `json:"by_key"`
	Ptr   *record           `json:"ptr"`
	List  []record          `json:"list"`
}

// selfRef exists only to prove the walk terminates on a cyclic type.
type selfRef struct {
	Next *selfRef `json:"next"`
	Rec  record   `json:"rec"`
}

func TestCheck_PoisonsMissingIdentityAndErrors(t *testing.T) {
	recs := []record{{ID: "sensor.real"}, {Note: "no id here"}}

	err := degeneracy.Check("/api/states", &recs)
	if err == nil {
		t.Fatal("a record with no id decoded without complaint")
	}
	if !strings.Contains(err.Error(), degeneracy.Marker) {
		t.Errorf("error does not carry the marker, so the harness scan cannot see it: %v", err)
	}
	if !strings.Contains(err.Error(), "/api/states") {
		t.Errorf("error does not name the wire source: %v", err)
	}
	if recs[1].ID != degeneracy.Marker {
		t.Errorf("identity-less record was not poisoned in place: %q", recs[1].ID)
	}
	if recs[0].ID != "sensor.real" {
		t.Errorf("a record with an identity was modified: %q", recs[0].ID)
	}
	if recs[1].Note != "no id here" {
		t.Errorf("Check touched a non-identity field: %q", recs[1].Note)
	}
}

func TestCheck_EmptyAndNilAreLegitimateAnswers(t *testing.T) {
	var nilSlice []record
	if err := degeneracy.Check("trace/list", &nilSlice); err != nil {
		t.Errorf("a nil slice is 'nothing found', not a degenerate decode: %v", err)
	}

	empty := []record{}
	if err := degeneracy.Check("trace/list", &empty); err != nil {
		t.Errorf("an empty list is a legitimate answer: %v", err)
	}

	optional := []record{{ID: "sensor.real"}}
	if err := degeneracy.Check("trace/list", &optional); err != nil {
		t.Errorf("an empty optional field must not be poisoned: %v", err)
	}
	if optional[0].Note != "" {
		t.Errorf("a legitimately empty field was poisoned: %q", optional[0].Note)
	}
}

func TestCheck_ConditionalIdentityOnlyAppliesWhenItClaimsFailure(t *testing.T) {
	ok := conditional{OK: true}
	if err := degeneracy.Check("validate_config", &ok); err != nil {
		t.Errorf("a valid result has nothing that could have gone missing: %v", err)
	}

	bad := conditional{OK: false}
	if err := degeneracy.Check("validate_config", &bad); err == nil {
		t.Fatal("invalid with no error message is an unparsed verdict, not a verdict")
	}
	if bad.Error != degeneracy.Marker {
		t.Errorf("the missing reason was not poisoned: %q", bad.Error)
	}
}

func TestCheck_ReachesRecordsThroughSlicesMapsAndPointers(t *testing.T) {
	n := nested{
		List:  []record{{}},
		ByKey: map[string]record{"a": {}},
		Ptr:   &record{},
	}

	err := degeneracy.Check("companion /v1/everything", &n)
	if err == nil {
		t.Fatal("nested identity-less records decoded without complaint")
	}
	if n.List[0].ID != degeneracy.Marker {
		t.Errorf("slice element not poisoned: %q", n.List[0].ID)
	}
	if n.ByKey["a"].ID != degeneracy.Marker {
		// Map values are not addressable; Check has to write the poisoned copy
		// back or the renderer prints the clean original.
		t.Errorf("map value not poisoned: %q", n.ByKey["a"].ID)
	}
	if n.Ptr.ID != degeneracy.Marker {
		t.Errorf("pointer target not poisoned: %q", n.Ptr.ID)
	}
	if got := strings.Count(err.Error(), "3 of 3"); got != 1 {
		t.Errorf("error should account for all three records, got: %v", err)
	}
}

func TestCheck_DetectsThroughANonPointerValue(t *testing.T) {
	// Poisoning a copy cannot reach the caller, but detection must not
	// silently degrade to a no-op just because the caller passed a value.
	if err := degeneracy.Check("lovelace/info", record{}); err == nil {
		t.Fatal("a value argument silently skipped the check")
	}
}

func TestCheck_TerminatesOnASelfReferentialType(t *testing.T) {
	// A cyclic wire type must not hang the command. The bound is the point;
	// reaching it is not an error.
	loop := &selfRef{}
	loop.Next = loop

	done := make(chan error, 1)
	go func() { done <- degeneracy.Check("cyclic", loop) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the identity-less record inside the cycle was not reported")
		}
	case <-timeoutAfterSeconds(10):
		t.Fatal("Check did not terminate on a self-referential value")
	}
}

func TestCheck_IgnoresNilAndUntypedNil(t *testing.T) {
	// "Absent" and "present but identity-less" are different classifications.
	// A caller that got nothing back has no record to be missing an id, so
	// neither nil shape may be reported.
	if err := degeneracy.Check("nothing", nil); err != nil {
		t.Errorf("a nil payload is not a degenerate decode: %v", err)
	}
	var nilPtr *record
	if err := degeneracy.Check("nothing", nilPtr); err != nil {
		t.Errorf("a nil pointer is not a degenerate decode: %v", err)
	}
	var nilSlice []record
	if err := degeneracy.Check("nothing", &nilSlice); err != nil {
		t.Errorf("a nil slice is not a degenerate decode: %v", err)
	}

	// The positive control: the same source, one field of nesting away from
	// those nils, still has to be classified degenerate — and with the sentinel
	// callers branch on. Without this, silencing Check entirely would pass.
	present := &record{}
	err := degeneracy.Check("nothing", present)
	if err == nil {
		t.Fatal("a present record with no id was classified as a real answer")
	}
	if !errors.Is(err, degeneracy.ErrDegenerate) {
		t.Errorf("Check error = %v, want it to wrap ErrDegenerate", err)
	}
	if present.ID != degeneracy.Marker {
		t.Errorf("ID = %q, want the poison marker %q", present.ID, degeneracy.Marker)
	}
}

func timeoutAfterSeconds(n int) <-chan time.Time {
	return time.After(time.Duration(n) * time.Second)
}

// TestCheck_ErrorIsIdentifiableByCallersWithAFallback pins the sentinel that
// lets a caller distinguish "this source is unavailable" from "this source
// answered in a shape hactl cannot decode".
//
// This exists because of a real hole the T6 mutation sweep found:
// cmd.fetchLogEntries treated *any* SystemLogList error as "system_log is
// unavailable" and silently fell back to the far less structured
// /api/error_log. A renamed field in system_log/list would therefore have been
// detected by degeneracy.Check and then thrown away one frame up, and `hactl
// log` would have kept printing a plausible answer from a different source —
// the exact failure this package exists to prevent, reintroduced by a fallback.
func TestCheck_ErrorIsIdentifiableByCallersWithAFallback(t *testing.T) {
	entries := []record{{Note: "no id"}}
	err := degeneracy.Check("system_log/list", &entries)
	if err == nil {
		t.Fatal("a record with no identity decoded without complaint")
	}
	if !errors.Is(err, degeneracy.ErrDegenerate) {
		t.Errorf("a degeneracy error must be identifiable with errors.Is: %v", err)
	}
	// A caller's fallback must not trip on an unrelated transport failure.
	if errors.Is(fmt.Errorf("reading response: %w", io.EOF), degeneracy.ErrDegenerate) {
		t.Error("an ordinary transport error was misreported as a degeneracy error")
	}
	// The human-readable half must survive the wrapping.
	if !strings.Contains(err.Error(), degeneracy.Marker) {
		t.Errorf("the marker was lost from the error text: %q", err)
	}
}
