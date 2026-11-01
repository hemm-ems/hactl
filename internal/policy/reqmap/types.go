package reqmap

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/hemm-ems/hactl/internal/policy"
)

// Transport is the family a request belongs to. It decides which rows can name
// it: a companion request is matched by its logical route only, never as an HA
// REST path, even though under Ingress both ride the same HTTP transport
// (D-74).
type Transport string

// The three request families hactl sends.
const (
	// WS is a Home Assistant websocket message.
	WS Transport = "ws"
	// REST is a Home Assistant HTTP API request (haapi.Client).
	REST Transport = "rest"
	// Companion is a companion add-on request (companion.Client), by logical
	// route.
	Companion Transport = "companion"
)

// Request is one outgoing request, as it will be sent.
type Request struct {
	// Transport selects the row family.
	Transport Transport

	// Type is the websocket message type (WS only).
	Type string
	// Params is the websocket message without `type` and `id` (WS only).
	Params map[string]any

	// Method is the HTTP method (REST and Companion), in any case.
	Method string
	// Path is the request path. For REST it is the escaped path as it goes on
	// the wire (url.URL.EscapedPath); for Companion it is the logical route
	// (`/v1/config/automation`), never the transport prefix an Ingress URL
	// carries. A query string after `?` is allowed and is not part of the key.
	Path string
	// Query holds query parameters in addition to any in Path.
	Query url.Values
	// Body is the request body (REST JSON; companion JSON or YAML text).
	Body []byte
}

// Disposition is what the classifier concluded about a request.
type Disposition int

const (
	// Write means the request changes something; Checks is its plan.
	Write Disposition = iota + 1
	// ReadDeferred means the request is a read, enforced from phase 3.
	ReadDeferred
	// Exempt means the request touches no policy resource, by an explicit row.
	Exempt
	// Unmapped means no row names the request.
	Unmapped
	// Unmappable means a row names it but the mapping cannot be computed.
	Unmappable
)

func (d Disposition) String() string {
	switch d {
	case Write:
		return "write"
	case ReadDeferred:
		return "read-deferred-phase3"
	case Exempt:
		return "exempt"
	case Unmapped:
		return "unmapped"
	case Unmappable:
		return "unmappable"
	}
	return "disposition(?)"
}

// Check is one (resource, verb) a request needs.
type Check struct {
	Verb     policy.Verb
	Resource policy.Resource
	// KindWide marks a check on every id of Resource.Kind (D-75): Resource.ID
	// is empty and Resource.Sub, when set, is the sub-resource below each id.
	// It renders as `<kind>:*[/<sub>]` and is never a concrete resource.
	KindWide bool
	// DenyOnly marks a check only a deny decides (D-107): the typed address
	// (automation, script, helper, template entity) of an entity a service
	// call reaches. The call's grant is decided on the entity; the typed
	// address can only refuse it — the profile default and the allows do not
	// decide it. It renders like any check.
	DenyOnly bool
}

// Target renders the checked resource: the address, or `<kind>:*[/<sub>]` for
// a kind-wide check.
func (c Check) Target() string {
	if !c.KindWide {
		return c.Resource.String()
	}
	s := string(c.Resource.Kind) + ":*"
	if c.Resource.Sub != "" {
		s += "/" + c.Resource.Sub
	}
	return s
}

// String renders `<verb> <target>`, the form tests and reports use.
func (c Check) String() string { return c.Verb.String() + " " + c.Target() }

// Result is the classifier's answer for one request.
type Result struct {
	Disposition Disposition
	// Row is the manifest key of the row that named the request; "" when
	// Unmapped.
	Row string
	// Checks is the plan of a Write, or the reads of a ReadDeferred row as far
	// as the request names them (informational until phase 3). Sorted by verb,
	// then target; no duplicates.
	Checks []Check
	// Mutating says whether the request can change anything. It is the row's
	// own answer when a row named the request, and otherwise true for every
	// websocket type and every HTTP method other than GET (E-9).
	Mutating bool
	// Rule names the fail-closed reason of an Unmapped or Unmappable result,
	// `fail-closed: …`, the way a refusal names a policy rule.
	Rule string
	// Detail is the human-readable why behind Rule or behind an exemption.
	Detail string
	// PlanFromResponse marks a companion ref/replace dry run: the request
	// writes nothing, and its plan is the response, mapped by
	// [RefReplaceChecks] before any confirmed write relies on it (D-77).
	PlanFromResponse bool
}

// Refused reports whether the request must be refused whatever the policy says
// (while a profile is in effect): it is unmapped or unmappable, and it can
// change something. A Write, a read and an exempt request are decided by the
// policy, or not at all.
func (r Result) Refused() bool {
	return (r.Disposition == Unmapped || r.Disposition == Unmappable) && r.Mutating
}

// Strings renders Checks as `<verb> <target>` lines, in order; a deny-only
// check (D-107) as `<verb> <target> (deny only)`.
func (r Result) Strings() []string {
	out := make([]string, len(r.Checks))
	for i, c := range r.Checks {
		out[i] = c.String()
		if c.DenyOnly {
			out[i] += DenyOnlySuffix
		}
	}
	return out
}

// DenyOnlySuffix marks a deny-only check in [Result.Strings].
const DenyOnlySuffix = " (deny only)"

// Facts is what the same command invocation already read, under the same
// loaded policy (D-72), that a mapping may rely on. Every field is optional;
// a nil field means "not read in this invocation", never "empty".
type Facts struct {
	// PolicyHash is the content hash of the policy loaded for this
	// invocation. A fact that carries its own hash (a checked ref/replace
	// plan) authorises nothing under another hash (D-72, R-7).
	PolicyHash string
	// Live is the live entity set (GET /api/states). It classifies a service
	// as one domain's entity service, expands `entity_id: all`, and decides
	// which strings of a service call's data name an entity.
	Live *LiveStates
	// Reach is Home Assistant's `extract_from_target` answer for this very
	// request (D-59). Asked with [ReachQuery].
	Reach *Reach
	// Registries are the entity, device and area registries, for a delete's
	// cascade (D-76) and a script entity's config id (D-84).
	Registries *Registries
	// Dashboards maps a canonical url_path ("" = default) to the stored
	// config read for it. A url_path that is absent was not read.
	Dashboards map[string]StoredDashboard
	// DashboardIDs maps a dashboard's storage id to its url_path, from
	// lovelace/dashboards/list. Nil = not read.
	DashboardIDs map[string]string
	// VerifiedAutomationIDs holds the config ids a GET /v1/config/automation
	// verified in this invocation.
	VerifiedAutomationIDs map[string]bool
	// OptionsFlowHandlers maps an options flow id to its handler — the config
	// entry's entry_id (D-83) — from a start response this invocation holds.
	OptionsFlowHandlers map[string]string
	// RefReplacePlans are the checked ref/replace dry runs of this
	// invocation.
	RefReplacePlans []RefReplacePlan
}

// LiveStates is the live entity set, with what each live state says about the
// entities a call on it is forwarded to and about its typed address.
type LiveStates struct {
	EntityIDs []string
	// Members maps a live entity to the entity ids its state's `entity_id`
	// attribute lists — the members a group, a light (switch, cover, fan,
	// media_player, lock) group or a scene forwards a call on it to (D-106).
	// Absent = the state lists none.
	Members map[string][]string
	// AutomationIDs maps a live automation entity to its config id, the
	// state's `id` attribute (D-107). Absent = the automation has none.
	AutomationIDs map[string]string
}

// Reach is an `extract_from_target` answer: the entity ids the target
// references, or the error Home Assistant answered with (or the transport
// failure). An error is `fail-closed: reach unknown` (D-73).
type Reach struct {
	EntityIDs []string
	Err       error
}

// Registries are the registry rows a cascade or a script lookup needs.
type Registries struct {
	Entities []RegistryEntity
	Devices  []RegistryDevice
	Areas    []RegistryArea
}

// RegistryEntity is one entity registry row.
type RegistryEntity struct {
	EntityID string
	// Platform is the integration that provides the entity (`template`,
	// `input_boolean`, `script`, …): it tells a helper or a template entity
	// apart from other entities of its domain (D-107).
	Platform      string
	UniqueID      string
	ConfigEntryID string
	AreaID        string
	Labels        []string
}

// RegistryDevice is one device registry row.
type RegistryDevice struct {
	ID            string
	AreaID        string
	Labels        []string
	ConfigEntries []string
}

// RegistryArea is one area registry row.
type RegistryArea struct {
	ID      string
	FloorID string
	Labels  []string
}

// StoredDashboard is a dashboard config as read in this invocation. A nil
// Config means Home Assistant holds no stored config for the dashboard.
type StoredDashboard struct {
	Config json.RawMessage
}

// RefReplacePlan is one checked ref/replace dry run: the {old, new} it was
// asked for and the changes it answered with.
type RefReplacePlan struct {
	Old, New string
	// PolicyHash is the content hash of the policy the plan was checked
	// under; a plan checked under another hash than [Facts.PolicyHash]
	// authorises nothing (D-72, R-7).
	PolicyHash string
	Changes    []PlanChange
}

// PlanChange is one change of a ref/replace plan.
type PlanChange struct {
	// Location is the companion-relative file the change lies in.
	Location string
	// Path is the change's path inside that file (`[3].actions[0].entity_id`).
	Path string
	// Owner is the typed entry the change lies in, when the caller resolved
	// it from the file (the automation entry, script key or template entity
	// containing Path). Nil = the path names no typed owner.
	Owner *TypedOwner
}

// TypedOwner is the typed config entry a file change lies in (D-77, R-3).
type TypedOwner struct {
	// Kind is automation, script or template.
	Kind policy.Kind
	// ID is the owner's address before the change: the automation's config
	// id, the script's key, the template entity's unique_id.
	ID string
	// NewID is the owner's address after the change, when the change rewrites
	// that id (`id:` or `unique_id:`); "" or ID when it does not. A rewritten
	// id is a rename (E-5): edit the old address and create the new.
	NewID string
}

// checkSet accumulates checks and renders them sorted and de-duplicated.
type checkSet struct {
	m map[string]Check
}

func newCheckSet() *checkSet { return &checkSet{m: map[string]Check{}} }

// add keeps one check per (verb, target); of a deny-only and a full check of
// the same pair, the full check — the stricter one — stays.
func (s *checkSet) add(c Check) {
	if have, ok := s.m[c.String()]; ok && !have.DenyOnly && c.DenyOnly {
		return
	}
	s.m[c.String()] = c
}

func (s *checkSet) addAll(cs []Check) {
	for _, c := range cs {
		s.add(c)
	}
}

func (s *checkSet) list() []Check {
	out := make([]Check, 0, len(s.m))
	for _, c := range s.m {
		out = append(out, c)
	}
	sortChecks(out)
	return out
}

func sortChecks(cs []Check) {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Verb != cs[j].Verb {
			return cs[i].Verb < cs[j].Verb
		}
		return cs[i].Target() < cs[j].Target()
	})
}

// resource builds and validates an address with the policy grammar: an id the
// grammar refuses makes the request unmappable (D-67, D-68, D-69), never
// canonicalised.
func resource(kind policy.Kind, id, sub string) (policy.Resource, error) {
	s := string(kind) + ":" + id
	if sub != "" {
		s += "/" + sub
	}
	return policy.ParseResource(s)
}

// check builds a concrete check, validating the address.
func check(v policy.Verb, kind policy.Kind, id, sub string) (Check, error) {
	r, err := resource(kind, id, sub)
	if err != nil {
		return Check{}, err
	}
	return Check{Verb: v, Resource: r}, nil
}

// kindWide builds a check on every id of a kind.
func kindWide(v policy.Verb, kind policy.Kind, sub string) Check {
	return Check{Verb: v, Resource: policy.Resource{Kind: kind, Sub: sub}, KindWide: true}
}

// canonicalDashboard maps HA's spellings of the default dashboard to its
// address: an absent url_path, "" and "lovelace" are all `dashboard:` (D-66 as
// refined).
func canonicalDashboard(urlPath string) string {
	if urlPath == "lovelace" {
		return ""
	}
	return urlPath
}

// upper normalises an HTTP method.
func upper(m string) string { return strings.ToUpper(strings.TrimSpace(m)) }
