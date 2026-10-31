// Package policy is hactl's permission policy: a guardrail against an agent's
// mistakes, never a security boundary (D-51, level A). The token in `.env` can
// do everything, and anything with a shell can reach Home Assistant directly or
// edit `policy.yaml`; the package exists so that a misread instruction, a
// too-wide target or a typo in an id is refused, not so that an agent working
// around the policy is stopped. Nothing in this package, its output or its
// documentation may call it security.
//
// # Scope of phase 1
//
// The model only: [Load] reads `<instance>/policy.yaml`, [Policy.Decide]
// answers one (resource, verb) question, [Lint] reports rules that cannot mean
// what their author wrote. The read-only commands `hactl policy show`,
// `hactl policy check` and `hactl policy lint` are built on it. Nothing else
// consults a Policy yet: no request-layer hook, no command refusal, no listing
// filter, no audit log, no `policy explain` (phases 2–4 of the workspace's
// SPEC-permissions.md). The built-in, unremovable deny on `file:hactl/**`
// (D-57, E-8) is phase 4 and is NOT part of [Policy.Decide] yet.
//
// # The contract
//
// docs/manual.md, section "Policy (permission guardrail)", is the contract this
// package is built and tested against: the file format, the resource grammar,
// the verbs, evaluation, matching, profile selection, the broken-file and
// no-file behaviour, and the output of the three commands. Decisions:
// D-51..D-58, D-60 (a broken file fails closed), D-61 (profile without
// `active:`), D-62 (only the file selects the profile; `--as` is a what-if).
//
// # The shared glob (D-2, D-28)
//
// Rules match with the same glob as `--pattern`: one matcher, never two. The
// engine is the leaf package `internal/glob` (moved there from
// internal/cmd/ent.go in phase 1), imported by both internal/cmd and this
// package, with two entry points over one engine:
//
//   - glob.Match(s, pattern) — case-insensitive (D-2), anchored at both ends,
//     `*` any run of characters, `?` exactly one character, every other byte
//     literal. A pattern without a wildcard is an exact (case-insensitive)
//     comparison.
//   - glob.MatchQualified(s, pattern) — Match at s, or at the part of s after
//     its first dot (D-28's second anchor, for qualified `<domain>.<name>`
//     identifiers).
//
// internal/cmd's `matchPattern` keeps its filter-only convenience on top — a
// pattern without a wildcard is a substring test — and calls MatchQualified for
// the glob case. The policy evaluator never uses the substring form: an allow
// on `entity:light.kitchen` must not grant `light.kitchen_2`, and the
// permissive direction D-28 accepts for a filter is the dangerous direction for
// an allow. The policy uses MatchQualified for the id of the two kinds whose ids
// are qualified (`entity`, `service`) and Match for every other id and every
// sub-resource segment, segment by segment (see the manual, "Matching").
//
// Undecided, and recorded here rather than assumed: MatchQualified applies to
// a wildcard-free entity or service pattern too, so a domain-less literal
// (`entity:front_door`) matches that name in every domain (`lock.front_door`,
// `cover.front_door`). Whether such a literal should match only the full id,
// or be a broken file, is an open decision; the behaviour is pinned by
// TestUnqualifiedLiteralMatchesTheNameInEveryDomainPendingDecision so the
// decision visibly changes it.
package policy
