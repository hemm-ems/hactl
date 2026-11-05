package enforce

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/reqgate"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// Read enforcement (W-2, D-138..D-147). A read is decided twice: before it is
// sent, on what the request names itself (one thing read whole — refused when
// denied), and on its answer, before any command decodes it (H-21): a listing's
// rows the profile may not read are dropped and counted, the parts it may not
// read are masked and counted, and a single read whose address only the
// answer carries is refused. A command therefore never holds a denied value,
// and no output path — prose, --json, a count, an error — can carry one.

// Denied is the value a masked part reads (D-140): the key stays.
const Denied = "<denied>"

var (
	deniedJSON     = json.RawMessage(`"` + Denied + `"`)
	deniedListJSON = json.RawMessage(`["` + Denied + `"]`)
)

// RuleTemplateNeedsAllow decides `read system:template` when the profile
// denies some read and only its default allows it (D-143, M-3): a template can
// read any state, so it needs an allow of its own.
const RuleTemplateNeedsAllow = "system:template needs an explicit allow while the profile denies a read"

// ConfigFileFetcher is a Fetcher that can also read a config file's raw
// content (through the companion). The read filter asks it which typed owner
// a wide read's hit lies in (D-142); without it, a hit whose owner matters is
// hidden (D-141).
type ConfigFileFetcher interface {
	ConfigFile(ctx context.Context, location string) ([]byte, error)
}

// readCounts are an invocation's hidden and masked counts (D-139).
type readCounts struct {
	hidden, masked         int
	hiddenKeys, maskedKeys map[string]bool
	// refused counts the reads refused at the request layer; a command that
	// carried on past one (a best-effort read) did not show it, so it is
	// hidden when the command succeeds.
	refused int
}

// ReadCounts returns what this invocation's reads hid and masked so far:
// rows dropped, parts masked, and reads refused at the request layer.
func (s *Session) ReadCounts() (hidden, masked, refused int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads.hidden, s.reads.masked, s.reads.refused
}

// countKeys adds what one answer hid and masked to the invocation's counts:
// each address once, however many reads hid or masked it (D-139); a row
// without an address on its own.
func (s *Session) countKeys(hidden []string, anon int, masked []string) {
	if len(hidden) == 0 && anon == 0 && len(masked) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reads.hiddenKeys == nil {
		s.reads.hiddenKeys, s.reads.maskedKeys = map[string]bool{}, map[string]bool{}
	}
	for _, k := range hidden {
		if !s.reads.hiddenKeys[k] {
			s.reads.hiddenKeys[k] = true
			s.reads.hidden++
		}
	}
	s.reads.hidden += anon
	for _, k := range masked {
		if !s.reads.maskedKeys[k] {
			s.reads.maskedKeys[k] = true
			s.reads.masked++
		}
	}
}

// readVerdict decides `read <kind>:<id>[/<sub>]`; ok is false when the address
// is one the grammar refuses — a row hactl cannot address (D-141).
func (s *Session) readVerdict(kind policy.Kind, id, sub string) (policy.Verdict, bool) {
	addr := string(kind) + ":" + id
	if sub != "" {
		addr += "/" + sub
	}
	s.vmu.Lock()
	if v, ok := s.verdicts[addr]; ok {
		s.vmu.Unlock()
		return v.v, v.ok
	}
	s.vmu.Unlock()
	var out cachedVerdict
	if r, err := policy.ParseResource(addr); err == nil {
		out = cachedVerdict{v: s.decideRead(reqmap.Check{Verb: policy.Read, Resource: r}), ok: true}
	}
	s.vmu.Lock()
	s.verdicts[addr] = out
	s.vmu.Unlock()
	return out.v, out.ok
}

type cachedVerdict struct {
	v  policy.Verdict
	ok bool
}

// readable reports whether `read <kind>:<id>[/<sub>]` is allowed.
func (s *Session) readable(kind policy.Kind, id, sub string) bool {
	v, ok := s.readVerdict(kind, id, sub)
	return ok && v.Allowed
}

// decideRead decides one read check. `read system:template` is the one with a
// rule of its own (D-143).
func (s *Session) decideRead(c reqmap.Check) policy.Verdict {
	v := s.decide(c)
	if c.KindWide {
		return v
	}
	return TemplateRead(s.pol, c.Resource, v)
}

// TemplateRead applies D-143 to a read verdict: `read system:template` — what
// `tpl eval` reads — needs an explicit allow while the profile denies some
// read, because a template can read any state (M-3); the profile default
// alone does not grant it then. Every other verdict is returned as it is.
// `policy check` and the request layer answer it alike.
func TemplateRead(pol *policy.Policy, r policy.Resource, v policy.Verdict) policy.Verdict {
	if r.Kind != policy.KindSystem || r.ID != "template" || r.Sub != "" || !DeniesAnyRead(pol) {
		return v
	}
	if v.Allowed && v.Rule == policy.RuleDefaultAllow {
		v.Allowed, v.Rule, v.Line = false, RuleTemplateNeedsAllow, 0
	}
	return v
}

// DeniesAnyRead reports whether the profile in effect denies some read: a deny
// for read anywhere, or a kind its allows and default leave unreadable.
func DeniesAnyRead(pol *policy.Policy) bool {
	if pol == nil || !pol.InEffect() {
		return false
	}
	for _, k := range policy.Kinds() {
		if v := pol.DecideKindWide(k, "", policy.Read); !v.Allowed || v.Partial {
			return true
		}
	}
	return false
}

// checkRead decides what a read names itself, before it is sent (D-138).
// Reads are not audited (D-146).
func (s *Session) checkRead(req reqmap.Request, res reqmap.Result) error {
	ref := s.newDenial(LayerRequest, s.dryRun, describe(req))
	ref.Read = true
	ref.Checked = len(res.Checks)
	for _, c := range res.Checks {
		v := s.decideRead(c)
		switch {
		case !v.Allowed:
			ref.Denied = append(ref.Denied, denial(c, v))
		case res.Whole && v.Partial:
			// One opaque body cannot be masked: a denied part refuses it.
			d := denial(c, v)
			if len(v.PartialDenies) > 0 {
				d.Rule, d.Line = v.PartialDenies[0].Rule, v.PartialDenies[0].Line
			}
			d.Partial = true
			ref.Denied = append(ref.Denied, d)
		}
	}
	if len(ref.Denied) == 0 {
		return nil
	}
	return s.refuseRead(ref)
}

// refuseRead records a refused read. It is not the invocation's refusal (a
// write's is, H-29): a command that reported it fails with it, and one that
// carried on past it showed less, which the trailer counts.
func (s *Session) refuseRead(ref *DeniedError) error {
	// Reported like a denied write (D-155): `would read <what>`, every
	// denied thing counted as checked.
	names := make([]string, len(ref.Denied))
	for i, d := range ref.Denied {
		names[i] = d.Resource
	}
	ref.Action = "read " + strings.Join(names, ", ")
	ref.Checked = max(ref.Checked, len(ref.Denied))
	s.mu.Lock()
	defer s.mu.Unlock()
	ref.Written = append([]string{}, s.written...)
	s.reads.refused++
	return ref
}

// Filter implements reqgate.Gate: the answer to a read, filtered (D-138).
func (s *Session) Filter(ctx context.Context, req reqmap.Request, resp []byte) ([]byte, error) {
	if isOwn(ctx) || s.writeCommand {
		return resp, nil // the session's own question, or a write's own read (D-82)
	}
	// A read's mapping needs no fact (the row and the request decide it), so
	// the answer is classified without asking anything: a write's answer is
	// never filtered, and its mapping is not computed a second time.
	res := reqmap.Classify(req, nil)
	if res.Disposition != reqmap.Read || res.Filter.Kind == reqmap.FilterNone {
		return resp, nil
	}
	f := &answerFilter{s: s, ctx: ctx, spec: res.Filter}
	out, err := f.apply(resp)
	if err != nil {
		var ref *DeniedError
		if errorsAs(err, &ref) {
			return nil, s.refuseRead(ref)
		}
		// An answer the filter cannot read is not shown unchecked: it is
		// refused, fail closed (D-141).
		ref = s.newDenial(LayerRequest, s.dryRun, describe(req))
		ref.Read = true
		ref.Denied = []Denial{{Resource: describe(req), Verb: policy.Read.String(), Rule: "fail-closed: the answer cannot be filtered: " + err.Error()}}
		return nil, s.refuseRead(ref)
	}
	s.countKeys(f.hiddenKeys, f.anon, f.maskedKeys)
	return out, nil
}

// answerFilter filters one answer, counting what it drops and masks.
type answerFilter struct {
	s      *Session
	ctx    context.Context
	spec   reqmap.ReadFilter
	hidden int
	masked int
	// dirty is set when a row was dropped or rewritten, whether or not it
	// counts (a wide read's per-file hit counts).
	dirty bool
	files map[string][]byte

	// What the trailer counts (D-139), as addresses: a thing two reads of
	// one invocation hide (an entity in the states and in the registry) or
	// mask (an attribute in every history row) counts once. A row hactl
	// cannot address counts on its own (anon).
	hiddenKeys, maskedKeys []string
	anon                   int
	keyed, quiet           bool
}

// keep is a row's verdict `read <kind>:<id>[/<sub>]`; a row it drops is
// counted by that address.
func (f *answerFilter) keep(kind policy.Kind, id, sub string) bool {
	if f.s.readable(kind, id, sub) {
		return true
	}
	f.hide(string(kind) + ":" + id + subSuffix(sub))
	return false
}

// hide records a dropped row by its address.
func (f *answerFilter) hide(key string) {
	f.keyed = true
	f.hiddenKeys = append(f.hiddenKeys, key)
}

// mask masks o[field] with v and counts the part by its address.
func (f *answerFilter) mask(o map[string]json.RawMessage, field string, v json.RawMessage, key string) {
	o[field] = v
	f.masked++
	f.maskedKeys = append(f.maskedKeys, key)
}

func (f *answerFilter) apply(resp []byte) ([]byte, error) {
	switch f.spec.Kind {
	case reqmap.FilterStates:
		return f.list(resp, "", f.stateRow)
	case reqmap.FilterState:
		return f.object(resp, func(o map[string]json.RawMessage) (bool, error) {
			f.maskState(f.spec.Entity, o)
			return true, nil
		})
	case reqmap.FilterEntityRegistry:
		return f.list(resp, "", f.entityRegistryRow)
	case reqmap.FilterDeviceRegistry:
		return f.list(resp, "", f.deviceRegistryRow)
	case reqmap.FilterAreas:
		return f.list(resp, "", f.byKeyRow("area_id", policy.KindArea))
	case reqmap.FilterLabels:
		return f.list(resp, "", f.byKeyRow("label_id", policy.KindLabel))
	case reqmap.FilterFloors:
		return f.list(resp, "", f.byKeyRow("floor_id", policy.KindFloor))
	case reqmap.FilterDashboards:
		return f.list(resp, "", f.dashboardRow)
	case reqmap.FilterDashboardConfig:
		return f.object(resp, f.dashboardConfig)
	case reqmap.FilterTraces:
		return f.list(resp, "", f.traceRow)
	case reqmap.FilterStatisticIDs:
		return f.list(resp, "", f.statisticRow)
	case reqmap.FilterStatistics:
		return f.statistics(resp)
	case reqmap.FilterEnergyPrefs:
		return f.object(resp, f.energyPrefs)
	case reqmap.FilterConfigEntries:
		return f.list(resp, "", f.byKeyRow("entry_id", policy.KindConfigEntry))
	case reqmap.FilterHistory:
		return f.history(resp)
	case reqmap.FilterLogbook:
		return f.list(resp, "", f.logbookRow)
	case reqmap.FilterServices:
		return f.list(resp, "", f.serviceDomain)
	case reqmap.FilterCoreConfig:
		return f.object(resp, f.coreConfig)
	case reqmap.FilterFiles:
		return f.object(resp, func(o map[string]json.RawMessage) (bool, error) {
			return true, f.listField(o, "files", func(raw json.RawMessage) (json.RawMessage, bool, error) {
				var p string
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, false, fmt.Errorf("a file is not a string: %w", err)
				}
				return raw, f.keep(policy.KindFile, p, ""), nil
			})
		})
	case reqmap.FilterConfigBlock:
		return f.object(resp, f.configBlock)
	case reqmap.FilterRelated:
		return f.object(resp, f.related)
	case reqmap.FilterRefHits:
		return f.object(resp, f.refHits)
	case reqmap.FilterTemplates:
		return f.object(resp, func(o map[string]json.RawMessage) (bool, error) {
			return true, f.listField(o, "templates", f.byKeyRow("unique_id", policy.KindTemplate))
		})
	case reqmap.FilterHelpers:
		return f.object(resp, func(o map[string]json.RawMessage) (bool, error) {
			return true, f.listField(o, "helpers", f.helperRow)
		})
	case reqmap.FilterHelper:
		return f.object(resp, f.helper)
	}
	return nil, fmt.Errorf("no filter for answers of kind %q", f.spec.Kind)
}

// --- generic shapes -----------------------------------------------------------

// rowFunc decides one row: the row to keep (possibly masked), and whether it is
// kept at all.
type rowFunc func(raw json.RawMessage) (json.RawMessage, bool, error)

// list filters a JSON array of rows. An answer whose filter changed nothing is
// returned byte for byte.
func (f *answerFilter) list(resp []byte, _ string, fn rowFunc) ([]byte, error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(resp, &rows); err != nil {
		return nil, fmt.Errorf("the answer is not a list: %w", err)
	}
	out, changed, err := f.rows(rows, fn)
	if err != nil {
		return nil, err
	}
	if !changed {
		return resp, nil
	}
	return marshalRows(out)
}

// rows applies fn to every row; changed says whether any row was dropped or
// rewritten.
func (f *answerFilter) rows(rows []json.RawMessage, fn rowFunc) ([]json.RawMessage, bool, error) {
	out := make([]json.RawMessage, 0, len(rows))
	changed := false
	for _, r := range rows {
		f.keyed = false
		kept, keep, err := fn(r)
		if err != nil {
			return nil, false, err
		}
		if !keep {
			f.hidden++
			if !f.keyed && !f.quiet {
				f.anon++
			}
			changed = true
			continue
		}
		if !bytes.Equal(kept, r) {
			changed = true
		}
		out = append(out, kept)
	}
	if changed {
		f.dirty = true
	}
	return out, changed, nil
}

// listField filters the array under key of an object in place.
func (f *answerFilter) listField(o map[string]json.RawMessage, key string, fn rowFunc) error {
	raw, ok := o[key]
	if !ok || isNull(raw) {
		return nil
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return fmt.Errorf("%s is not a list: %w", key, err)
	}
	out, changed, err := f.rows(rows, fn)
	if err != nil || !changed {
		return err
	}
	b, err := marshalRows(out)
	if err != nil {
		return err
	}
	o[key] = b
	return nil
}

// object filters a JSON object through fn, which edits it in place. An object
// fn left as it was is returned byte for byte.
func (f *answerFilter) object(resp []byte, fn func(map[string]json.RawMessage) (bool, error)) ([]byte, error) {
	var o map[string]json.RawMessage
	if err := json.Unmarshal(resp, &o); err != nil {
		return nil, fmt.Errorf("the answer is not an object: %w", err)
	}
	before := f.hidden + f.masked
	if _, err := fn(o); err != nil {
		return nil, err
	}
	if f.hidden+f.masked == before && !f.dirty {
		return resp, nil
	}
	return marshal(o)
}

func marshalRows(rows []json.RawMessage) ([]byte, error) {
	if len(rows) == 0 {
		return []byte("[]"), nil
	}
	return marshal(rows)
}

func isNull(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null"
}

// stringField reads a string field of an object row; ok is false when it is
// absent, null, empty or not a string.
func stringField(o map[string]json.RawMessage, key string) (string, bool) {
	raw, have := o[key]
	if !have {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || s == "" {
		return "", false
	}
	return s, true
}

func decodeRow(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var o map[string]json.RawMessage
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("a row is not an object: %w", err)
	}
	return o, nil
}

// byKeyRow keeps a row when `read <kind>:<row[key]>` is allowed; a row
// without the key cannot be addressed and is hidden (D-141).
func (f *answerFilter) byKeyRow(key string, kind policy.Kind) rowFunc {
	return func(raw json.RawMessage) (json.RawMessage, bool, error) {
		o, err := decodeRow(raw)
		if err != nil {
			return nil, false, err
		}
		id, ok := stringField(o, key)
		if !ok {
			return raw, false, nil
		}
		return raw, f.keep(kind, id, ""), nil
	}
}

// --- entities -----------------------------------------------------------------

// maskState masks a state object's `state` and attributes by `read
// entity:<e>/state` and `…/attr/<name>` (D-140): the key stays, the value
// reads <denied>. Only a partial verdict on the entity can mask anything.
func (f *answerFilter) maskState(e string, o map[string]json.RawMessage) {
	v, ok := f.s.readVerdict(policy.KindEntity, e, "")
	if !ok || !v.Partial {
		return
	}
	if _, has := o["state"]; has && !f.s.readable(policy.KindEntity, e, "state") {
		f.mask(o, "state", deniedJSON, "entity:"+e+"/state")
	}
	raw, has := o["attributes"]
	if !has || isNull(raw) {
		return
	}
	av, aok := f.s.readVerdict(policy.KindEntity, e, "attr")
	if aok && av.Allowed && !av.Partial {
		return
	}
	var attrs map[string]json.RawMessage
	if json.Unmarshal(raw, &attrs) != nil {
		// Attributes that are not an object cannot be masked by name.
		f.mask(o, "attributes", deniedJSON, "entity:"+e+"/attr")
		return
	}
	n := 0
	for name := range attrs {
		if !f.s.readable(policy.KindEntity, e, "attr/"+name) {
			attrs[name] = deniedJSON
			f.maskedKeys = append(f.maskedKeys, "entity:"+e+"/attr/"+name)
			n++
		}
	}
	if n == 0 {
		return
	}
	b, err := marshal(attrs)
	if err != nil {
		f.mask(o, "attributes", deniedJSON, "entity:"+e+"/attr")
		return
	}
	o["attributes"] = b
	f.masked += n
}

// stateRow is one row of GET /api/states.
func (f *answerFilter) stateRow(raw json.RawMessage) (json.RawMessage, bool, error) {
	o, err := decodeRow(raw)
	if err != nil {
		return nil, false, err
	}
	e, ok := stringField(o, "entity_id")
	if !ok || !f.keep(policy.KindEntity, e, "") {
		return raw, false, nil
	}
	before := f.masked
	f.maskState(e, o)
	if f.masked == before {
		return raw, true, nil
	}
	b, err := marshal(o)
	return b, true, err
}

// registryFields are the registry fields the grammar addresses (closed set,
// internal/policy KindSpecs) that a read can mask; `entity_id` is the row's own
// address and is decided with the row.
var (
	entityRegistryFields = []string{"name", "area_id", "labels", "icon", "disabled_by"}
	deviceRegistryFields = []string{"area_id", "labels", "name_by_user"}
)

// maskRegistry masks the registry fields of a row whose verdict is partial.
func (f *answerFilter) maskRegistry(kind policy.Kind, id string, o map[string]json.RawMessage, fields []string) {
	v, ok := f.s.readVerdict(kind, id, "")
	if !ok || !v.Partial {
		return
	}
	for _, field := range fields {
		raw, has := o[field]
		if !has || f.s.readable(kind, id, "registry/"+field) {
			continue
		}
		key := string(kind) + ":" + id + "/registry/" + field
		if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '[' {
			// A list keeps its shape: one element reading <denied>, so a
			// decoder of the field still reads it (D-140).
			f.mask(o, field, deniedListJSON, key)
		} else {
			f.mask(o, field, deniedJSON, key)
		}
	}
}

func (f *answerFilter) entityRegistryRow(raw json.RawMessage) (json.RawMessage, bool, error) {
	o, err := decodeRow(raw)
	if err != nil {
		return nil, false, err
	}
	e, ok := stringField(o, "entity_id")
	if !ok || !f.keep(policy.KindEntity, e, "") || !f.keep(policy.KindEntity, e, "registry/entity_id") {
		return raw, false, nil
	}
	before := f.masked
	f.maskRegistry(policy.KindEntity, e, o, entityRegistryFields)
	if f.masked == before {
		return raw, true, nil
	}
	b, err := marshal(o)
	return b, true, err
}

func (f *answerFilter) deviceRegistryRow(raw json.RawMessage) (json.RawMessage, bool, error) {
	o, err := decodeRow(raw)
	if err != nil {
		return nil, false, err
	}
	d, ok := stringField(o, "id")
	if !ok || !f.keep(policy.KindDevice, d, "") {
		return raw, false, nil
	}
	before := f.masked
	f.maskRegistry(policy.KindDevice, d, o, deviceRegistryFields)
	if f.masked == before {
		return raw, true, nil
	}
	b, err := marshal(o)
	return b, true, err
}

// history filters GET /api/history/period: a list per entity, the entity
// named by the first row that carries it (a minimal response drops it from the
// rest).
func (f *answerFilter) history(resp []byte) ([]byte, error) {
	var lists [][]json.RawMessage
	if err := json.Unmarshal(resp, &lists); err != nil {
		return nil, fmt.Errorf("the answer is not a list of lists: %w", err)
	}
	out := make([][]json.RawMessage, 0, len(lists))
	changed := false
	for _, l := range lists {
		e := ""
		for _, r := range l {
			o, err := decodeRow(r)
			if err != nil {
				return nil, err
			}
			if id, ok := stringField(o, "entity_id"); ok {
				e = id
				break
			}
		}
		if e == "" || !f.s.readable(policy.KindEntity, e, "history") {
			// The rows are the points: each hidden point counts.
			n := max(len(l), 1)
			f.hidden += n
			f.anon += n
			changed = true
			continue
		}
		kept := make([]json.RawMessage, 0, len(l))
		for _, r := range l {
			o, err := decodeRow(r)
			if err != nil {
				return nil, err
			}
			before := f.masked
			f.maskState(e, o)
			if f.masked == before {
				kept = append(kept, r)
				continue
			}
			b, err := marshal(o)
			if err != nil {
				return nil, err
			}
			kept = append(kept, b)
			changed = true
		}
		out = append(out, kept)
	}
	if !changed {
		return resp, nil
	}
	if len(out) == 0 {
		return []byte("[]"), nil
	}
	return marshal(out)
}

// logbookRow is one logbook row, by `read entity:<e>` (D-142); its `state`
// is masked like a state's.
func (f *answerFilter) logbookRow(raw json.RawMessage) (json.RawMessage, bool, error) {
	o, err := decodeRow(raw)
	if err != nil {
		return nil, false, err
	}
	// A logbook row is an event, not a thing: each hidden row counts
	// (D-139 as refined), so it is not recorded by its entity's address.
	e, ok := stringField(o, "entity_id")
	if !ok || !f.s.readable(policy.KindEntity, e, "") {
		return raw, false, nil
	}
	before := f.masked
	f.maskState(e, o)
	if f.masked == before {
		return raw, true, nil
	}
	b, err := marshal(o)
	return b, true, err
}

// coreConfigZoneHome maps the keys of GET /api/config that are zone.home's
// to the zone.home attribute they are read as: the home's location, and its
// name — Home Assistant names zone.home after location_name, so the name is
// zone.home's friendly_name (found by the W-6 read sweep: `health` printed it
// under a profile that reads nothing).
var coreConfigZoneHome = []struct{ key, attr string }{
	{"latitude", "latitude"}, {"longitude", "longitude"}, {"elevation", "elevation"},
	{"radius", "radius"}, {"location_name", "friendly_name"},
}

// coreConfig masks the home's location and name in GET /api/config:
// they are zone.home's, so they are read as zone.home's attributes.
func (f *answerFilter) coreConfig(o map[string]json.RawMessage) (bool, error) {
	for _, z := range coreConfigZoneHome {
		if _, has := o[z.key]; has && !f.s.readable(policy.KindEntity, "zone.home", "attr/"+z.attr) {
			f.mask(o, z.key, deniedJSON, "entity:zone.home/attr/"+z.attr)
		}
	}
	return true, nil
}

// --- statistics and energy ----------------------------------------------------

// statisticReadable is `read entity:<id>/stats`; an external statistic
// (`domain:name`) has no address (D-141).
func (f *answerFilter) statisticReadable(id string) bool {
	if id == "" {
		return false
	}
	if strings.Contains(id, ":") {
		f.hide("statistic:" + id)
		return false
	}
	return f.keep(policy.KindEntity, id, "stats")
}

// statisticAllowed is statisticReadable for rows that are occurrences (a
// statistic's points, an energy source naming it): it records no address, so
// each hidden row counts on its own.
func (f *answerFilter) statisticAllowed(id string) bool {
	return id != "" && !strings.Contains(id, ":") && f.s.readable(policy.KindEntity, id, "stats")
}

func (f *answerFilter) statisticRow(raw json.RawMessage) (json.RawMessage, bool, error) {
	o, err := decodeRow(raw)
	if err != nil {
		return nil, false, err
	}
	id, _ := stringField(o, "statistic_id")
	return raw, f.statisticReadable(id), nil
}

// statistics filters recorder/statistics_during_period, a map keyed by
// statistic id.
func (f *answerFilter) statistics(resp []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(resp, &m); err != nil {
		return nil, fmt.Errorf("the answer is not an object: %w", err)
	}
	changed := false
	for id, points := range m {
		if !f.statisticAllowed(id) {
			// Each hidden point counts: the answer's rows are the points.
			var list []json.RawMessage
			n := 1
			if json.Unmarshal(points, &list) == nil && len(list) > 0 {
				n = len(list)
			}
			delete(m, id)
			f.hidden += n
			f.anon += n
			changed = true
		}
	}
	if !changed {
		return resp, nil
	}
	return marshal(m)
}

// energyStats are the statistic and entity ids an energy source, flow or
// tracked device names: every string under a `stat_…` or `entity_…` key.
func energyStats(o map[string]json.RawMessage) (stats, entities []string) {
	for k, raw := range o {
		var s string
		if json.Unmarshal(raw, &s) != nil || s == "" {
			continue
		}
		switch {
		case strings.HasPrefix(k, "stat_") || k == "included_in_stat":
			stats = append(stats, s)
		case strings.HasPrefix(k, "entity_"):
			entities = append(entities, s)
		}
	}
	return stats, entities
}

// energyItem keeps an energy source, flow or device when every statistic it
// names is readable (D-142); its legacy flow lists are filtered item by item.
func (f *answerFilter) energyItem(raw json.RawMessage) (json.RawMessage, bool, error) {
	o, err := decodeRow(raw)
	if err != nil {
		return nil, false, err
	}
	// Each source, flow and device is a row of its own: a hidden one counts
	// however many others name the same statistic.
	stats, entities := energyStats(o)
	for _, s := range stats {
		if !f.statisticAllowed(s) {
			return raw, false, nil
		}
	}
	for _, e := range entities {
		if !f.s.readable(policy.KindEntity, e, "") {
			return raw, false, nil
		}
	}
	before := f.hidden
	for _, key := range []string{"flow_from", "flow_to"} {
		if ferr := f.listField(o, key, f.energyItem); ferr != nil {
			return nil, false, ferr
		}
	}
	if f.hidden == before {
		return raw, true, nil
	}
	b, err := marshal(o)
	return b, true, err
}

func (f *answerFilter) energyPrefs(o map[string]json.RawMessage) (bool, error) {
	for _, key := range []string{"energy_sources", "device_consumption", "device_consumption_water"} {
		if err := f.listField(o, key, f.energyItem); err != nil {
			return false, err
		}
	}
	return true, nil
}

// --- registry kinds, dashboards, traces, services ---------------------------------

func (f *answerFilter) dashboardRow(raw json.RawMessage) (json.RawMessage, bool, error) {
	o, err := decodeRow(raw)
	if err != nil {
		return nil, false, err
	}
	u, ok := stringField(o, "url_path")
	if !ok {
		return raw, false, nil
	}
	if u == "lovelace" {
		u = ""
	}
	return raw, f.keep(policy.KindDashboard, u, ""), nil
}

// dashboardConfig drops the views of a dashboard config that a deny covers
// (D-140), each counted. A view without a path cannot be addressed: while any
// view is denied it is hidden (D-141).
func (f *answerFilter) dashboardConfig(o map[string]json.RawMessage) (bool, error) {
	u := f.spec.URLPath
	v, ok := f.s.readVerdict(policy.KindDashboard, u, "views")
	if ok && v.Allowed && !v.Partial {
		return true, nil
	}
	return true, f.listField(o, "views", func(raw json.RawMessage) (json.RawMessage, bool, error) {
		view, err := decodeRow(raw)
		if err != nil {
			return nil, false, err
		}
		p, has := stringField(view, "path")
		if !has {
			return raw, false, nil
		}
		return raw, f.keep(policy.KindDashboard, u, "views/"+p), nil
	})
}

func (f *answerFilter) traceRow(raw json.RawMessage) (json.RawMessage, bool, error) {
	o, err := decodeRow(raw)
	if err != nil {
		return nil, false, err
	}
	domain, _ := stringField(o, "domain")
	item, ok := stringField(o, "item_id")
	if !ok || (domain != "automation" && domain != "script") {
		return raw, false, nil
	}
	// A trace is a run, not a thing: each hidden one counts.
	return raw, f.s.readable(policy.Kind(domain), item, "trace"), nil
}

// serviceDomain filters one domain of GET /api/services, service by service;
// a domain left without one is dropped too. Each hidden service counts.
func (f *answerFilter) serviceDomain(raw json.RawMessage) (json.RawMessage, bool, error) {
	o, err := decodeRow(raw)
	if err != nil {
		return nil, false, err
	}
	domain, ok := stringField(o, "domain")
	if !ok {
		return raw, false, nil
	}
	var services map[string]json.RawMessage
	if svcRaw, has := o["services"]; has && json.Unmarshal(svcRaw, &services) != nil {
		return nil, false, fmt.Errorf("services of %s is not an object", domain)
	}
	dropped := 0
	for name := range services {
		if !f.keep(policy.KindService, domain+"."+name, "") {
			delete(services, name)
			dropped++
		}
	}
	if dropped == 0 {
		return raw, true, nil
	}
	if len(services) == 0 {
		return raw, false, nil
	}
	f.hidden += dropped
	b, err := marshal(services)
	if err != nil {
		return nil, false, err
	}
	o["services"] = b
	out, err := marshal(o)
	return out, true, err
}

// --- companion ------------------------------------------------------------------

// configBlock refuses a block whose canonical id — in the answer only — is
// denied.
func (f *answerFilter) configBlock(o map[string]json.RawMessage) (bool, error) {
	id, ok := stringField(o, "id")
	path := f.spec.Path
	if p, has := stringField(o, "path"); has {
		path = p
	}
	if ok {
		if v, vok := f.s.readVerdict(policy.KindFile, path, "block/"+id); vok {
			if v.Allowed {
				return true, nil
			}
			return false, f.answerDenied(policy.KindFile, path, "block/"+id, v)
		}
	}
	// No canonical id, or one the grammar refuses: the block is decided as
	// the whole file.
	v, vok := f.s.readVerdict(policy.KindFile, path, "")
	if vok && v.Allowed && !v.Partial {
		return true, nil
	}
	return false, f.unaddressable("file:"+path+"/block/"+id, "the block's id is not an address the policy grammar reads")
}

// helper refuses a helper whose address — in the answer only — is denied.
func (f *answerFilter) helper(o map[string]json.RawMessage) (bool, error) {
	domain, dok := stringField(o, "domain")
	id, iok := stringField(o, "id")
	if !dok || !iok {
		return false, f.unaddressable("helper", "the answer names no domain and id")
	}
	v, ok := f.s.readVerdict(policy.KindHelper, domain+"/"+id, "config")
	if !ok {
		return false, f.unaddressable("helper:"+domain+"/"+id, "not an address the policy grammar reads")
	}
	if !v.Allowed {
		return false, f.answerDenied(policy.KindHelper, domain+"/"+id, "config", v)
	}
	return true, nil
}

func (f *answerFilter) helperRow(raw json.RawMessage) (json.RawMessage, bool, error) {
	o, err := decodeRow(raw)
	if err != nil {
		return nil, false, err
	}
	domain, dok := stringField(o, "domain")
	id, iok := stringField(o, "id")
	if !dok || !iok {
		return raw, false, nil
	}
	return raw, f.keep(policy.KindHelper, domain+"/"+id, ""), nil
}

// related filters what the companion relates to an entity: each related
// entity by `read entity:<e>`, each stale reference as a wide-read hit.
func (f *answerFilter) related(o map[string]json.RawMessage) (bool, error) {
	if err := f.listField(o, "related", f.byKeyRow("entity_id", policy.KindEntity)); err != nil {
		return false, err
	}
	return true, f.listField(o, "stale_refs", f.hitRow)
}

// refHits filters a wide read's hits (D-142) and what it says about them: the
// files it skipped and, for grep, each file's hit counts and their total.
func (f *answerFilter) refHits(o map[string]json.RawMessage) (bool, error) {
	key := f.spec.Rows
	if key == "" {
		key = "hits"
	}
	// Hits dropped for their owner still count in their file's grep source.
	dropped := map[string][2]int{}
	err := f.listField(o, key, func(raw json.RawMessage) (json.RawMessage, bool, error) {
		kept, keep, err := f.hitRow(raw)
		if err == nil && !keep {
			if h, derr := decodeRow(raw); derr == nil {
				loc, _ := stringField(h, "location")
				kind, _ := stringField(h, "kind")
				d := dropped[loc]
				if kind == "key" {
					d[0]++
				} else {
					d[1]++
				}
				dropped[loc] = d
			}
		}
		return kept, keep, err
	})
	if err != nil {
		return false, err
	}
	if err := f.listField(o, "skipped", f.byKeyRow("location", policy.KindFile)); err != nil {
		return false, err
	}
	if _, has := o["sources"]; !has {
		return true, nil
	}
	total := 0
	f.quiet = true
	if err := f.listField(o, "sources", func(raw json.RawMessage) (json.RawMessage, bool, error) {
		src, err := decodeRow(raw)
		if err != nil {
			return nil, false, err
		}
		loc, ok := stringField(src, "location")
		if !ok || !f.s.readable(policy.KindFile, loc, "") {
			f.keyed = true // counted where its hits were listed
			return raw, false, nil
		}
		var counts struct {
			Key   int `json:"key_hits"`
			Value int `json:"value_hits"`
		}
		_ = json.Unmarshal(raw, &counts)
		d, had := dropped[loc]
		if had {
			f.dirty = true
			counts.Key = max(counts.Key-d[0], 0)
			counts.Value = max(counts.Value-d[1], 0)
			kb, _ := marshal(counts.Key)
			vb, _ := marshal(counts.Value)
			src["key_hits"], src["value_hits"] = kb, vb
		}
		total += counts.Key + counts.Value
		if !had {
			return raw, true, nil
		}
		b, err := marshal(src)
		return b, true, err
	}); err != nil {
		return false, err
	}
	// A dropped source is no row the caller would have seen: its hits are
	// already counted where they were listed.
	f.quiet = false
	if _, has := o["total"]; has {
		b, _ := marshal(total)
		if !bytes.Equal(bytes.TrimSpace(o["total"]), b) {
			o["total"] = b
			f.dirty = true
		}
	}
	return true, nil
}

// hitRow keeps a hit when its file is readable and so is every typed owner it
// lies in (D-142). The owner is read from the file only when the profile can
// deny one: a hit whose file cannot be read then is hidden (D-141).
func (f *answerFilter) hitRow(raw json.RawMessage) (json.RawMessage, bool, error) {
	h, err := decodeRow(raw)
	if err != nil {
		return nil, false, err
	}
	loc, ok := stringField(h, "location")
	if !ok {
		return raw, false, nil
	}
	path, _ := stringField(h, "path")
	// A hit is an occurrence: each hidden one counts.
	hidden := func() (json.RawMessage, bool, error) {
		return raw, false, nil
	}
	if !f.s.readable(policy.KindFile, loc, "") {
		return hidden()
	}
	if !f.s.ownersMatter() {
		return raw, true, nil
	}
	val, _ := stringField(h, "matched_value")
	content, ok := f.file(loc)
	if !ok {
		return hidden()
	}
	owners, err := reqmap.RefChangeOwners(loc, content, path, val, val)
	if err != nil {
		return hidden() // a hit hactl cannot place is hidden, never shown unchecked (D-141)
	}
	for _, o := range owners {
		if !f.s.readable(o.Kind, o.ID, "config") {
			return hidden()
		}
	}
	return raw, true, nil
}

// file reads a config file's content through the fetcher, once per answer.
func (f *answerFilter) file(loc string) ([]byte, bool) {
	if c, have := f.files[loc]; have {
		return c, c != nil
	}
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	cf, ok := f.s.fetcher.(ConfigFileFetcher)
	if !ok || f.s.fetcher == nil {
		f.files[loc] = nil
		return nil, false
	}
	c, err := cf.ConfigFile(own(f.ctx), loc)
	if err != nil || c == nil {
		f.files[loc] = nil
		return nil, false
	}
	f.files[loc] = c
	return c, true
}

// ownersMatter reports whether some typed owner's config may be unreadable:
// only then does a hit's owner change its verdict.
func (s *Session) ownersMatter() bool {
	for _, k := range []policy.Kind{policy.KindAutomation, policy.KindScript, policy.KindTemplate} {
		if v := s.pol.DecideKindWide(k, "config", policy.Read); !v.Allowed || v.Partial {
			return true
		}
	}
	return false
}

// answerDenied is the refusal of a single read whose address only the answer
// carries.
func (f *answerFilter) answerDenied(kind policy.Kind, id, sub string, v policy.Verdict) error {
	r, _ := policy.ParseResource(string(kind) + ":" + id + subSuffix(sub))
	ref := f.s.newDenial(LayerRequest, f.s.dryRun, "read "+r.String())
	ref.Read = true
	ref.Checked = 1
	ref.Denied = []Denial{denial(reqmap.Check{Verb: policy.Read, Resource: r}, v)}
	return ref
}

// unaddressable refuses a single read hactl cannot address (D-141).
func (f *answerFilter) unaddressable(what, why string) error {
	ref := f.s.newDenial(LayerRequest, f.s.dryRun, "read "+what)
	ref.Read = true
	ref.Checked = 1
	ref.Denied = []Denial{{Resource: what, Verb: policy.Read.String(), Rule: "fail-closed: unaddressable read: " + why}}
	return ref
}

func subSuffix(sub string) string {
	if sub == "" {
		return ""
	}
	return "/" + sub
}

func isOwn(ctx context.Context) bool { return reqgate.IsOwn(ctx) }

func own(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return reqgate.Own(ctx)
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }

// marshal encodes a filtered answer without HTML escaping: `<denied>` and
// every string Home Assistant sent read as they were, not as `\u003c…`.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
