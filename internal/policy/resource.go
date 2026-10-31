package policy

import (
	"fmt"
	"strings"
)

// Kind is the first part of a resource address, `<kind>:<id>[/<sub>…]`. The set
// is closed: a kind not listed here is a D-60 problem in a rule and a refusal in
// `policy check`. Kinds are spelled exactly as below (lowercase, no folding).
type Kind string

// The fourteen kinds (SPEC-permissions.md §2.1, manual "Resources").
const (
	KindEntity      Kind = "entity"
	KindDevice      Kind = "device"
	KindArea        Kind = "area"
	KindFloor       Kind = "floor"
	KindLabel       Kind = "label"
	KindAutomation  Kind = "automation"
	KindScript      Kind = "script"
	KindTemplate    Kind = "template"
	KindHelper      Kind = "helper"
	KindDashboard   Kind = "dashboard"
	KindConfigEntry Kind = "config_entry"
	KindFile        Kind = "file"
	KindService     Kind = "service"
	KindSystem      Kind = "system"
)

// Kinds returns every kind in the manual's table order. Anything that renders
// or iterates kinds (lint's unchecked list, show's grammar help) uses this
// order or sorts; it never ranges over a map of kinds (H-16).
func Kinds() []Kind {
	return []Kind{
		KindEntity, KindDevice, KindArea, KindFloor, KindLabel,
		KindAutomation, KindScript, KindTemplate, KindHelper, KindDashboard,
		KindConfigEntry, KindFile, KindService, KindSystem,
	}
}

// KindSpec is the grammar of one kind: how many path segments its id spans,
// which sub-resources exist below it, and which matcher its id uses. It is the
// Go form of the manual's "Resources" table; the two must agree, and a test
// that parses the manual table against KindSpecs is welcome.
type KindSpec struct {
	Kind Kind

	// IDSegments is how many `/`-separated segments the id spans: 1 for most
	// kinds, 2 for helper (`<domain>/<id>`), 0 for file (one or more — a
	// companion-relative path). An id segment is never empty, except that the
	// default dashboard's whole id is "" (EmptyID).
	IDSegments int

	// EmptyID is true only for dashboard, whose default is url_path "" and is
	// addressed as `dashboard:` (and `dashboard:/views/<path>`).
	EmptyID bool

	// IDs, when non-nil, is the closed set of ids the kind has (system only).
	IDs []string

	// Qualified is true for the kinds whose ids are `<domain>.<name>` (entity,
	// service). Like every other id it is matched with glob.Match, against the
	// full id only: D-28's second anchor (the part after the domain) is for
	// filters, never for policy patterns (D-63). A wildcard-free pattern id of
	// a Qualified kind without a `.` can never match a real id, so it is a
	// D-60 problem, not a dead rule.
	Qualified bool

	// Subs are the sub-resources below an id. A sub is one fixed word,
	// optionally followed by one free segment.
	Subs []SubSpec

	// Where is true for the kinds a `where:` selector may narrow (entity,
	// device). A `where:` on any other pattern is a D-60 problem.
	Where bool
}

// SubSpec is one sub-resource word. Word is fixed and must be written
// literally in a pattern (no glob), so a misspelt sub is a load error rather
// than a rule that silently matches nothing. Param says whether one more
// segment may follow; ParamValues, when non-nil, closes that segment's
// vocabulary too (registry fields), otherwise it is free and may glob in a
// pattern. A pattern or resource may always stop at Word: `entity:x/attr`
// covers every attribute, `entity:x/registry` every registry field.
type SubSpec struct {
	Word        string
	Param       bool
	ParamValues []string
}

// KindSpecs returns the grammar of every kind, in Kinds() order.
//
// system is the one irregular kind: its ids are the closed set
// {logs, wireguard, reload, check_config, cache}, and only `reload` has a sub,
// a single free segment naming the domain (`system:reload/automation`), with
// no fixed word before it. SubSpec{Word: "", Param: true} encodes that.
func KindSpecs() []KindSpec {
	return []KindSpec{
		{Kind: KindEntity, IDSegments: 1, Qualified: true, Where: true, Subs: []SubSpec{
			{Word: "state"},
			{Word: "attr", Param: true},
			{Word: "registry", Param: true, ParamValues: []string{"name", "area_id", "labels", "icon", "disabled_by", "entity_id"}},
			{Word: "history"},
			{Word: "stats"},
		}},
		{Kind: KindDevice, IDSegments: 1, Where: true, Subs: []SubSpec{
			{Word: "registry", Param: true, ParamValues: []string{"area_id", "labels", "name_by_user"}},
		}},
		{Kind: KindArea, IDSegments: 1},
		{Kind: KindFloor, IDSegments: 1},
		{Kind: KindLabel, IDSegments: 1},
		{Kind: KindAutomation, IDSegments: 1, Subs: []SubSpec{{Word: "config"}, {Word: "trace"}}},
		{Kind: KindScript, IDSegments: 1, Subs: []SubSpec{{Word: "config"}, {Word: "trace"}, {Word: "run"}}},
		{Kind: KindTemplate, IDSegments: 1, Subs: []SubSpec{{Word: "config"}}},
		{Kind: KindHelper, IDSegments: 2, Subs: []SubSpec{{Word: "config"}}},
		{Kind: KindDashboard, IDSegments: 1, EmptyID: true, Subs: []SubSpec{
			{Word: "views", Param: true},
			{Word: "resources"},
		}},
		{Kind: KindConfigEntry, IDSegments: 1, Subs: []SubSpec{{Word: "options"}, {Word: "flow"}}},
		{Kind: KindFile, IDSegments: 0, Subs: []SubSpec{{Word: "block", Param: true}}},
		{Kind: KindService, IDSegments: 1, Qualified: true},
		{Kind: KindSystem, IDSegments: 1,
			IDs:  []string{"logs", "wireguard", "reload", "check_config", "cache"},
			Subs: []SubSpec{{Word: "", Param: true}}},
	}
}

// Resource is one concrete thing a verb applies to: `<kind>:<id>[/<sub>…]`.
//
// ID is Home Assistant's own identifier (H-9, H-17): an entity_id, a device_id,
// an automation's config id (D-1), a helper's `<domain>/<id>`, a
// companion-relative file path, a dashboard url_path ("" = default). Phase 1
// does not resolve aliases to it — `policy check` takes the canonical id; the
// command layer resolves aliases before checking from phase 2 on.
//
// Sub is the `/`-joined sub-resource path below the id ("" = the resource
// itself), e.g. "attr/latitude", "registry/labels", "block/climate_schedule".
type Resource struct {
	Kind Kind
	ID   string
	Sub  string
}

// String renders the address in the grammar ParseResource reads:
// `<kind>:<id>` plus `/<sub>` when Sub is set. ParseResource(r.String()) == r
// for every r ParseResource returns. A Resource built by hand is not
// necessarily one of those: a file path whose second-to-last segment is
// `block` (`packages/block/x.yaml`) renders to an address ParseResource reads
// as the block `x.yaml` of `packages`, because a trailing `/block/<id>` always
// is one; a path with `block` anywhere else renders to one it refuses as
// ambiguous with the block sub-resource (D-67).
func (r Resource) String() string {
	s := string(r.Kind) + ":" + r.ID
	if r.Sub != "" {
		s += "/" + r.Sub
	}
	return s
}

// ParseResource parses a concrete resource address — the first argument of
// `policy check`. It refuses, with an error naming the problem:
//
//   - a missing `:` or an unknown kind;
//   - any wildcard (`*`, `?`) — a resource names one thing; patterns belong in
//     rules;
//   - an empty id (except `dashboard:`), an empty segment (`a//b`), or an id
//     with the wrong number of segments for its kind (helper needs
//     `<domain>/<id>`);
//   - a system id outside the kind's closed set;
//   - a sub-resource word the kind does not have, a closed param value it does
//     not list (`registry/lables`), or more sub segments than the grammar
//     allows;
//   - a character outside the id's charset (D-68): entity and service ids are
//     ASCII letters, digits, `_` and `.`; helper, area, floor and label ids
//     ASCII letters, digits and `_`; device and config_entry ids hold no
//     white space; no id segment and no free sub-resource segment of any kind
//     starts or ends with white space or holds a control character or a
//     format character (Unicode Cf: zero-width space, BOM, bidi controls…);
//   - a file path with a `.` or `..` segment, or a segment named `block` (in
//     any case) anywhere but before a trailing block id (D-67).
//
// For file, a trailing `/block/<id>` after at least one path segment is
// always read as the block sub-resource; everything before it is the path.
func ParseResource(s string) (Resource, error) {
	if s == "*" || strings.ContainsAny(s, "*?") {
		return Resource{}, fmt.Errorf("resource %q holds a wildcard: a resource names one thing (patterns belong in rules)", s)
	}
	a, err := parseAddress(s, true)
	if err != nil {
		return Resource{}, fmt.Errorf("resource %q: %w", s, err)
	}
	return Resource{Kind: a.kind, ID: strings.Join(a.id, "/"), Sub: strings.Join(a.sub, "/")}, nil
}
