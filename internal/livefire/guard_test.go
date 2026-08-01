//go:build livefire

package livefire

import (
	"errors"
	"strings"
	"testing"
)

// The guard is the only thing standing between this sweep and somebody's real
// house, so it is tested the way a gate has to be: every case that must be
// refused is asserted to be refused, by name. A guard whose tests only cover
// the allowed cases proves that it permits, never that it protects.

func TestLiveWriteGuardAllowsAPlaygroundWrite(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		targets []string
		vocab   []string
	}{
		{
			"delete a pg automation",
			[]string{"auto", "delete", "pg_core_auto_counter", "--confirm"},
			[]string{"pg_core_auto_counter"},
			[]string{"auto", "delete"},
		},
		{
			"service call targeting a pg entity through a payload",
			[]string{"svc", "call", "automation.turn_off", "-d", `{"entity_id":"automation.pg_w5_auto"}`, "--confirm"},
			[]string{"automation.pg_w5_auto"},
			[]string{"svc", "call", "automation.turn_off"},
		},
		{
			"helper create names its domain, which is vocabulary not a target",
			[]string{"helper", "create", "input_boolean", "--file", "/tmp/x.yaml", "--confirm"},
			[]string{},
			[]string{"helper", "create", "input_boolean"},
		},
		{
			"a dotted pg entity_id",
			[]string{"ent", "set-area", "sensor.pg_w7_template_sensor", "pg_core_area", "--confirm"},
			[]string{"sensor.pg_w7_template_sensor", "pg_core_area"},
			[]string{"ent", "set-area"},
		},
		{
			// finding #81 / H-27: --clear takes no value, so it must not
			// swallow --confirm as though --confirm were its argument — the
			// failure mode isBoolFlag's list exists to name each flag against.
			"--clear followed by --confirm",
			[]string{"ent", "set-area", "sensor.pg_w7_template_sensor", "--clear", "--confirm"},
			[]string{"sensor.pg_w7_template_sensor"},
			[]string{"ent", "set-area"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := guardLiveWrite(tc.args, tc.targets, tc.vocab); err != nil {
				t.Fatalf("guard refused a legitimate playground write: %v", err)
			}

			// The control. A guard that permitted everything would also pass
			// the assertion above, so each allowed command is re-run with one
			// real object appended: the same inputs must now be refused, and
			// the refusal must name that object rather than something else.
			intruded := append(append([]string{}, tc.args...), "wohnzimmer_licht")
			err := guardLiveWrite(intruded, tc.targets, tc.vocab)
			var unguarded *UnguardedError
			if !errors.As(err, &unguarded) {
				t.Fatalf("guard permitted a real object appended to an allowed command: %v", intruded)
			}
			if unguarded.Arg != "wohnzimmer_licht" {
				t.Errorf("guard blamed %q, want the intruding object", unguarded.Arg)
			}
		})
	}
}

func TestLiveWriteGuardRefusesAnythingOutsideThePlayground(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		targets []string
		vocab   []string
		wantArg string
	}{
		{
			"a real automation as a positional",
			[]string{"auto", "delete", "weckzeit_schlafzimmer_von_fhem", "--confirm"},
			[]string{},
			[]string{"auto", "delete"},
			"weckzeit_schlafzimmer_von_fhem",
		},
		{
			"a real object declared as a target",
			[]string{"auto", "delete", "morning_routine", "--confirm"},
			[]string{"morning_routine"},
			[]string{"auto", "delete"},
			"morning_routine",
		},
		{
			"a real entity smuggled through a service payload",
			[]string{"svc", "call", "light.turn_off", "-d", `{"entity_id":"light.wohnzimmer"}`, "--confirm"},
			[]string{},
			[]string{"svc", "call", "light.turn_off"},
			`{"entity_id":"light.wohnzimmer"}`,
		},
		{
			"an undeclared identifier riding along beside a legitimate one",
			[]string{"ent", "set-area", "sensor.pg_w7_template_sensor", "kueche", "--confirm"},
			[]string{"sensor.pg_w7_template_sensor"},
			[]string{"ent", "set-area"},
			"kueche",
		},
		{
			"a blank name, which HA accepts and which bricks the area family",
			[]string{"area", "create", "", "--confirm"},
			[]string{},
			[]string{"area", "create"},
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := guardLiveWrite(tc.args, tc.targets, tc.vocab)
			if err == nil {
				t.Fatalf("guard ALLOWED a write outside the playground: %v", tc.args)
			}
			var unguarded *UnguardedError
			if !errors.As(err, &unguarded) {
				t.Fatalf("want *UnguardedError, got %T: %v", err, err)
			}
			if unguarded.Arg != tc.wantArg {
				t.Errorf("guard blamed %q, want %q", unguarded.Arg, tc.wantArg)
			}
			if !strings.Contains(err.Error(), "pg_") {
				t.Errorf("refusal does not name the rule it enforces: %v", err)
			}
		})
	}
}

// A flag's value must not be mistaken for a target — and must not become a
// hole either. `--file /etc/passwd` is a path, not an object on the instance,
// so it is skipped; but skipping must not run past the end of the argument
// list or swallow the next real argument.
func TestLiveWriteGuardHandlesFlagValues(t *testing.T) {
	if err := guardLiveWrite(
		[]string{"auto", "apply", "pg_w5_auto", "--file", "new.yaml", "--confirm"},
		[]string{"pg_w5_auto"}, []string{"auto", "apply"},
	); err != nil {
		t.Fatalf("a flag value blocked a legitimate write: %v", err)
	}

	// The boolean --confirm consumes nothing, so the id after it is still seen.
	err := guardLiveWrite(
		[]string{"auto", "delete", "--confirm", "real_automation"},
		[]string{}, []string{"auto", "delete"},
	)
	if err == nil {
		t.Fatal("an id after a boolean flag slipped past the guard")
	}

	// A trailing flag with no value must not panic or wrap around.
	if err := guardLiveWrite([]string{"auto", "ls", "--label"}, nil, []string{"auto", "ls"}); err != nil {
		t.Fatalf("trailing flag: %v", err)
	}
}

// `new.yaml` and `light.kitchen` are the same shape. The guard has to tell
// them apart or it either refuses every --file or lets a real entity through
// in a payload, so the rule that separates them is pinned here rather than
// left to the regex.
func TestLiveWriteGuardTellsAPathFromAnEntityID(t *testing.T) {
	paths := []string{"new.yaml", "/tmp/x.yaml", "backup.json", "./cfg/dash.yml", "notes.md"}
	for _, p := range paths {
		if !looksLikeAPath(p) {
			t.Errorf("%q read as an entity_id; a legitimate --file value would be refused", p)
		}
		if mentionsAnIdentifier(p) {
			t.Errorf("%q counted as naming an object", p)
		}
	}

	entities := []string{"light.kitchen", "sensor.temperature", "automation.morning", "binary_sensor.door"}
	for _, e := range entities {
		if looksLikeAPath(e) {
			t.Errorf("%q read as a path; a real entity would reach the instance", e)
		}
		if !mentionsAnIdentifier(e) {
			t.Errorf("%q not recognised as naming an object", e)
		}
	}

	// The playground stays exempt in both directions.
	if mentionsAnIdentifier("automation.pg_w5_auto") {
		t.Error("a pg_ entity was treated as off-limits")
	}
	// A payload naming both must still be refused for the real one.
	if !mentionsAnIdentifier(`{"entity_id":["automation.pg_w5_auto","light.wohnzimmer"]}`) {
		t.Error("a real entity hid behind a pg_ one in the same payload")
	}
}
