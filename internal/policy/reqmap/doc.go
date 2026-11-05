// Package reqmap is the request layer's classifier (INVARIANTS.md H-29, DRAFT;
// dev/surfaces/policy.manifest): a pure function from one outgoing request to
// the (resource, verb) pairs it touches.
//
// Every request hactl sends — a Home Assistant websocket message, an HA REST
// call, a companion call by its logical route — is mapped by [Classify] to one
// of five dispositions:
//
//   - [Write]: the request changes something; [Result.Checks] is every
//     (resource, verb) the policy must allow before it is sent. A kind-wide
//     check ([Check.KindWide]) stands for every id of a kind (D-75): it is
//     rendered `<kind>:*` and a refusal names it so.
//   - [Read]: a read (W-2, D-138). [Result.Checks] is what the request names
//     itself, refused before it is sent when denied; [Result.Filter] is how
//     the session filters its answer before any command decodes it (H-21).
//   - [Exempt]: no policy resource (transport, probe), by an explicit row.
//   - [Unmapped]: no row names the request.
//   - [Unmappable]: a row names it, but the request cannot be mapped — a path
//     breaking the whole-path rule (R-8), an id the policy grammar refuses
//     (D-67, D-68, D-69), a body that does not pin its identity (D-78), or a
//     fact the mapping needs that this invocation never established (D-72).
//
// [Result.Refused] says whether the request must be refused whatever the
// policy says (fail closed, E-9): an unmapped or unmappable request that can
// change anything — and, from W-2 on, an unmapped or unmappable read too
// (D-145). A websocket type and every HTTP method other than GET is mutating
// unless its row says otherwise. [Result.Rule] then names the fail-closed
// reason, `fail-closed: …`, the way a policy rule is named.
//
// # Pure
//
// The classifier sends nothing and reads nothing. What a mapping relies on
// beyond the request itself — the live entity set, an `extract_from_target`
// answer, the registries, a dashboard's stored config, a verified automation
// id, a checked `ref/replace` dry run, an options flow's handler — arrives in
// [Facts], which the caller fills from what the same command invocation read
// under the same loaded policy (D-72). A missing fact never widens a mapping
// into an allow: it either makes the request unmappable (where the contract
// says so) or widens the check to a superset — the whole dashboard, every id of
// the kind — which can over-deny and never under-deny (D-59's pole).
//
// # Rows
//
// [Rows] is the row table. Its keys are exactly the request keys of
// dev/surfaces/policy.manifest (`ws <type>`, `ws supervisor/api <METHOD>
// <endpoint>`, `rest <METHOD> <template>`, `companion <METHOD> <route>`), and
// TestPolicyManifestIsInLockstepWithTheClassifier holds the two in lockstep,
// and TestPolicySurfaceIsClosed holds the manifest to the requests the source
// sends. A row added here without a manifest line, or the reverse, is a red
// gate. TestEveryWriteRequestHasAMapping maps the request vectors
// (testdata/policy/requests), written from the contract before this package
// existed, one or more per row.
//
// The classifier is applied to every live request by internal/policy/enforce,
// through the hooks internal/policy/reqgate installs in the clients (stage 2b).
package reqmap
