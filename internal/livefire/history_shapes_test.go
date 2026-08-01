//go:build livefire

package livefire

import (
	"encoding/json"
	"testing"
)

// Finding #38: `ent hist` and `ent anomalies` exited 1 with empty stdout on any
// entity whose history contained a legitimately empty state.
//
// The degeneracy guard declared `state` an identity field on five structs, on
// the strength of a comment that said Home Assistant rejects an empty state
// string. It does not. The reference instance served 62 of 407 records over 400
// days with `"state": ""` — the key present on every one — and a second,
// unrelated entity carries the same shape. Because the guard compares the
// decoded value against Go's zero value, it cannot tell a field the wire never
// sent from one the wire sent empty, so it read an answer as a broken payload
// and the whole command died before rendering anything.
//
// The case asserts BOTH halves, because either alone would pass against the
// defect's absence rather than its fix: the command has to succeed, and its
// answer has to still contain the empty states. A version that silently dropped
// them would exit 0 and be just as wrong.
func TestSweepAnEmptyStateIsAnAnswerNotABrokenPayload(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()

		// The rig carries the shape deterministically (backfilled by TestMain).
		// The live entity is a pushed categorical sensor that goes blank
		// between computations, and needs a wide window because the blanks are
		// sparse — at 24h they had already aged out of the reported window.
		entity, window := rigEmptyStateSeries.EntityID, "3h"
		if tgt.Profile == Live {
			entity, window = "sensor.strompreis_kategorie", "400d"
		}

		out, err := tgt.Read(t, "ent", "hist", entity, "--since", window, "--json", "--tokensmax", "0")
		if err != nil {
			t.Fatalf("ent hist %s --since %s exited %d — an empty state was read as a missing wire "+
				"field again:\n%s", entity, window, ExitCode(err), truncate(out))
		}

		var rows []struct {
			State string `json:"state"`
			Time  string `json:"time"`
		}
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("ent hist --json is not a JSON array: %v\n%s", err, truncate(out))
		}

		var blanks int
		for _, r := range rows {
			if r.State == "" {
				blanks++
			}
		}
		if blanks == 0 {
			t.Fatalf("%s returned %d records over %s and not one carries an empty state — the shape "+
				"this case exists for is absent, so a pass here proves nothing. On the rig, TestMain's "+
				"backfill has stopped producing it; on the live profile the blanks have aged out of the "+
				"window and the case needs a wider one or a different entity.", entity, len(rows), window)
		}
	})
}
