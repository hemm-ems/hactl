//go:build livefire

package livefire

import (
	"encoding/json"
	"strings"
	"testing"
)

// Finding #81 — H-27: every assignment a command can make, it can also
// unmake. `ent set-label`/`device set-label` could only ever grow a label
// set, and `ent set-area`/`device set-area` rejected "" and "none" outright,
// so there was no way to take one label off one entity or clear one entity's
// area short of `label delete`/`area delete`, which strip the value from
// EVERY holder instance-wide.
//
// Both cases below assert the property that distinguishes `--remove`/`--clear`
// from the instance-wide delete they replace: the effect is scoped to the ONE
// target named. A case that only checked "the primary lost it" would pass for
// `label delete` too; the assertion that matters is that a SECOND holder of
// the same value is untouched.
//
// unmakeEntityA and unmakeEntityB are two real, distinct, deviceless entities
// with no area/label of their own on the reference instance (a template
// sensor and a helper, so H-8's device-area inheritance cannot mask a clear as
// a no-op): sensor.pg_w7_template_sensor (used the same way in
// TestLiveWriteGuardAllowsAPlaygroundWrite) and input_boolean.pg_core_flag_a
// (FIXPLAN-livefire.md WP7 lesson 1). The rig has no such fixture — its
// entities carry no registry area/label of their own either — so on the rig
// two arbitrary config-defined entities serve the same role.
func unmakeEntities(tgt Target) (a, b string) {
	if tgt.Profile == Live {
		return "sensor.pg_w7_template_sensor", "input_boolean.pg_core_flag_a"
	}
	return "input_boolean.guest_mode", "input_boolean.alarm_armed"
}

// unmakeLabelName is the pg_-namespaced label the label case creates and
// deletes within itself, rather than depending on one already existing.
func unmakeLabelName(tgt Target) string {
	if tgt.Profile == Live {
		return "pg_wp13_unmake_label"
	}
	return "wp13_unmake_label"
}

// unmakeAreaName is the same for the area case.
func unmakeAreaName(tgt Target) string {
	if tgt.Profile == Live {
		return "pg_wp13_unmake_area"
	}
	return "wp13_unmake_area"
}

// entShowFields is the subset of `ent show --json` this file reads back.
// `area` and `labels` are both OMITTED by the command when empty (ent.go's
// runEntShow), never sent as "" — so their Go zero value already means
// "absent" and no separate presence check is needed here.
type entShowFields struct {
	Area   string `json:"area"`
	Labels string `json:"labels"`
}

func entShow(t *testing.T, tgt Target, entity string) entShowFields {
	t.Helper()
	out := tgt.MustRead(t, "ent", "show", entity, "--json")
	var fields entShowFields
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		t.Fatalf("ent show %s --json is not a JSON object: %v\n%s", entity, err, out)
	}
	return fields
}

// dryRunThenConfirm runs args as a dry-run and, only if that succeeds, the
// same args plus --confirm — H-26's witness clause: a --confirm is authorized
// by a dry-run of the same command and target, never assumed.
func dryRunThenConfirm(t *testing.T, tgt Target, targets, vocab, args []string) {
	t.Helper()
	if out, err := tgt.Read(t, args...); err != nil {
		t.Fatalf("dry run %v: %v\n%s", args, err, out)
	}
	confirmArgs := append(append([]string{}, args...), "--confirm")
	if out, err := tgt.Write(t, targets, vocab, confirmArgs); err != nil {
		t.Fatalf("%v: %v\n%s", confirmArgs, err, out)
	}
}

// TestSweepRemovingOneLabelLeavesTheOtherHolderAlone is finding #81's label
// half. It attaches one label to two entities, removes it from ONE via
// --remove, and requires the other to still carry it — the property
// `label delete` cannot offer, because that command removes the label from
// both.
func TestSweepRemovingOneLabelLeavesTheOtherHolderAlone(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		primary, secondary := unmakeEntities(tgt)
		label := unmakeLabelName(tgt)

		dryRunThenConfirm(t, tgt, []string{label}, []string{"label", "create"},
			[]string{"label", "create", label})
		t.Cleanup(func() {
			_, _ = tgt.Write(t, []string{label}, []string{"label", "delete"},
				[]string{"label", "delete", label, "--confirm"})
		})

		for _, entity := range []string{primary, secondary} {
			dryRunThenConfirm(t, tgt, []string{entity, label}, []string{"ent", "set-label"},
				[]string{"ent", "set-label", entity, label})
		}
		t.Cleanup(func() {
			for _, entity := range []string{primary, secondary} {
				_, _ = tgt.Write(t, []string{entity, label}, []string{"ent", "set-label", "--remove"},
					[]string{"ent", "set-label", entity, "--remove", label, "--confirm"})
			}
		})

		if before := entShow(t, tgt, primary); !strings.Contains(before.Labels, label) {
			t.Fatalf("%s does not carry %q after attaching it — the setup step did not take", primary, label)
		}
		if before := entShow(t, tgt, secondary); !strings.Contains(before.Labels, label) {
			t.Fatalf("%s does not carry %q after attaching it — the setup step did not take", secondary, label)
		}

		dryRunThenConfirm(t, tgt, []string{primary, label}, []string{"ent", "set-label", "--remove"},
			[]string{"ent", "set-label", primary, "--remove", label})

		if after := entShow(t, tgt, primary); strings.Contains(after.Labels, label) {
			t.Fatalf("%s: %q is still attached after 'ent set-label --remove' — the unmake did not take: labels=%q",
				primary, label, after.Labels)
		}
		if after := entShow(t, tgt, secondary); !strings.Contains(after.Labels, label) {
			t.Fatalf("%s: --remove on %s took %q off %s too. %s exists precisely because "+
				"'label delete' already does that; --remove has to be scoped to the entity it named.",
				Finding(81), primary, label, secondary, secondary)
		}
	})
}

// TestSweepClearingOneEntitysAreaLeavesAnothersAlone is finding #81's area
// half, one registry field over: --clear removes ONE entity's own area
// without touching a second entity placed in the same area — the property
// `area delete` cannot offer, because deleting the area strips it from every
// entity, device and area entry that holds it.
func TestSweepClearingOneEntitysAreaLeavesAnothersAlone(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		primary, secondary := unmakeEntities(tgt)
		area := unmakeAreaName(tgt)

		dryRunThenConfirm(t, tgt, []string{area}, []string{"area", "create"},
			[]string{"area", "create", area})
		t.Cleanup(func() {
			_, _ = tgt.Write(t, []string{area}, []string{"area", "delete"},
				[]string{"area", "delete", area, "--confirm"})
		})

		for _, entity := range []string{primary, secondary} {
			dryRunThenConfirm(t, tgt, []string{entity, area}, []string{"ent", "set-area"},
				[]string{"ent", "set-area", entity, area})
		}
		t.Cleanup(func() {
			for _, entity := range []string{primary, secondary} {
				_, _ = tgt.Write(t, []string{entity}, []string{"ent", "set-area", "--clear"},
					[]string{"ent", "set-area", entity, "--clear", "--confirm"})
			}
		})

		if before := entShow(t, tgt, primary); before.Area != area {
			t.Fatalf("%s area = %q after setup, want %q — the setup step did not take", primary, before.Area, area)
		}
		if before := entShow(t, tgt, secondary); before.Area != area {
			t.Fatalf("%s area = %q after setup, want %q — the setup step did not take", secondary, before.Area, area)
		}

		dryRunThenConfirm(t, tgt, []string{primary}, []string{"ent", "set-area", "--clear"},
			[]string{"ent", "set-area", primary, "--clear"})

		if after := entShow(t, tgt, primary); after.Area != "" {
			t.Fatalf("%s area = %q after --clear, want none — the unmake did not take", primary, after.Area)
		}
		if after := entShow(t, tgt, secondary); after.Area != area {
			t.Fatalf("%s: --clear on %s also left %s with area %q instead of %q. %s exists precisely "+
				"because 'area delete' already clears it instance-wide; --clear has to be scoped to the "+
				"entity it named.", Finding(81), primary, secondary, after.Area, area, secondary)
		}
	})
}
