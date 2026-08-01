package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/analyze"
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

// caseQuestionFlag reports whether a flag is one where case is a question: it
// narrows a listing (narrowsListing, derived from the flag's own declared
// purpose) and it does so by a value a person typed.
//
// Both halves are derived, neither is named. This was `filterFlags`, four names
// — pattern, name, area, label — typed into this file. The three `--domain`
// filters (`ent ls`, `helper ls`, `config entries`) and both `--component`
// filters were added afterwards, none of them was in the list, and so none of
// them was ever asked the question D-2 had already answered. `ent ls --domain
// SENSOR` returned 0 of 2 551 sensors on the reference instance and said
// "verify the domain exists" while it did so (live-fire #28's second half).
// A flag nobody listed was indistinguishable from a flag nobody needed to list.
func caseQuestionFlag(f *pflag.Flag) bool {
	return narrowsListing(f) && f.Value.Type() == "string"
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
	"hactl auto ls/label": func(n string) []string {
		rows := filterAutosByTag(parityAutoRows(), n)
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.id)
		}
		return out
	},
	"hactl script ls/label": func(n string) []string {
		rows := filterScriptsByLabel(parityScriptRows(), n)
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.id)
		}
		return out
	},
	"hactl script ls/pattern": func(n string) []string {
		rows := filterScriptsByPattern(parityScriptRows(), n)
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.id)
		}
		return out
	},
	"hactl helper ls/pattern": func(n string) []string {
		return helperRowIDs(filterHelperRowsByPattern(parityHelperRows(), n))
	},
	"hactl helper ls/name": func(n string) []string {
		return helperRowIDs(filterHelperRowsByName(parityHelperRows(), n))
	},
	"hactl helper ls/domain": func(n string) []string {
		return helperRowIDs(filterHelperRowsByDomain(parityHelperDomainRows(), n))
	},
	"hactl ent ls/domain": func(n string) []string {
		return entityIDs(filterEntitiesByDomain(parityDomainEntities(), n))
	},
	"hactl config entries/domain": func(n string) []string {
		out := make([]string, 0, 2)
		for _, e := range filterConfigEntriesByDomain(parityConfigEntries(), n) {
			out = append(out, e.EntryID)
		}
		return out
	},
	"hactl dash show/view": func(n string) []string {
		if _, ok := selectView(parityViews(), n); !ok {
			return nil
		}
		return []string{"wozi"}
	},
	"hactl log/component": func(n string) []string {
		out := make([]string, 0, 2)
		for _, e := range analyze.FilterByComponent(parityLogEntries(), n) {
			out = append(out, e.Component)
		}
		return out
	},
}

// forwardedFilters are the narrowing flags hactl does not apply. `companion
// logs` sends --component and --level to the add-on as query parameters and
// renders whatever comes back, so the case behaviour on the other side of that
// wire is the companion's contract and this package holds no filter to probe.
//
// They are recorded rather than skipped: TestFilterSurfaceIsClosed treats an
// entry here as a disposition, so a flag that is neither probed nor recorded
// still fails the build — the property every surface in dev/surfaces has, that
// no site can be silent.
var forwardedFilters = map[string]string{
	"hactl companion logs/component": "sent to the add-on as ?component= and applied there; " +
		"the records that cross the wire are already filtered, so there is no local filter to hold to a pole",
	"hactl companion logs/level": "sent to the add-on as ?level= and applied there as a minimum-level " +
		"threshold, which is a comparison rather than a match on a value read off the screen",
}

// parityViews are two Lovelace views: one whose PATH carries the needle and one
// whose TITLE does, so the probe covers both halves of selectView's match.
func parityViews() []json.RawMessage {
	return []json.RawMessage{
		json.RawMessage(`{"path":"Wozi","title":"Wohnzimmer","cards":[]}`),
		json.RawMessage(`{"path":"kueche","title":"Kitchen","cards":[]}`),
	}
}

// parityHelperDomainRows differ in DOMAIN, which parityHelperRows cannot
// express: its two rows differ in domain and in id at once, so a domain filter
// and an id filter would be indistinguishable against them.
func parityHelperDomainRows() []helperRow {
	return []helperRow{
		{ID: "mode_flag", Name: "Mode Flag", Domain: "Wozi", Source: "yaml"},
		{ID: "kitchen_timer", Name: "Kitchen Timer", Domain: "timer", Source: "yaml"},
	}
}

// parityDomainEntities carries one entity per domain so a --domain probe has a
// domain to keep and a domain to drop. parityEntities' two rows differ in
// domain AND in name, which a domain filter cannot tell apart.
func parityDomainEntities() []entityState {
	return []entityState{
		{EntityID: "Wozi.ceiling", State: "on"},
		{EntityID: "sensor.kitchen_temp", State: "21"},
	}
}

func parityConfigEntries() []configEntry {
	return []configEntry{
		{EntryID: "entry_wozi", Domain: "Wozi", Title: "Wozi Integration"},
		{EntryID: "entry_other", Domain: "kitchen", Title: "Kitchen Integration"},
	}
}

func parityLogEntries() []analyze.LogEntry {
	return []analyze.LogEntry{
		{Component: "Wozi.controller", Message: "started"},
		{Component: "kitchen.sensor", Message: "started"},
	}
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
	return []autoRow{
		{id: "Wozi_evening", labels: "Wozi Lights"},
		{id: "kitchen_fan", labels: "Kitchen"},
	}
}

func parityScriptRows() []scriptRow {
	return []scriptRow{
		{id: "Wozi_scene", labels: "Wozi Lights"},
		{id: "kitchen_reset", labels: "Kitchen"},
	}
}

// parityHelperRows mixes the two id shapes helper ls prints: a storage row's
// full entity_id and a yaml row's bare slug — mixed-case in ID and Name so the
// case gate proves something.
func parityHelperRows() []helperRow {
	return []helperRow{
		{ID: "input_boolean.Wozi_mode", Name: "Wozi Mode", Domain: "input_boolean", Source: "storage"},
		{ID: "kitchen_timer", Name: "Kitchen Timer", Domain: "timer", Source: "yaml"},
	}
}

func helperRowIDs(rows []helperRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
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
	for key := range walkCaseQuestionFlags() {
		if probes[key] == nil && forwardedFilters[key] == "" {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		t.Errorf("filter %q has no probe — add one to `probes` in surface_filter_test.go, "+
			"so its case behaviour is held to the D-2 pole alongside its siblings "+
			"(or, if hactl forwards it to the companion rather than applying it, say so in "+
			"`forwardedFilters` — the one thing it may not do is leave no trace)", k)
	}
	if len(probes) == 0 {
		t.Fatal("no probes are registered — the case gate proves nothing")
	}
}

// walkCaseQuestionFlags returns every "<command path>/<flag>" in the live tree
// where case is a question, keyed the way `probes` is.
func walkCaseQuestionFlags() map[string]*pflag.Flag {
	out := map[string]*pflag.Flag{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if caseQuestionFlag(f) {
				out[c.CommandPath()+"/"+f.Name] = f
			}
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	return out
}

// TestNarrowingFlagsDeclareThemselves holds the tree to the convention
// narrowsListing derives from: a flag that narrows a listing says so at the
// FRONT of its help text ("filter by …", "show only …").
//
// The derivation is only as good as the convention, and a convention with no
// gate is a comment. A flag whose help mentions filtering in the middle of a
// sentence would read to a human as a filter and to narrowsListing as not one,
// which is the exact failure mode the hand-written `filterFlags` had: silent
// absence from a surface. Here it fails the build, and the fix is a five-word
// edit to the help text.
func TestNarrowingFlagsDeclareThemselves(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		c.Flags().VisitAll(func(f *pflag.Flag) {
			usage := strings.ToLower(f.Usage)
			if !strings.Contains(usage, "filter") || narrowsListing(f) {
				return
			}
			t.Errorf("%s --%s says it filters (%q) but not at the front of its help, so "+
				"narrowsListing does not see it: it would leave the case gate and its "+
				"listing's empty answer would not name it. Reword the help to open with "+
				"\"filter …\" or \"show only …\".", c.CommandPath(), f.Name, f.Usage)
		})
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}

// TestGlobIsAnchoredAtEveryIdentifierFormTheListingPrints — D-28, over every
// --pattern in the tree rather than the one command that reported it.
//
// `helper ls --pattern 'anwesen*'` matched nothing while `--pattern
// anwesenheit` matched six, because the glob was anchored against the whole
// printed id and helper ids are `input_boolean.anwesenheit_flur` (finding #29).
// Every fixture below carries a record whose UNQUALIFIED name starts with the
// needle, so a glob anchored only at the front of the qualified id fails here
// exactly as it failed live.
//
// The set walked is the live tree's `--pattern` flags, so a listing that grows
// one inherits the rule; a probe missing from `probes` is already a build
// failure via TestFilterSurfaceIsClosed.
func TestGlobIsAnchoredAtEveryIdentifierFormTheListingPrints(t *testing.T) {
	anchored := 0
	for key, f := range walkCaseQuestionFlags() {
		if !strings.Contains(strings.ToLower(f.Usage), "glob") {
			continue
		}
		probe := probes[key]
		if probe == nil {
			continue // TestFilterSurfaceIsClosed reports this
		}
		anchored++
		substring := probe(parityNeedle)
		if len(substring) == 0 {
			t.Errorf("probe %q matches nothing as a substring — its fixture no longer exercises the filter", key)
			continue
		}
		if got := probe(parityNeedle + "*"); !equalSets(got, substring) {
			t.Errorf("%s: %q→%v but %q→%v.\n"+
				"    D-28 (docs/decisions.md): a glob is anchored at every identifier form the\n"+
				"    listing prints — the id as printed AND the part after the domain. Every\n"+
				"    record here is named %[4]q…, so a glob that finds fewer than the substring\n"+
				"    does is anchored against the domain prefix, not against the name.",
				key, parityNeedle+"*", got, parityNeedle, substring)
		}
	}
	if anchored == 0 {
		t.Fatal("no glob-documented filter was walked — the extractor has stopped matching and this gate proves nothing")
	}
}

// TestFilterFlagsAgreeOnCase — every filter flag, on every command, ignores
// case. This is the pole D-2 decided (docs/decisions.md), not mere agreement.
//
// This gate used to assert parity: the filter flags of one command had to
// agree with each other about case, whichever way. Parity was the honest gate
// while no decision existed, but it is satisfied by a command whose filters
// are all case-SENSITIVE — which is where commit 17039dd was headed when it
// deleted `strings.ToLower` from `device ls --pattern` to "harmonise" with
// `ent ls --pattern`, the sibling with no stake in the answer, since entity
// ids are always lowercase and case cannot bite there. A parity gate accepts
// that harmonisation finishing the job; this one refuses it at the first flag.
//
// The pole is case-insensitive because these flags match values a person read
// off a screen, and Home Assistant stores names exactly as a human typed them
// ("WoziSued", "Wozi Tv"). An empty listing reads as "no such thing" rather
// than "not spelled the way I store it", and under the manual's
// stop-at-the-first-miss rule that is a wrong answer, not a missing one.
func TestFilterFlagsAgreeOnCase(t *testing.T) {
	if len(probes) == 0 {
		t.Fatal("no probes are registered — the case gate proves nothing")
	}
	for _, key := range probeKeys() {
		probe := probes[key]
		exact := probe(parityNeedle)
		if len(exact) == 0 {
			t.Errorf("probe %q matches nothing even spelled exactly — its fixture no longer exercises the filter", key)
			continue
		}
		for _, needle := range []string{strings.ToLower(parityNeedle), strings.ToUpper(parityNeedle)} {
			if got := probe(needle); !equalSets(got, exact) {
				t.Errorf("%s is case-sensitive: %q→%v but %q→%v.\n"+
					"    D-2 (docs/decisions.md): every filter flag ignores case. A caller who types a name\n"+
					"    they read off the screen must get the same answer whatever case they typed;\n"+
					"    do not harmonise a sibling toward case-sensitivity to fix this.",
					key, needle, got, parityNeedle, exact)
			}
		}
	}
}

func probeKeys() []string {
	out := make([]string, 0, len(probes))
	for k := range probes {
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
