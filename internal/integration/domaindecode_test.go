//go:build integration

package integration

import (
	"strings"
	"testing"
)

// ============================================================================
// H-21 against a real Home Assistant: a listing decodes only the entities it
// lists.
//
// The unit tier proves the ordering against a hand-written payload
// (internal/cmd/states_domain_decode_test.go). This tier proves it against a
// payload HA itself produced, through the real CLI — the shape the live report
// arrived in:
//
//	D:\hactl>hactl auto ls
//	parsing states: json: cannot unmarshal number -1.7525 into Go struct field
//	automationAttributes.attributes.current of type int
//
// Two colliders are in play, and they are complementary. The fixture's
// `template:` sensors carry five of the six keys at a colliding type; the
// States API push adds the sixth, `friendly_name`, which HA core overwrites on
// a template entity with the entity's name — a fixture built from `template:`
// alone silently asserts nothing about that key.
//
// Both are SYNTHETIC and named so. The entity that broke the reporting instance
// is unknown and that instance is unreachable; these cover a deliberate
// superset of it.
// ============================================================================

// TestAutoLsIgnoresAForeignEntitysAttributeTypes is the end-to-end reproduction
// for `auto ls`: HA's own /api/states carries entities whose attributes cannot
// fit automationAttributes, and the automations still list.
func TestAutoLsIgnoresAForeignEntitysAttributeTypes(t *testing.T) {
	inst := getDomainDecodeHA(t)
	pushed := ddPushCollider(t, inst)

	out := runHactlDir(t, inst.Dir(), "auto", "ls")

	for _, want := range []string{"collider_parallel", "collider_inert_a", "collider_inert_b"} {
		if !strings.Contains(out, want) {
			t.Errorf("`auto ls` did not list %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, ddSyntheticMarker) {
		t.Errorf("`auto ls` rendered %s — the listing must discard non-automation entities, "+
			"not just survive them:\n%s", pushed, out)
	}
}

// TestScriptLsIgnoresAForeignEntitysAttributeTypes is the same for `script ls`,
// which failed on the reporting instance in the same run and against its own,
// smaller attribute struct.
func TestScriptLsIgnoresAForeignEntitysAttributeTypes(t *testing.T) {
	inst := getDomainDecodeHA(t)
	pushed := ddPushCollider(t, inst)

	out := runHactlDir(t, inst.Dir(), "script", "ls")

	for _, want := range []string{"collider_parallel_script", "collider_inert_script"} {
		if !strings.Contains(out, want) {
			t.Errorf("`script ls` did not list %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, ddSyntheticMarker) {
		t.Errorf("`script ls` rendered %s — the listing must discard non-script entities:\n%s",
			pushed, out)
	}
}

// TestEntListingsStillSeeTheColliderIsTheControl is the negative control the
// two tests above need: they would also pass if the colliders had never reached
// /api/states at all. `ent ls` decodes every entity in the instance into
// entityState, whose Attributes is a map — the shape H-21 says a whole-payload
// decode must have — so it lists the colliders that `auto ls` and `script ls`
// must discard.
func TestEntListingsStillSeeTheColliderIsTheControl(t *testing.T) {
	inst := getDomainDecodeHA(t)
	pushed := ddPushCollider(t, inst)

	out := runHactlDir(t, inst.Dir(), "ent", "ls", "--pattern", ddSyntheticMarker)

	if !strings.Contains(out, pushed) {
		t.Errorf("`ent ls` does not show %s, so the colliders may never have been in the payload "+
			"the two H-21 listings read — which would make those tests pass while proving "+
			"nothing:\n%s", pushed, out)
	}
}
