//go:build p2a_stub

// Package reqmap maps one outgoing request hactl sends — a Home Assistant
// websocket message, a Home Assistant REST request, a companion request by its
// logical route — to the (resource, verb) pairs it touches, or says it is a
// phase-3 read, an exempt request, a request no row names, or a request that
// matches a row and still cannot be mapped (INVARIANTS.md H-29, request layer;
// dev/surfaces/policy.manifest; D-72..D-84).
//
// THIS FILE IS A CONTRACT STUB, built only with `-tags p2a_stub`. It exists so
// the stage-2a tests (`-tags p2a_contract`, written from the contract before
// any implementation) compile and can be watched to fail. The integrator
// deletes it once the real package exists, and drops the `p2a_contract` build
// line from the tests. No contract document names this API; it is the minimal
// one the tests need, chosen by the test author:
//
//   - [Classify] is pure: everything it may rely on beyond the request itself
//     arrives in [Facts] — what the same command invocation already read under
//     the same loaded policy (D-72), and the extract_from_target answer the
//     hook asked for (D-59). It sends nothing and reads nothing.
//   - [DashboardDiff] is the per-view diff of `lovelace/config/save`, as a
//     pure function over (stored config, new config).
//
// If the implementation's names differ, only the adapter in
// vectors_p2a_test.go has to change; the vector table
// (testdata/policy/requests/*.json) is the contract and stays as it is.
package reqmap

import (
	"encoding/json"

	"github.com/hemm-ems/hactl/internal/policy"
)

// Transport is the family a request belongs to; each has its own key grammar
// in dev/surfaces/policy.manifest.
type Transport string

// The three request families.
const (
	TransportWS        Transport = "ws"
	TransportREST      Transport = "rest"
	TransportCompanion Transport = "companion"
)

// Request is one request as it leaves the process.
type Request struct {
	Transport Transport
	// Message is a websocket message as sent, without its "id": "type" plus
	// the params at the top level, the way sendCommand merges them. WS only.
	Message map[string]any
	// Method is the HTTP method, upper case. REST and companion.
	Method string
	// Path is the path exactly as sent — percent-escapes intact, so the
	// whole-path rule (R-8) can see %2F and %5C — plus an optional "?query".
	// For the companion it is the LOGICAL route (/v1/…), never the Ingress
	// wire URL (D-74).
	Path string
	// Body is the request body as sent (JSON, YAML or text), nil when none.
	Body []byte
}

// Disposition is what a request maps to.
type Disposition string

// The five dispositions.
const (
	// Write: a row matched and the request maps to Checks (verbs control,
	// edit, create, delete).
	Write Disposition = "write"
	// ReadDeferred: a read row (map: read-deferred-phase3). Never refused by a
	// write check in phase 2 (D-54, D-74).
	ReadDeferred Disposition = "read-deferred"
	// Exempt: a transport, probe or oracle row; no policy resource.
	Exempt Disposition = "exempt"
	// Unmapped: no row names the request. Row is "". Refused for every WS type
	// and every HTTP method other than GET (E-9, D-74).
	Unmapped Disposition = "unmapped"
	// Unmappable: the whole-path rule failed (R-8), or a row matched but the
	// request cannot be mapped — an id the grammar refuses (D-67..D-69), a key
	// with no grammar field, a missing or non-string id, a fact this
	// invocation does not hold, a reach question without an answer. Refused.
	// Reason names why (the fail-closed reasons of the manual).
	Unmappable Disposition = "unmappable"
)

// Check is one (resource, verb) pair a request is checked as. KindWide marks
// a check on every id of a kind (D-75): Resource.ID is then "*" and the
// resource renders as `<kind>:*[/<sub>]`.
type Check struct {
	Resource policy.Resource
	Verb     policy.Verb
	KindWide bool
}

// Result is the mapping of one request.
type Result struct {
	// Row is the manifest key the request matched ("rest POST
	// /api/services/{domain}/{service}"), "" when none did.
	Row         string
	Disposition Disposition
	// Checks are the (resource, verb) pairs of a Write. Order is not part of
	// the contract.
	Checks []Check
	// Reason explains an Unmappable or Unmapped result.
	Reason string
}

// Facts is what the request layer may rely on beyond the request itself.
type Facts struct {
	// PolicyHash is the content hash of the policy loaded for this
	// invocation; a fact checked under another hash authorises nothing (D-72).
	PolicyHash string
	// LiveEntities are the entity ids with a live state.
	LiveEntities []string
	// Registry is what this invocation read from the three registries.
	Registry Registry
	// Reach is the extract_from_target answer for this request's target
	// (expand_group: true); nil when Home Assistant gave none.
	Reach *Reach
	// DashboardConfigs is each stored dashboard config this invocation read,
	// by canonical url_path ("" = the default dashboard).
	DashboardConfigs map[string]json.RawMessage
	// DashboardIDs maps a dashboard's storage id to its url_path, from the
	// lovelace/dashboards/list this invocation read.
	DashboardIDs map[string]string
	// VerifiedAutomationIDs are the config ids a GET /v1/config/automation in
	// this invocation verified.
	VerifiedAutomationIDs []string
	// OptionsFlowHandlers maps an options flow's flow_id to the handler (the
	// entry_id, D-83) of the start response this invocation holds.
	OptionsFlowHandlers map[string]string
	// RefReplacePlans are the ref/replace dry runs this invocation checked.
	RefReplacePlans []RefReplacePlan
}

// Registry is the slice of the entity, device and area registries the cascades
// (D-76) and the script run checks (D-84) are enumerated from.
type Registry struct {
	Entities []RegistryEntity
	Devices  []RegistryDevice
	Areas    []RegistryArea
}

// RegistryEntity is one entity registry row. Empty strings mean "none".
type RegistryEntity struct {
	EntityID      string
	UniqueID      string
	Platform      string
	DeviceID      string
	AreaID        string
	Labels        []string
	ConfigEntryID string
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
	AreaID  string
	FloorID string
	Labels  []string
}

// Reach is Home Assistant's answer to extract_from_target: the referenced
// entities, or the error it answered with.
type Reach struct {
	Entities []string
	Err      string
}

// RefReplacePlan is a checked ref/replace dry run: its {old, new}, the policy
// hash it was checked under, and every (resource, verb) it was checked as.
type RefReplacePlan struct {
	Old, New   string
	PolicyHash string
	Checks     []Check
}

// Classify maps one request. STUB: maps nothing.
func Classify(_ Request, _ Facts) Result {
	return Result{Disposition: Unmapped, Reason: "contract stub: no classifier is built yet"}
}

// DashboardDiff returns the checks a save of updated over stored needs for
// the dashboard at urlPath ("" = default); stored nil = no stored config this
// invocation read. An error when a view path does not parse as one grammar
// segment. STUB: diffs nothing.
func DashboardDiff(_ string, _, _ json.RawMessage) ([]Check, error) {
	return nil, nil
}
