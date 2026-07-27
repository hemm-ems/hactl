package cmd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// surfaceRepoRoot walks up to the go.mod that owns this tree.
func surfaceRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// filterFlags are the flags that narrow a listing by something a person read
// off the screen. They are the flags where case is a question.
var filterFlags = map[string]bool{
	"pattern": true, "name": true, "area": true, "label": true,
}

// filterProbe drives one command's filter with one needle and reports which
// records survived. A probe calls the filter the command itself calls, never a
// reimplementation, so it cannot agree with a filter that has changed.
type filterProbe func(needle string) []string

// parityNeedle is mixed-case and matches every fixture below.
//
// Home Assistant's device names are human-written — "WoziSued", "Wozi Tv" — so
// lowercase is a caller's natural first guess, and a case-sensitive filter
// answers "no devices" to a question that has answers.
const parityNeedle = "Wozi"

// probes binds every (command, filter flag) pair to a way of exercising it.
// TestFilterSurfaceIsClosed checks the map against the live cobra tree, so a
// filter flag added to any listing command fails the build until somebody says
// how it is exercised.
var probes = map[string]filterProbe{
	"hactl device ls/pattern": func(n string) []string {
		return deviceProbe(func() { flagDevicePattern = n })
	},
	"hactl device ls/name": func(n string) []string {
		return deviceProbe(func() { flagDeviceName = n })
	},
	"hactl device ls/area": func(n string) []string {
		return deviceProbe(func() { flagDeviceArea = n })
	},
	"hactl device ls/label": func(n string) []string {
		return deviceProbe(func() { flagDeviceLabel = n })
	},
	"hactl ent ls/pattern": func(n string) []string {
		return entityIDs(filterEntitiesByPattern(parityEntities(), n))
	},
	"hactl ent ls/area": func(n string) []string {
		return entityIDs(filterEntitiesByArea(parityEntities(), parityRegistry(), n))
	},
	"hactl ent ls/label": func(n string) []string {
		return entityIDs(filterEntitiesByLabel(parityEntities(), parityRegistry(), n))
	},
	"hactl auto ls/pattern": func(n string) []string {
		rows := filterAutosByPattern(parityAutoRows(), n)
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.id)
		}
		return out
	},
	"hactl auto ls/label":   parityLabelIDs,
	"hactl script ls/label": parityLabelIDs,
	"hactl script ls/pattern": func(n string) []string {
		rows := filterScriptsByPattern(parityScriptRows(), n)
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.id)
		}
		return out
	},
}

// ---------------------------------------------------------------------------
// fixtures — one record that matches parityNeedle and one that never does
// ---------------------------------------------------------------------------

func parityDevices() []haapi.DeviceRegistryEntry {
	return []haapi.DeviceRegistryEntry{
		{ID: "dev_wozi_1", Name: "Wozi Light Bulbs V2", AreaID: "area_wohnzimmer", Labels: []string{"lbl_wozi"}},
		{ID: "dev_other", Name: "Kitchen Sensor", AreaID: "area_kueche"},
	}
}

func parityEntities() []entityState {
	return []entityState{
		{EntityID: "light.Wozi_ceiling", State: "on"},
		{EntityID: "sensor.kitchen_temp", State: "21"},
	}
}

func parityAutoRows() []autoRow {
	return []autoRow{{id: "Wozi_evening"}, {id: "kitchen_fan"}}
}

func parityScriptRows() []scriptRow {
	return []scriptRow{{id: "Wozi_scene"}, {id: "kitchen_reset"}}
}

func parityAreas() map[string]haapi.AreaEntry {
	return map[string]haapi.AreaEntry{"area_wohnzimmer": {AreaID: "area_wohnzimmer", Name: "Wozi Wohnzimmer"}}
}

func parityLabels() map[string]haapi.LabelEntry {
	return map[string]haapi.LabelEntry{"lbl_wozi": {LabelID: "lbl_wozi", Name: "Wozi Lights"}}
}

func parityDeviceRegistry() *deviceRegistryContext {
	return &deviceRegistryContext{
		devices:   parityDevices(),
		areaByID:  parityAreas(),
		labelByID: parityLabels(),
	}
}

func parityRegistry() *registryContext {
	return &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			"light.Wozi_ceiling":  {EntityID: "light.Wozi_ceiling", AreaID: "area_wohnzimmer", Labels: []string{"lbl_wozi"}},
			"sensor.kitchen_temp": {EntityID: "sensor.kitchen_temp", AreaID: "area_kueche"},
		},
		areaByID:  parityAreas(),
		labelByID: parityLabels(),
	}
}

// deviceProbe runs filterDevices with exactly one filter flag set, so that the
// four flags are compared one at a time rather than in combination.
func deviceProbe(set func()) []string {
	p, n, a, l := flagDevicePattern, flagDeviceName, flagDeviceArea, flagDeviceLabel
	defer func() { flagDevicePattern, flagDeviceName, flagDeviceArea, flagDeviceLabel = p, n, a, l }()
	flagDevicePattern, flagDeviceName, flagDeviceArea, flagDeviceLabel = "", "", "", ""
	set()
	kept := filterDevices(parityDevices(), parityDeviceRegistry())
	out := make([]string, 0, len(kept))
	for _, d := range kept {
		out = append(out, d.ID)
	}
	return out
}

func entityIDs(states []entityState) []string {
	out := make([]string, 0, len(states))
	for _, s := range states {
		out = append(out, s.EntityID)
	}
	return out
}

func parityLabelIDs(needle string) []string {
	matched := matchingLabelIDs(parityLabels(), needle)
	out := make([]string, 0, len(matched))
	for id := range matched {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// the gates
// ---------------------------------------------------------------------------

// TestFilterSurfaceIsClosed — every filter flag on every command has a probe.
//
// Without this, adding `--pattern` to a new listing command adds a filter that
// nothing compares against its siblings, which is how the four filters inside
// filterDevices came to disagree with one another.
func TestFilterSurfaceIsClosed(t *testing.T) {
	var missing []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if !filterFlags[f.Name] {
				return
			}
			if key := c.CommandPath() + "/" + f.Name; probes[key] == nil {
				missing = append(missing, key)
			}
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	sort.Strings(missing)
	for _, k := range missing {
		t.Errorf("filter %q has no parity probe — add one to `probes` in surface_filter_test.go, "+
			"so its case behaviour is compared against its siblings", k)
	}
	if len(probes) == 0 {
		t.Fatal("no probes are registered — the parity gate proves nothing")
	}
}

// TestFilterFlagsAgreeOnCase — the filter flags of one command answer the same
// question whatever case the caller typed.
//
// The rule is parity, not a particular pole, and that is deliberate. When two
// sibling behaviours disagree, the tempting move is to harmonise them, and a
// harmonisation is only as good as the sibling chosen as the model. Commit
// 17039dd deleted `strings.ToLower` from `device ls --pattern` to match `ent ls
// --pattern` — the sibling with no stake in the answer, since entity ids are
// always lowercase and case cannot bite there — while the three filters beside
// it in filterDevices kept case-insensitivity. A gate demanding one pole would
// have been satisfied by that commit. A gate demanding agreement is not.
func TestFilterFlagsAgreeOnCase(t *testing.T) {
	byCommand := map[string]map[string]bool{}
	for key, probe := range probes {
		cmdPath, flag, _ := strings.Cut(key, "/")
		lower, upper, exact := probe(strings.ToLower(parityNeedle)), probe(strings.ToUpper(parityNeedle)), probe(parityNeedle)
		if len(exact) == 0 {
			t.Errorf("probe %q matches nothing even spelled exactly — its fixture no longer exercises the filter", key)
			continue
		}
		insensitive := equalSets(lower, exact) && equalSets(upper, exact)
		if byCommand[cmdPath] == nil {
			byCommand[cmdPath] = map[string]bool{}
		}
		byCommand[cmdPath][flag] = insensitive
		if !insensitive {
			t.Logf("%s is case-SENSITIVE: %q→%v  %q→%v  %q→%v",
				key, strings.ToLower(parityNeedle), lower, parityNeedle, exact, strings.ToUpper(parityNeedle), upper)
		}
	}

	for _, cmdPath := range parityCommands(byCommand) {
		var sensitive, insensitive []string
		for flag, ins := range byCommand[cmdPath] {
			if ins {
				insensitive = append(insensitive, "--"+flag)
			} else {
				sensitive = append(sensitive, "--"+flag)
			}
		}
		if len(sensitive) == 0 || len(insensitive) == 0 {
			continue
		}
		sort.Strings(sensitive)
		sort.Strings(insensitive)
		t.Errorf("`%s` disagrees with itself about case: %s ignore it, %s do not.\n"+
			"    A caller who types a name they read off the screen gets an answer from one flag and an\n"+
			"    empty listing from the other, and an empty listing reads as \"no such thing\" rather than\n"+
			"    \"not spelled the way I store it\". Pick a pole for the whole command.",
			cmdPath, strings.Join(insensitive, ", "), strings.Join(sensitive, ", "))
	}
}

func parityCommands(m map[string]map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// TestEntLsAreaAcceptsTheIDAreaLsPrints — H-17 in the area family.
//
// `area ls` prints area_id in its first column and docs/manual.md routes a
// caller who cannot find something to "the matching registry ls". `ent ls
// --area` matched the area NAME only, so the id it had just been handed
// returned zero rows at exit 0, while `device ls --area` returned the row for
// the same input. The two filters disagreed with each other.
func TestEntLsAreaAcceptsTheIDAreaLsPrints(t *testing.T) {
	for _, needle := range []string{
		"area_wohnzimmer", // the id, as `area ls` prints it
		"AREA_WOHNZIMMER", // and its case does not decide the answer
		"Wozi Wohnzimmer", // the name
		"wozi wohnzimmer", // and its case does not either
	} {
		t.Run(needle, func(t *testing.T) {
			got := entityIDs(filterEntitiesByArea(parityEntities(), parityRegistry(), needle))
			if len(got) != 1 || got[0] != "light.Wozi_ceiling" {
				t.Errorf("ent ls --area %q matched %v, want [light.Wozi_ceiling]", needle, got)
			}
		})
	}
	if got := entityIDs(filterEntitiesByArea(parityEntities(), parityRegistry(), "no_such_area")); len(got) != 0 {
		t.Errorf("an area that does not exist matched %v, want nothing — the negative control", got)
	}
}
