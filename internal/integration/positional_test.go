//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// H-22 — the positional contract, against a live instance.
//
// The P1 this closes was reported from a real instance: `hactl auto show ''`
// answered with a real, unrelated automation and `hactl auto delete ''` printed
// a plan to delete it, because `resolveAutomation` compares the caller's
// reference to `attributes.id` with `==` and the automation it matched had none.
//
// The record it matched there was a restored ghost. The premise underneath is
// narrower and stock: **Home Assistant reports no config id for an automation
// that has no `id:`**, and an automation without one is ordinary YAML. The
// `idless` fixture is exactly that instance, and
// TestOracleAutomationWithoutAnIDCarriesNoConfigID is the probe.
//
// Recorded because it was asked and answered, not assumed: creating an
// automation through HA's config API and deleting it again does *not* leave a
// ghost — on HA 2026.x the delete removes the entity registry entry too, and
// the entity disappears from /api/states outright (observed on this rig,
// 2026-07-30). The field instance's 37 ghosts come from configs removed some
// other way (hand-edited YAML, re-authored ids), which this rig cannot produce
// through the API. Nothing here rests on the ghost: the id-less automation is
// the same record shape and needs no deletion at all.
// ============================================================================

var (
	idlessOnce sync.Once
	idlessHA   *hatest.Instance
)

// getIdlessHA starts (once) an instance whose automations.yaml holds one
// automation with a config id and one without.
func getIdlessHA(t *testing.T) *hatest.Instance {
	t.Helper()
	idlessOnce.Do(func() {
		idlessHA = hatest.StartShared(t, hatest.WithFixture("idless"))
		waitForRunning(t, idlessHA)
	})
	if idlessHA == nil {
		t.Fatal("idless HA instance unavailable")
	}
	return idlessHA
}

// idlessAutomations returns the entity_id of the automation without a config id
// and of the one with it, read from HA rather than from the fixture.
func idlessAutomations(t *testing.T, inst *hatest.Instance) (idless, withID string) {
	t.Helper()
	for _, e := range ddStates(t, inst) {
		if !strings.HasPrefix(e.EntityID, "automation.") {
			continue
		}
		var id string
		if raw, ok := e.Attributes["id"]; ok {
			if err := json.Unmarshal(raw, &id); err != nil {
				t.Fatalf("%s: decoding attributes.id (%s): %v", e.EntityID, raw, err)
			}
		}
		if id == "" {
			idless = e.EntityID
		} else {
			withID = e.EntityID
		}
	}
	if idless == "" || withID == "" {
		t.Fatalf("the idless fixture did not load both automations (without id: %q, with id: %q)", idless, withID)
	}
	return idless, withID
}

// TestOracleAutomationWithoutAnIDCarriesNoConfigID asks HA for the premise the
// P1 rests on: is there an ordinary automation whose `attributes.id` — the
// field `resolveAutomation` compares against the caller's reference — is empty
// or absent? If every automation carried a config id, an empty reference would
// have matched nothing and the ordering of the fix would be arguable.
func TestOracleAutomationWithoutAnIDCarriesNoConfigID(t *testing.T) {
	inst := getIdlessHA(t)

	var withoutID, withID []string
	for _, e := range ddStates(t, inst) {
		if !strings.HasPrefix(e.EntityID, "automation.") {
			continue
		}
		raw, ok := e.Attributes["id"]
		if !ok {
			withoutID = append(withoutID, e.EntityID)
			continue
		}
		var id string
		if err := json.Unmarshal(raw, &id); err != nil {
			t.Fatalf("%s: attributes.id is %s, which is not a string", e.EntityID, raw)
		}
		if id == "" {
			withoutID = append(withoutID, e.EntityID)
		} else {
			withID = append(withID, e.EntityID)
		}
	}
	if len(withoutID) == 0 {
		t.Fatal("HA reports a config id for every automation on this instance — the empty-reference match this law refuses would have nothing to match")
	}
	if len(withID) == 0 {
		t.Fatal("no automation on this instance carries a config id — the fixture is not distinguishing")
	}
	t.Logf("HA reports no config id for %v and a config id for %v", withoutID, withID)
}

// TestBlankAutomationIdentifierResolvesNothing is the P1 itself: against an
// instance that holds the record an empty reference matched, every command that
// takes an identifier must refuse the empty string — and the record must stay
// addressable by the identifiers HA does report for it, since a fix that made
// it unreachable would pass the first half and break H-17.
func TestBlankAutomationIdentifierResolvesNothing(t *testing.T) {
	inst := getIdlessHA(t)
	idless, _ := idlessAutomations(t, inst)
	dir := inst.Dir()

	for _, args := range [][]string{
		{"auto", "show", ""},
		{"auto", "delete", ""},
		{"auto", "cat", ""},
		{"auto", "apply", ""},
		{"device", "show", ""},
		{"ent", "show", "  "},
	} {
		out, err := runHactlDirErr(t, dir, args...)
		if err == nil {
			t.Errorf("hactl %s succeeded against a live instance:\n%s", strings.Join(args, " "), out)
		}
		if strings.Contains(out, idless) {
			t.Errorf("hactl %s answered with %s:\n%s", strings.Join(args, " "), idless, out)
		}
	}

	// The control: H-17 — every identifier HA reports for that automation still
	// resolves, so the refusal narrowed nothing but the empty string.
	objectID := strings.TrimPrefix(idless, "automation.")
	for _, ref := range []string{idless, objectID, "No Config Id"} {
		out, err := runHactlDirErr(t, dir, "auto", "show", ref)
		if err != nil {
			t.Errorf("hactl auto show %q failed: %v\n%s", ref, err, out)
			continue
		}
		if !strings.Contains(out, idless) {
			t.Errorf("hactl auto show %q did not report %s:\n%s", ref, idless, out)
		}
	}
}

// TestListingRefusesAPositionalFilter is the P2 against a live instance: the
// finding was that `ent ls light` printed output byte-identical to `ent ls`, so
// the dropped argument was invisible. The pair below is that comparison, with
// the refusal in place of the first half and the flag form as the control.
func TestListingRefusesAPositionalFilter(t *testing.T) {
	out, err := runHactlErr(t, "ent", "ls", "sun")
	if err == nil {
		t.Errorf("hactl ent ls sun succeeded and returned:\n%s", out)
	} else if !strings.Contains(err.Error(), "--domain sun") {
		t.Errorf("the refusal does not route the caller to the flag that works: %v", err)
	}

	filtered := runHactl(t, "ent", "ls", "--domain", "sun", "--json", "--tokensmax", "0")
	var rows []map[string]any
	if jsonErr := json.Unmarshal([]byte(filtered), &rows); jsonErr != nil {
		t.Fatalf("ent ls --domain sun --json: %v\n%s", jsonErr, filtered)
	}
	if len(rows) == 0 {
		t.Fatal("ent ls --domain sun returned no rows — the control is not exercising anything")
	}
	for _, r := range rows {
		if id, _ := r["entity_id"].(string); !strings.HasPrefix(id, "sun.") {
			t.Errorf("ent ls --domain sun returned %q", id)
		}
	}
}

// TestUnknownSubcommandFailsAgainstALiveInstance keeps the third leg honest at
// the boundary an agent actually sees: a mistyped subcommand must not be
// reported as success by a run that could otherwise have reached HA.
func TestUnknownSubcommandFailsAgainstALiveInstance(t *testing.T) {
	for _, args := range [][]string{
		{"helper", "set"},
		{"dash", "frobnicate"},
		{"auto", "shwo", "climate_schedule"},
	} {
		out, err := runHactlErr(t, args...)
		if err == nil {
			t.Errorf("hactl %s succeeded:\n%s", strings.Join(args, " "), out)
		}
		if out != "" {
			t.Errorf("hactl %s wrote to stdout while failing:\n%s", strings.Join(args, " "), out)
		}
	}
}
