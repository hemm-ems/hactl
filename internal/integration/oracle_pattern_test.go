//go:build integration

package integration

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// ============================================================================
// H-17 / D-1 — an identifier hactl prints is an identifier hactl accepts.
//
// Home Assistant carries interchangeable names for one automation: the config
// `id:` (surfaced as attributes.id, and what HA keys traces by), the alias
// (attributes.friendly_name, verbatim), the entity_id HA derives from the
// alias, and that entity_id's object id. hactl prints all of them — `auto ls`
// the object id, `auto show` the entity_id and the config_id, `auto create`
// the config id it just wrote, `ent show` and `auto cat` the alias — and
// `auto show`, `cat`, `diff`, `apply`, `delete` and `rollback` accept all of
// them (docs/decisions.md D-1).
//
// `--pattern` did not. A caller who copied the config id (or the alias) out of
// one command and pasted it into `auto ls --pattern` / `ent ls --pattern` got
// an empty listing, which under the manual's stop-at-the-first-miss rule reads
// as "no such automation" (D6/R2). The rule is one-directional and absolute:
// whatever hactl prints as an identifier for a resource, every hactl command
// that filters on that resource's identifier must match.
// ============================================================================

// lsIDsMatching returns the values of `column` from a `--json` listing filtered
// by --pattern.
func lsIDsMatching(t *testing.T, dir, column, pattern string, args ...string) []string {
	t.Helper()
	full := append(append([]string(nil), args...), "--pattern", pattern, "--json", "--top", "1000")
	raw := runHactlDir(t, dir, full...)
	var rows []map[string]string
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("hactl %v did not emit parseable JSON: %v\noutput:\n%s", full, err, raw)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r[column])
	}
	return out
}

func autoLsIDsMatching(t *testing.T, dir, pattern string) []string {
	t.Helper()
	return lsIDsMatching(t, dir, "id", pattern, "auto", "ls")
}

func entLsIDsMatching(t *testing.T, dir, pattern string) []string {
	t.Helper()
	return lsIDsMatching(t, dir, "entity_id", pattern, "ent", "ls")
}

// TestPatternAcceptsEveryIdentifierHactlPrints is the R2/D6 gate.
//
// HA is the oracle for which identifiers exist: /api/states reports the config
// id and the alias alongside the entity_id for every automation, and the test
// asserts against those rather than against a list typed into the test.
func TestPatternAcceptsEveryIdentifierHactlPrints(t *testing.T) {
	inst, _ := getOracleHA(t)
	identities := oracleAutomationIdentities(t, inst)

	divergent := 0
	for entityID, identity := range identities {
		configID, alias := identity.ConfigID, identity.Alias
		objectID := strings.TrimPrefix(entityID, "automation.")
		if configID == objectID || alias == "" || alias == objectID {
			// Nothing to distinguish: an identifier that collapses into the
			// object id cannot see the defect (H-8), and an automation with no
			// alias has only the machine forms.
			continue
		}
		divergent++

		t.Run(objectID, func(t *testing.T) {
			// hactl prints the config id here — that is what makes it an
			// identifier a caller can be holding.
			raw := runHactlDir(t, inst.Dir(), "auto", "show", entityID, "--json")
			var show struct {
				EntityID string `json:"entity_id"`
				ConfigID string `json:"config_id"`
			}
			if err := json.Unmarshal([]byte(raw), &show); err != nil {
				t.Fatalf("auto show --json did not parse: %v\noutput:\n%s", err, raw)
			}
			if show.ConfigID != configID {
				t.Fatalf("auto show printed config_id %q for %s, HA reports %q",
					show.ConfigID, entityID, configID)
			}

			for _, ref := range []string{configID, entityID, objectID, alias} {
				if got := autoLsIDsMatching(t, inst.Dir(), ref); !slices.Contains(got, objectID) {
					t.Errorf("auto ls --pattern %q returned %v, missing %q.\n"+
						"`auto show` displays and resolves that identifier for this very "+
						"automation (entity_id %s, config id %s, alias %q) — a command must "+
						"not print an identifier another command refuses (D-1).",
						ref, got, objectID, entityID, configID, alias)
				}
			}

			for _, ref := range []string{configID, alias} {
				if got := entLsIDsMatching(t, inst.Dir(), ref); !slices.Contains(got, entityID) {
					t.Errorf("ent ls --pattern %q returned %v, missing %q.\n"+
						"HA reports that identifier for this entity and hactl prints it; the "+
						"manual routes callers to `ent ls --pattern` as the discovery fallback, "+
						"so an empty answer here reads as 'no such entity'.",
						ref, got, entityID)
				}
			}
		})
	}

	if divergent == 0 {
		t.Fatal("precondition: no automation has a config id and alias that differ from its object id; " +
			"H-17/D-1 cannot be observed (see TestOracleFixtureIsDistinguishing)")
	}
}

// TestPatternStillRejectsWhatDoesNotExist is the negative control for the test
// above: a filter that matched everything would satisfy it just as well.
func TestPatternStillRejectsWhatDoesNotExist(t *testing.T) {
	inst, _ := getOracleHA(t)

	const absent = "cfgid_no_such_automation_anywhere"
	if got := autoLsIDsMatching(t, inst.Dir(), absent); len(got) != 0 {
		t.Errorf("auto ls --pattern %q matched %v; no automation carries that identifier", absent, got)
	}
	if got := entLsIDsMatching(t, inst.Dir(), absent); len(got) != 0 {
		t.Errorf("ent ls --pattern %q matched %v; no entity carries that identifier", absent, got)
	}

	// A glob over config ids must still discriminate: the oracle fixture's
	// config ids all start `cfgid_`, so a glob anchored elsewhere matches none
	// of them while the `cfgid_*` glob matches them all.
	configIDs := oracleAutomationConfigIDs(t, inst)
	want := make([]string, 0, len(configIDs))
	for entityID, configID := range configIDs {
		if strings.HasPrefix(configID, "cfgid_") {
			want = append(want, strings.TrimPrefix(entityID, "automation."))
		}
	}
	if len(want) == 0 {
		t.Fatal("precondition: no automation carries a cfgid_-prefixed config id")
	}
	got := autoLsIDsMatching(t, inst.Dir(), "cfgid_*")
	assertSameSet(t, "auto ls --pattern 'cfgid_*'", want, got)
	if got := autoLsIDsMatching(t, inst.Dir(), "zzz_*"); len(got) != 0 {
		t.Errorf("auto ls --pattern 'zzz_*' matched %v; no automation identifier starts with zzz_", got)
	}
}
