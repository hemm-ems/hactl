//go:build integration

package integration

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// ============================================================================
// Oracle for H-10's fidelity clause: a number hactl re-emits under --json is
// the number Home Assistant sent.
//
// H-21 was reported as an attribute DECODE defect and #105 fixed the decode.
// The encode half shipped unfixed: `encoding/json` decodes a JSON number into
// float64 for a map[string]any and marshals float64(5000) back as `5000`, so
// `ent show --json` re-emitted HA's `"max": 5000.0` as a bare integer. Python's
// json.loads types that as int, and every consumer that validates against HA's
// own attribute contracts then disagrees with HA about the entity.
//
// This is the one claim in the fix that the unit tier cannot settle, because it
// rests on what HA actually puts on the wire. A stub answering `5000.0` proves
// hactl preserves whatever it is handed; it does not prove HA hands out
// `5000.0` in the first place. So the oracle reads the wire itself — the same
// method domaindecode_oracle_test.go established for the decode half, against
// the same fixture, whose `demo:` platform supplies number.* and climate.*
// entities across the two domains the report named.
// ============================================================================

// wireNumberLiteral matches a JSON number that is integral in VALUE but written
// as a float — `5000.0`, `-45.0`, `1.0e2`. That is the exact set the re-encode
// collapsed, and the exact set a round-tripped equality comparison cannot see,
// since Go decodes `5000` and `5000.0` to one float64.
var wireNumberLiteral = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][-+]?\d+)?$`)

// integralFloatLiteral reports whether a raw JSON number literal is a float in
// form and whole in value.
func integralFloatLiteral(raw string) bool {
	raw = strings.TrimSpace(raw)
	if !wireNumberLiteral.MatchString(raw) || !strings.ContainsAny(raw, ".eE") {
		return false
	}
	var f float64
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		return false
	}
	return f == float64(int64(f))
}

// TestOracleEntShowJSONPreservesWireNumberForm asks a live HA for every
// attribute it types as a whole-valued float, then requires `ent show --json`
// to hand each one back in the same lexical form.
//
// The test carries its own emptiness guard. "No integral float found" would
// otherwise be a green run proving nothing, which is precisely how the decode
// half's blind spot survived: a sweep that matches nothing passes forever.
func TestOracleEntShowJSONPreservesWireNumberForm(t *testing.T) {
	inst := getDomainDecodeHA(t)

	// The wire, read with attributes raw so no Go type is imposed on them.
	states := ddStates(t, inst)

	type probe struct {
		entityID string
		attr     string
		literal  string
	}
	var probes []probe
	for _, e := range states {
		if e.synthetic() {
			continue // this file asks what HA emits, not what a test pushed in
		}
		for attr, raw := range e.Attributes {
			if integralFloatLiteral(string(raw)) {
				probes = append(probes, probe{entityID: e.EntityID, attr: attr, literal: strings.TrimSpace(string(raw))})
			}
		}
	}
	if len(probes) == 0 {
		t.Fatal("no first-party entity in this instance carries a whole-valued float attribute — " +
			"the oracle cannot settle the encode question against a payload that never poses it. " +
			"Check that the `demo:` platform still loads number.*/climate.* entities.")
	}

	// Keep the run bounded but cover more than one entity and more than one
	// domain: the report named number.* and climate.*, and a fix that repaired
	// one renderer would pass a single-entity check.
	byEntity := map[string][]probe{}
	domains := map[string]bool{}
	for _, p := range probes {
		byEntity[p.entityID] = append(byEntity[p.entityID], p)
		domains[strings.SplitN(p.entityID, ".", 2)[0]] = true
	}
	t.Logf("wire oracle: %d whole-valued float attribute(s) across %d entities in %d domains",
		len(probes), len(byEntity), len(domains))

	checked := 0
	for entityID, ps := range byEntity {
		out := runHactlDir(t, inst.Dir(), "ent", "show", entityID, "--json", "--tokensmax", "0")
		for _, p := range ps {
			// The assertion is on the BYTES. Decoding both sides and comparing
			// would pass over the defect, because that is what the defect is:
			// the value survives, the type does not.
			want := `"` + p.attr + `": ` + p.literal
			if !strings.Contains(out, want) {
				t.Errorf("ent show --json changed the wire form of %s.%s: HA sent %s, and the output does not contain %q\n%s",
					entityID, p.attr, p.literal, want, out)
			}
			checked++
		}
	}
	t.Logf("wire oracle: %d attribute literal(s) round-tripped byte for byte", checked)
}

// TestOracleEntShowJSONNumberDomains records WHICH domains this instance can
// pose the question with, so a future run that finds fewer of them shows up as
// a narrowed oracle rather than as a quiet pass.
//
// It is a census, not a pass/fail on hactl: an instance is a lower bound on
// what HA can emit, never a census of it — the same framing
// TestOracleStatesSixKeyDomainCensus carries.
func TestOracleEntShowJSONNumberDomains(t *testing.T) {
	inst := getDomainDecodeHA(t)
	states := ddStates(t, inst)

	byDomain := map[string]int{}
	for _, e := range states {
		if e.synthetic() {
			continue
		}
		for _, raw := range e.Attributes {
			if integralFloatLiteral(string(raw)) {
				byDomain[e.domain()]++
			}
		}
	}
	if len(byDomain) == 0 {
		t.Fatal("no domain in this instance emits a whole-valued float attribute — the census is empty")
	}
	for _, want := range []string{"number", "climate"} {
		if byDomain[want] == 0 {
			t.Logf("note: %s.* emitted no whole-valued float here; the report observed the defect on that domain, "+
				"so this instance is a narrower oracle than the reporting one", want)
		}
	}
	t.Logf("whole-valued float attributes by domain: %v", byDomain)
}
