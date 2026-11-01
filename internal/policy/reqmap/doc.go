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
//   - [ReadDeferred]: a read. Phase 2 enforces no read (D-54); the row exists
//     so the closure gate can tell a read from a forgotten write.
//   - [Exempt]: no policy resource (transport, probe), by an explicit row.
//   - [Unmapped]: no row names the request.
//   - [Unmappable]: a row names it, but the request cannot be mapped — a path
//     breaking the whole-path rule (R-8), an id the policy grammar refuses
//     (D-67, D-68, D-69), a body that does not pin its identity (D-78), or a
//     fact the mapping needs that this invocation never established (D-72).
//
// [Result.Refused] says whether the request must be refused whatever the
// policy says (fail closed, E-9): an unmapped or unmappable request that can
// change anything. A websocket type and every HTTP method other than GET is
// mutating unless its row says otherwise; an unmapped GET is allowed in phase 2
// and refused from phase 3 (D-74). [Result.Rule] then names the fail-closed
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
// TestPolicySurfaceIsClosed holds the two in lockstep with the requests the
// source sends. A row added here without a manifest line, or the reverse, is a
// red gate.
//
// Phase 2a builds the classifier only. Nothing calls it on a live request yet:
// the hooks in sendCommand, the shared RoundTripper and companion.Client, the
// audit log and the command layer's plan checks are stage 2b.
package reqmap
