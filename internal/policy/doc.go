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
// `active:`), D-62 (only the file selects the profile; `--as` is a what-if),
// D-63 (the full id only), D-64 (a pinned MCP server keeps its instance),
// D-65 (lint below the id), D-66 (the partial verdict), D-67 (file ids),
// D-68 (the id charset).
//
// # The shared glob (D-2, D-28)
//
// Rules match with the same glob as `--pattern`: one matcher, never two. The
// engine is the leaf package `internal/glob` (moved there from
// internal/cmd/ent.go in phase 1), imported by both internal/cmd and this
// package, with two entry points over one engine:
//
//   - glob.Match(s, pattern) — case-insensitive (D-2), anchored at both ends,
//     `*` any run of characters, `?` exactly one character (one code point,
//     never one byte), every other character literal. A pattern without a
//     wildcard is an exact (case-insensitive) comparison.
//   - glob.MatchQualified(s, pattern) — Match at s, or at the part of s after
//     its first dot (D-28's second anchor, for qualified `<domain>.<name>`
//     identifiers). For filters only.
//
// internal/cmd's `matchPattern` keeps its filter-only conveniences on top — a
// pattern without a wildcard is a substring test, and the glob case calls
// MatchQualified. The policy evaluator uses neither: it calls glob.Match for
// every id and every sub-resource segment, segment by segment (see the
// manual, "Matching"). A filter may err towards more rows; an allow may not
// err towards more rights. So `entity:kitchen*` matches no entity (every
// entity id starts with its domain), the cross-domain form is
// `entity:*.front_door`, and an entity or service pattern id with neither a
// `.` nor a wildcard (`entity:front_door`) can never match and is a broken
// file (D-63).
package policy
