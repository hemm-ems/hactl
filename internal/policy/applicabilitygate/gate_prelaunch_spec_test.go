// Package applicabilitygate holds the B-1 lockstep gates (D-110) for the
// applicability table: the write verbs that apply at each address.
//
// It is a test-only package on purpose. The gates need one new exported
// function the contract implies but does not exist yet —
//
//	func policy.ApplicableVerbs(r policy.Resource) []policy.Verb
//
// the write verbs (control, edit, create, delete, in that order) that apply
// exactly at r, i.e. the Go table — and keeping them out of internal/policy
// lets that package's own tests compile and run while it is missing. Once the
// function exists these tests may move into internal/policy unchanged.
//
// Written from the contract only (D-110, the manual's "Verbs" table and its
// "What each write command checks" table, the request vectors), without
// reading the evaluator or the request layer's mapping code.
package applicabilitygate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/docs"
	"github.com/hemm-ems/hactl/internal/policy"
)

var writeVerbs = []policy.Verb{policy.Control, policy.Edit, policy.Create, policy.Delete}

// shape is a resource's (kind, sub shape) as D-110 defines it: closed words
// literal (`registry/labels`, `config`, `run`, `options`), free segments as `*`
// (`views/*`, `attr/*`, `block/*`); for system the fixed id is part of it.
func shape(r policy.Resource) string {
	if r.Kind == policy.KindSystem {
		s := "system:" + r.ID
		if r.Sub != "" {
			s += "/*"
		}
		return s
	}
	var spec policy.KindSpec
	for _, ks := range policy.KindSpecs() {
		if ks.Kind == r.Kind {
			spec = ks
		}
	}
	if r.Sub == "" {
		return string(r.Kind) + ":"
	}
	segs := strings.Split(r.Sub, "/")
	for _, sub := range spec.Subs {
		if sub.Word != segs[0] {
			continue
		}
		if len(segs) > 1 && sub.ParamValues == nil {
			segs[1] = "*"
		}
	}
	return string(r.Kind) + ":/" + strings.Join(segs, "/")
}

// parse reads a check's resource; a kind-wide check (`<kind>:*`) is read as
// one sample id of the kind — the shape is what counts.
func parse(t *testing.T, s string) policy.Resource {
	t.Helper()
	kind, rest, _ := strings.Cut(s, ":")
	if rest == "*" || strings.HasPrefix(rest, "*/") {
		sample := map[string]string{"entity": "light.x", "service": "light.turn_on", "helper": "input_boolean/x"}[kind]
		if sample == "" {
			sample = "x"
		}
		s = kind + ":" + sample + strings.TrimPrefix(rest, "*")
	}
	r, err := policy.ParseResource(s)
	if err != nil {
		t.Fatalf("ParseResource(%q): %v", s, err)
	}
	return r
}

type check struct {
	Resource string `json:"resource"`
	Verb     string `json:"verb"`
}

// emittedChecks are the (resource, verb) pairs the request layer and the
// dashboard diff are specified to emit — testdata/policy/requests/vectors.json
// and dashboard_diff.json, both written from dev/surfaces/policy.manifest.
func emittedChecks(t *testing.T) []check {
	t.Helper()
	var out []check
	for _, name := range []string{"vectors.json", "dashboard_diff.json"} {
		raw, err := os.ReadFile(filepath.Clean(filepath.Join("..", "..", "..", "testdata", "policy", "requests", name)))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Vectors []struct {
				Expect struct {
					Checks []check `json:"checks"`
				} `json:"expect"`
			} `json:"vectors"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, v := range doc.Vectors {
			out = append(out, v.Expect.Checks...)
		}
	}
	if len(out) == 0 {
		t.Fatal("the request vectors hold no checks — the gate would compare nothing")
	}
	return out
}

// commandPlanShapes are the (shape, verb) pairs the command plans emit beyond
// the request vectors, transcribed from the manual's "What each write command
// checks" table: svc call's deny-only typed checks, the typed create/delete/
// config edits, the kind-wide creates, the cascades, dash save's views, the
// config-flow checks, wireguard and the cache.
var commandPlanShapes = []string{
	"automation: control", "script: control", "helper: control", "template: control", // svc call, typed, deny-only
	"script:/run control", "service: control", "entity: control",
	"script: create", "automation: create", "helper: create", "template: create",
	"automation:/config edit", "script:/config edit", "template:/config edit",
	"automation: delete", "script: delete", "helper: delete", "template: delete", "entity: delete",
	"area: create", "floor: create", "label: create",
	"area: delete", "floor: delete", "label: delete", "area: edit",
	"entity:/registry/area_id edit", "device:/registry/area_id edit",
	"entity:/registry/labels edit", "device:/registry/labels edit",
	"entity:/registry/entity_id edit", "entity: create",
	"file: edit", "dashboard: edit", "dashboard:/views/* edit", "dashboard:/views/* create", "dashboard:/views/* delete",
	"dashboard: create", "dashboard: delete",
	"config_entry: delete", "device: delete", "config_entry:/options edit", "config_entry: create", "config_entry: edit",
	"system:wireguard edit", "system:wireguard control",
	"system:cache delete", "system:cache edit",
}

func emittedShapes(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, c := range emittedChecks(t) {
		if c.Verb == "read" {
			continue
		}
		out[shape(parse(t, c.Resource))+" "+c.Verb] = true
	}
	for _, s := range commandPlanShapes {
		out[s] = true
	}
	return out
}

func sorted(ss []string) []string {
	out := slices.Clone(ss)
	sort.Strings(out)
	return out
}

func verbNames(vs []policy.Verb) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.String())
	}
	return out
}

// TestEveryEmittedTripleIsInTheApplicabilityTable — D-110's gate, first
// direction: a (kind, sub shape, verb) the request layer or a command plan
// emits that the table does not list would be a verb `write` no longer covers
// there — a deny that silently stops refusing.
func TestEveryEmittedTripleIsInTheApplicabilityTable(t *testing.T) {
	for _, c := range emittedChecks(t) {
		if c.Verb == "read" {
			continue
		}
		r := parse(t, c.Resource)
		if got := verbNames(policy.ApplicableVerbs(r)); !slices.Contains(got, c.Verb) {
			t.Errorf("%s %s is emitted, but the table lists %v at %s", c.Verb, c.Resource, got, shape(r))
		}
	}
}

// samples is one concrete address per (kind, sub shape) the grammar allows —
// every sub word, every closed registry field, one free segment.
func samples(t *testing.T) []policy.Resource {
	t.Helper()
	ids := map[policy.Kind]string{
		policy.KindEntity: "light.x", policy.KindHelper: "input_boolean/h1", policy.KindFile: "packages/x.yaml",
		policy.KindService: "light.turn_on", policy.KindDashboard: "main",
	}
	var out []policy.Resource
	for _, ks := range policy.KindSpecs() {
		idList := ks.IDs
		if idList == nil {
			id := ids[ks.Kind]
			if id == "" {
				id = "x1"
			}
			idList = []string{id}
		}
		for _, id := range idList {
			out = append(out, policy.Resource{Kind: ks.Kind, ID: id})
			for _, sub := range ks.Subs {
				if ks.Kind == policy.KindSystem {
					if id == "reload" {
						out = append(out, policy.Resource{Kind: ks.Kind, ID: id, Sub: "light"})
					}
					continue
				}
				out = append(out, policy.Resource{Kind: ks.Kind, ID: id, Sub: sub.Word})
				switch {
				case sub.ParamValues != nil:
					for _, p := range sub.ParamValues {
						out = append(out, policy.Resource{Kind: ks.Kind, ID: id, Sub: sub.Word + "/" + p})
					}
				case sub.Param:
					out = append(out, policy.Resource{Kind: ks.Kind, ID: id, Sub: sub.Word + "/p1"})
				}
			}
		}
	}
	return out
}

// TestTheApplicabilityTableHoldsNothingNothingEmits — the second direction: a
// triple in the table that no request row and no command plan emits makes
// `write` wider there than any command asks, and quietly re-widens what a
// deny refuses.
func TestTheApplicabilityTableHoldsNothingNothingEmits(t *testing.T) {
	emitted := emittedShapes(t)
	for _, r := range samples(t) {
		for _, v := range policy.ApplicableVerbs(r) {
			if !emitted[shape(r)+" "+v.String()] {
				t.Errorf("the table lists %s at %s (%s), but nothing emits it", v, shape(r), r)
			}
		}
	}
}

// TestApplicableVerbsAreWriteVerbsInCanonicalOrder — read applies everywhere
// and is not part of the table; the order is control, edit, create, delete.
func TestApplicableVerbsAreWriteVerbsInCanonicalOrder(t *testing.T) {
	for _, r := range samples(t) {
		got := policy.ApplicableVerbs(r)
		var want []policy.Verb
		for _, v := range writeVerbs {
			if slices.Contains(got, v) {
				want = append(want, v)
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("ApplicableVerbs(%s) = %v, want write verbs only, in the order control, edit, create, delete", r, verbNames(got))
		}
	}
}

// --- the manual's table is the Go table --------------------------------------

var manualTableHead = "| address | write verbs that apply there |"

// manualSample maps each address the manual's table writes to the sample
// resource this test checks it at.
var manualSample = map[string]string{
	"entity:<id>":                       "entity:light.x",
	"entity:<id>/registry/<field>":      "entity:light.x/registry/labels",
	"device:<id>":                       "device:dev1",
	"device:<id>/registry/<field>":      "device:dev1/registry/area_id",
	"area:<id>":                         "area:hall",
	"floor:<id>":                        "floor:ground",
	"label:<id>":                        "label:night",
	"automation:<id>":                   "automation:a1",
	"template:<id>":                     "template:t1",
	"helper:<domain>/<id>":              "helper:input_boolean/h1",
	"script:<id>":                       "script:s1",
	"automation:<id>/config":            "automation:a1/config",
	"template:<id>/config":              "template:t1/config",
	"script:<id>/config":                "script:s1/config",
	"script:<id>/run":                   "script:s1/run",
	"dashboard:<url_path>":              "dashboard:main",
	"dashboard:<url_path>/views/<path>": "dashboard:main/views/v1",
	"config_entry:<id>":                 "config_entry:e1",
	"config_entry:<id>/options":         "config_entry:e1/options",
	"file:<path>":                       "file:packages/x.yaml",
	"service:<domain>.<service>":        "service:light.turn_on",
	"system:wireguard":                  "system:wireguard",
	"system:cache":                      "system:cache",
	"entity:<id>/state":                 "entity:light.x/state",
	"/attr/<name>":                      "entity:light.x/attr/brightness",
	"/history":                          "entity:light.x/history",
	"/stats":                            "entity:light.x/stats",
	"automation:<id>/trace":             "automation:a1/trace",
	"script:<id>/trace":                 "script:s1/trace",
	"helper:<domain>/<id>/config":       "helper:input_boolean/h1/config",
	"dashboard:<url_path>/resources":    "dashboard:main/resources",
	"config_entry:<id>/flow":            "config_entry:e1/flow",
	"file:<path>/block/<id>":            "file:packages/x.yaml/block/b1",
	"system:logs":                       "system:logs",
	"system:check_config":               "system:check_config",
	"system:reload":                     "system:reload",
}

var (
	backticked    = regexp.MustCompile("`([^`]+)`")
	parenthetical = regexp.MustCompile(`\s*\([^)]*\)`)
)

// TestManualApplicabilityTableIsTheGoTable — D-110: "the manual's table is the
// Go table, held in lockstep by a test." Every address the manual's table
// names answers ApplicableVerbs with exactly the verbs its row lists (none for
// the last row), and the table names exactly the addresses above.
func TestManualApplicabilityTableIsTheGoTable(t *testing.T) {
	_, rest, ok := strings.Cut(docs.Manual, manualTableHead)
	if !ok {
		t.Fatalf("the manual has no applicability table (%q)", manualTableHead)
	}
	table, _, _ := strings.Cut(rest, "\n\n")
	seen := map[string]bool{}
	for line := range strings.SplitSeq(table, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 4 || strings.HasPrefix(strings.TrimSpace(cells[1]), "---") {
			continue
		}
		var want []string
		if verbs := parenthetical.ReplaceAllString(strings.TrimSpace(cells[2]), ""); !strings.HasPrefix(verbs, "none") {
			for part := range strings.SplitSeq(verbs, ",") {
				want = append(want, strings.Fields(part)[0])
			}
		}
		for _, m := range backticked.FindAllStringSubmatch(cells[1], -1) {
			addr := m[1]
			sample, ok := manualSample[addr]
			if !ok {
				t.Errorf("the manual's table names %q, which this test has no sample for — add it", addr)
				continue
			}
			seen[addr] = true
			r, err := policy.ParseResource(sample)
			if err != nil {
				t.Fatalf("sample %q: %v", sample, err)
			}
			got := verbNames(policy.ApplicableVerbs(r))
			// The manual writes some rows in its own order (create, edit,
			// delete); the canonical order is the test above's concern.
			if !slices.Equal(sorted(got), sorted(want)) {
				t.Errorf("manual row %q lists %v; ApplicableVerbs(%s) = %v", addr, want, sample, got)
			}
		}
	}
	var missing []string
	for addr := range manualSample {
		if !seen[addr] {
			missing = append(missing, addr)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the manual's table no longer names %v", missing)
	}
}
