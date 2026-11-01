package policy

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestApplicableUnderPatterns — D-110: a verb applies at a pattern when the
// table lists it at an address the pattern covers, its own or one below it;
// `*` covers every kind, a system id glob picks its rows, a free or closed
// segment covers the row's `*`.
func TestApplicableUnderPatterns(t *testing.T) {
	all := []Verb{Control, Edit, Create, Delete}
	for pat, want := range map[string][]Verb{
		"*":                                 all,
		"entity:*":                          all,
		"entity:*/registry":                 {Edit},
		"entity:*/registry/entity_id":       {Edit},
		"entity:*/attr":                     {},
		"entity:*/attr/*":                   {},
		"entity:*/state":                    {},
		"device:*":                          {Edit, Delete},
		"automation:pg_*":                   all,
		"automation:pg_*/trace":             {},
		"script:*/run":                      {Control},
		"helper:*/*":                        {Control, Create, Delete},
		"helper:*/*/config":                 {},
		"dashboard:*/views/*":               {Edit, Create, Delete},
		"dashboard:*/views":                 {Edit, Create, Delete},
		"dashboard:/resources":              {},
		"config_entry:*":                    {Edit, Create, Delete},
		"config_entry:*/flow":               {},
		"file:**":                           {Edit},
		"file:x.yaml/block/*":               {},
		"service:light.*":                   {Control},
		"system:*":                          {Control, Edit, Delete},
		"system:w*":                         {Control, Edit},
		"system:cache":                      {Edit, Delete},
		"system:logs":                       {},
		"system:reload/*":                   {},
		"area:*":                            {Edit, Create, Delete},
		"floor:*":                           {Create, Delete},
		"label:night":                       {Create, Delete},
		"template:*/config":                 {Edit},
		"entity:device_tracker.*/attr/lat*": {},
	} {
		a, err := parseAddress(pat, false)
		if err != nil {
			t.Fatalf("%s: %v", pat, err)
		}
		if got := applicableUnder(a); !slices.Equal(got, want) {
			t.Errorf("applicableUnder(%s) = %v, want %v", pat, got, want)
		}
	}
}

// TestApplicableAtOrBelowAResource — what `policy check … write` answers
// (D-120): the resource's own verbs plus every verb below it.
func TestApplicableAtOrBelowAResource(t *testing.T) {
	for res, want := range map[string][]Verb{
		"entity:light.x":                 {Control, Edit, Create, Delete},
		"entity:light.x/registry/labels": {Edit},
		"entity:light.x/attr/brightness": {},
		"script:s1":                      {Control, Edit, Create, Delete},
		"script:s1/run":                  {Control},
		"helper:input_boolean/h1":        {Control, Create, Delete},
		"system:reload":                  {},
		"system:wireguard":               {Control, Edit},
		"dashboard:":                     {Edit, Create, Delete},
	} {
		if got := ApplicableAtOrBelow(mustResource(t, res)); !slices.Equal(got, want) {
			t.Errorf("ApplicableAtOrBelow(%s) = %v, want %v", res, got, want)
		}
	}
}

// TestWriteNarrowsPerRuleAndKeepsNamedVerbs — D-110 (1) and (2): `write`
// becomes the verbs that apply at the pattern; a verb written by name stays,
// applicable or not; read is untouched; a list without `write` is as parsed.
func TestWriteNarrowsPerRuleAndKeepsNamedVerbs(t *testing.T) {
	p := mustLoad(t, profileHead+
		"    deny:\n"+
		"      \"entity:*/registry/entity_id\": [write, control]\n"+
		"      \"entity:*/attr/*\": [read, write]\n"+
		"      \"entity:lock.*\": [write]\n"+
		"      \"entity:*/state\": [control]\n"+
		"      \"system:reload/*\": [write]\n")
	want := map[string][]Verb{
		"entity:*/registry/entity_id": {Control, Edit},
		"entity:*/attr/*":             {Read},
		"entity:lock.*":               {Control, Edit, Create, Delete},
		"entity:*/state":              {Control},
		"system:reload/*":             {},
	}
	for _, r := range p.Rules() {
		if got := r.Verbs; !slices.Equal(got, want[r.Pattern]) || got == nil {
			t.Errorf("%s: Verbs %v, want %v (never nil)", r.Pattern, got, want[r.Pattern])
		}
	}
	// The named control on the registry field still decides a direct check
	// of exactly that address, and makes nothing partial.
	if v := p.Decide(mustResource(t, "entity:light.x/registry/entity_id"), Control, nil); v.Allowed {
		t.Errorf("control on the registry field = %+v, want denied by the rule as written", v)
	}
	if v := p.DecideDenyOnly(mustResource(t, "entity:light.y"), Control); !v.Allowed || v.Partial {
		t.Errorf("deny-only control on entity:light.y = %+v, want allowed: control applies at none of the denies below it", v)
	}
}

// TestVerbNotApplicableFindings — D-110 (2): one finding per rule and named
// write verb that applies nowhere the pattern reaches, one per `write` that
// applies to nothing; read and `*` are never flagged; a kind on which the verb
// is checked nowhere says so.
func TestVerbNotApplicableFindings(t *testing.T) {
	p := mustLoad(t, profileHead+
		"    allow:\n"+
		"      \"*\": [read, control]\n"+ // 7: `*` covers every kind
		"      \"area:*\": [control, edit]\n"+ // 8: control on area applies nowhere
		"    deny:\n"+
		"      \"entity:*/state\": [read, control, edit]\n"+ // 10: two findings, read never
		"      \"file:x.yaml/block/b\": [write]\n"+ // 11: write applies to nothing
		"      \"entity:*/registry/labels\": [write]\n") // 12: fine
	got := verbNotApplicable(p)
	want := []Finding{
		{CodeVerbNotApplicable, "p", `allow "area:*" [control edit]`, 8,
			`"control" does not apply at area:* — no command asks for it there, so the rule decides nothing for it (no command asks for control on any area)`},
		{CodeVerbNotApplicable, "p", `deny "entity:*/state" [read control edit]`, 10,
			`"control" does not apply at entity:*/state — no command asks for it there, so the rule decides nothing for it (control on entity is checked at: entity:<id>)`},
		{CodeVerbNotApplicable, "p", `deny "entity:*/state" [read control edit]`, 10,
			`"edit" does not apply at entity:*/state — no command asks for it there, so the rule decides nothing for it (edit on entity is checked at: entity:<id>/registry/<field>)`},
		{CodeVerbNotApplicable, "p", `deny "file:x.yaml/block/b" [write]`, 11,
			`"write" applies to nothing at file:x.yaml/block/b — no command changes it, so the rule decides nothing for writes`},
	}
	sortFindings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("findings =\n%+v\nwant\n%+v", got, want)
	}
	// Lint reports them with a nil inventory too: the check needs no instance.
	rep := Lint(p, nil)
	n := 0
	for _, f := range rep.Findings {
		if f.Code == CodeVerbNotApplicable {
			n++
		}
	}
	if n != len(want) {
		t.Errorf("Lint reported %d verb-not-applicable findings, want %d: %+v", n, len(want), rep.Findings)
	}
}

// TestUnmetWhereNamesTheFirstFailingKey — D-112: each narrowed allow tried
// whose selector is false is one entry, in the order tried, with the first key
// that failed (area before label); a device's own area and labels are asked;
// an unevaluable selector is a note, not an entry; an allow that grants
// empties the list.
func TestUnmetWhereNamesTheFirstFailingKey(t *testing.T) {
	p := mustLoad(t, profileHead+
		"    allow:\n"+
		"      \"entity:light.*\": [control]\n"+ // 7
		"      \"entity:light.k*\": [control]\n"+ // 8
		"      \"device:*\": [delete]\n"+ // 9
		"    where:\n"+
		"      \"entity:light.*\": {area: [wohnzimmer, buero], label: agent_ok}\n"+
		"      \"entity:light.k*\": {label: [agent_ok, pg_*]}\n"+
		"      \"device:*\": {area: hall}\n")
	live := fakeLive{
		entityArea:   map[string]Ref{"light.kitchen": {ID: "kitchen_id", Name: "Kitchen"}, "light.kuehl": {ID: "buero", Name: "Büro"}},
		entityLabels: map[string][]Ref{"light.kuehl": {{ID: "energy", Name: "Energy"}}},
		deviceArea:   map[string]Ref{"dev_in_garage": {ID: "garage", Name: "Garage"}},
	}
	for _, c := range []struct {
		res  string
		verb Verb
		want []UnmetWhere
	}{
		{"entity:light.kitchen", Control, []UnmetWhere{
			{`allow "entity:light.*" [control] where area=wohnzimmer|buero label=agent_ok`, 7, "light.kitchen is in area kitchen_id, not wohnzimmer or buero"},
			{`allow "entity:light.k*" [control] where label=agent_ok|pg_*`, 8, "light.kitchen has no label agent_ok or pg_*"},
		}},
		// The area holds, so the label is the first key that fails.
		{"entity:light.kuehl", Control, []UnmetWhere{
			{`allow "entity:light.*" [control] where area=wohnzimmer|buero label=agent_ok`, 7, "light.kuehl has no label agent_ok"},
			{`allow "entity:light.k*" [control] where label=agent_ok|pg_*`, 8, "light.kuehl has no label agent_ok or pg_*"},
		}},
		{"entity:light.attic", Control, []UnmetWhere{
			{`allow "entity:light.*" [control] where area=wohnzimmer|buero label=agent_ok`, 7, "light.attic has no area"},
		}},
		{"device:dev_in_garage", Delete, []UnmetWhere{{`allow "device:*" [delete] where area=hall`, 9, "dev_in_garage is in area garage, not hall"}}},
		{"device:dev_nowhere", Delete, []UnmetWhere{{`allow "device:*" [delete] where area=hall`, 9, "dev_nowhere has no area"}}},
		{"entity:switch.x", Control, []UnmetWhere{}}, // no narrowed allow matched
	} {
		v := p.Decide(mustResource(t, c.res), c.verb, live)
		if v.Allowed || v.Rule != RuleDefaultDeny || !reflect.DeepEqual(v.UnmetWhere, c.want) {
			t.Errorf("%s %s = allowed %v rule %q unmet %+v, want denied by the default with %+v", c.res, c.verb, v.Allowed, v.Rule, v.UnmetWhere, c.want)
		}
	}

	// An earlier unmet selector, then a grant: the list is empty.
	q := mustLoad(t, profileHead+
		"    allow:\n"+
		"      \"entity:light.k*\": [control]\n"+
		"      \"entity:light.*\": [control]\n"+
		"    where:\n"+
		"      \"entity:light.k*\": {label: agent_ok}\n")
	if v := q.Decide(mustResource(t, "entity:light.kitchen"), Control, live); !v.Allowed || v.UnmetWhere == nil || len(v.UnmetWhere) != 0 {
		t.Errorf("granted after an unmet selector = %+v, want allowed with an empty unmet_where", v)
	}

	// Home Assistant unreachable: a note, never an entry.
	v := p.Decide(mustResource(t, "entity:light.kitchen"), Control, fakeLive{err: errors.New("HA unreachable")})
	if len(v.UnmetWhere) != 0 || len(v.Notes) != 2 || !strings.Contains(v.Notes[0], "HA unreachable") {
		t.Errorf("unevaluable selectors = notes %q unmet %+v, want two notes and no entry", v.Notes, v.UnmetWhere)
	}

	// Every evaluator returns an empty, never nil, list.
	for _, v := range []Verdict{
		p.DecideDenyOnly(mustResource(t, "automation:x"), Control),
		p.DecideKindWide(KindArea, "", Create),
		p.DecideKindWideDenyOnly(KindScript, "", Control),
	} {
		if v.UnmetWhere == nil {
			t.Errorf("verdict %+v has a nil UnmetWhere", v)
		}
	}
}
