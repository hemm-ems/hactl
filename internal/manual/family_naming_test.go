package manual

import (
	"slices"
	"strings"
	"testing"
)

// TestFamilyMembersCoversTheAliasesPointingAtIt — the members of a family are
// derived from the same two tables FamilyFor resolves against, so a new alias
// cannot leave the naming behind.
func TestFamilyMembersCoversTheAliasesPointingAtIt(t *testing.T) {
	for _, family := range Families() {
		members := FamilyMembers(family)
		if !slices.Contains(members, family) {
			t.Errorf("FamilyMembers(%q) = %v, missing the family's own command", family, members)
		}
		if !slices.IsSorted(members) {
			t.Errorf("FamilyMembers(%q) = %v is not sorted — H-16: an answer is never a function "+
				"of map iteration order", family, members)
		}
		for _, m := range members {
			got, ok := FamilyFor(m)
			if !ok || got != family {
				t.Errorf("FamilyMembers(%q) lists %q, but FamilyFor(%q) = %q,%v",
					family, m, m, got, ok)
			}
		}
	}
	for alias, family := range Aliases {
		if !slices.Contains(FamilyMembers(family), alias) {
			t.Errorf("alias %q → %q is missing from FamilyMembers(%q)", alias, family, family)
		}
	}
}

// TestFamilyNoteNamesEveryCommandItCovers — the banner must not state
// something false about the caller's session.
//
// label/area/floor deliberately share one manual section, keyed "label", and
// the note interpolated that internal key: an agent that had never typed
// `label` was told "delivered with your first label command" after running
// `area create`. The grouping is right; naming it after one of its three
// members is not — and the same shape sat on `trace`/`rollback` (keyed "auto"),
// `cc` (keyed "log") and `changes`/`issues` (keyed "health").
func TestFamilyNoteNamesEveryCommandItCovers(t *testing.T) {
	note := FamilyNote("label")
	for _, want := range []string{"area", "floor", "label"} {
		if !strings.Contains(note, want) {
			t.Errorf("the registry family note does not name %q: %s", want, note)
		}
	}

	// A family that covers exactly one command still reads as it always did.
	if !strings.Contains(FamilyNote("dash"), "'dash' family how-to") {
		t.Errorf("a single-command family note changed shape: %s", FamilyNote("dash"))
	}

	// Every note names every command it covers — the derived form of the above.
	for _, family := range Families() {
		n := FamilyNote(family)
		for _, member := range FamilyMembers(family) {
			if !strings.Contains(n, member) {
				t.Errorf("FamilyNote(%q) does not name %q, which it covers: %s", family, member, n)
			}
		}
	}
}
