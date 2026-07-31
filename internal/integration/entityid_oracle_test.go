//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// entityIDCorpus is the set of shapes the mirror and Home Assistant must agree
// on. The first five are the identifiers a live instance accepted as a rename
// PREVIEW and refused at confirm time ("Invalid entity ID", "New entity ID
// should be same domain"); the rest are the clauses of HA's regex, one case
// each, because a corpus that only carries the reported bug proves the mirror
// for that bug.
var entityIDCorpus = []string{
	"input_boolean.pg_w5_renamed",
	"input_boolean.pg w5 bad",
	"input_boolean.PG_w5_Bad!",
	"input_boolean.pg_w5_🔥bad",
	"input_boolean.pg.w5.bad",
	"sensor.time",
	"a.b",
	"1.2",
	"_a.b",
	"a_.b",
	"a.b_",
	"a._b",
	"a__b.c",
	"a.b__c",
	"ä.b",
	"nodomain",
	"a.",
	".b",
	"",
	"a.b.c",
	"binary_sensor.door_1",
}

// TestOracleEntityIDRule asks Home Assistant what a valid entity_id is and
// fails when haapi.ValidEntityID disagrees on any shape.
//
// The mirror exists because Go's RE2 has no lookaround, so HA's
// VALID_ENTITY_ID cannot be transliterated and has to be re-expressed clause by
// clause — which is precisely the kind of copy that drifts. hactl refuses a
// rename and a service call on the strength of it, so a mirror that is wrong in
// the strict direction refuses ids HA accepts, and one that is wrong in the lax
// direction re-opens finding #96: a preview that promises a rename HA will not
// perform.
//
// It reads HA's own compiled regex out of the running container rather than a
// vendored copy of the pattern, so the failure arrives the day HA changes the
// rule instead of the day a user hits it.
func TestOracleEntityIDRule(t *testing.T) {
	// One python invocation for the whole corpus: the shapes are NUL-free and
	// newline-free by construction, so the answers come back one per line, in
	// order, as `1` or `0`.
	script := "import sys\nfrom homeassistant.core import valid_entity_id\n" +
		"for line in sys.stdin.read().split('\\n'):\n    print(1 if valid_entity_id(line) else 0)\n"
	code, out, err := ha.Exec(context.Background(), "sh", "-c",
		"printf '%s' "+shellQuote(strings.Join(entityIDCorpus, "\n"))+" | python3 -c "+shellQuote(script))
	if err != nil {
		t.Fatalf("asking the running Home Assistant for its entity_id rule: %v", err)
	}
	if code != 0 {
		t.Fatalf("the probe exited %d: %s", code, out)
	}

	answers := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(answers) != len(entityIDCorpus) {
		t.Fatalf("asked about %d ids and got %d answers — the probe has stopped matching and would "+
			"pass forever while proving nothing:\n%s", len(entityIDCorpus), len(answers), out)
	}
	for i, id := range entityIDCorpus {
		want := strings.TrimSpace(answers[i]) == "1"
		if got := haapi.ValidEntityID(id); got != want {
			t.Errorf("haapi.ValidEntityID(%q) = %v; Home Assistant's valid_entity_id says %v — "+
				"internal/haapi/entityid.go no longer mirrors homeassistant/core.py's VALID_ENTITY_ID",
				id, got, want)
		}
	}
}

// shellQuote wraps s for `sh -c`, which is how the probe reaches python without
// a file: single quotes, with any embedded single quote spliced.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
