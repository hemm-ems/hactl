# Surfaces

A **surface** is the complete set of places a rule has to reach, derived
mechanically — from the cobra tree, from the source, from `INVARIANTS.md` — and
never typed out. A **manifest** in this directory binds every site on a surface
to a disposition. `make test-surface` fails when a site has none.

## Why this exists

Every other gate in this repository answers *"does the thing I fixed stay
fixed?"* Nothing answered *"did I fix every place this applies?"*

Four defects reported against v2026.7.12 were each the unfixed half of a fix
shipped in the same release. In every case the fix's scope was an enumeration —
built by hand, or by grepping the symptom — and the sites it missed left no
trace anywhere. A forgotten site was indistinguishable from a site that did not
exist.

- `auto apply` was not among the thirteen write commands that learned to resolve
  their target, because the scope was "commands printing `dry-run: would …`" and
  `auto apply` prints `dry-run: no changes written`. The E2E table that would
  have caught it lists `script apply` and four deletes — five hand-written rows,
  and the sixth was the bug.
- `trace show` still rendered UTC after the timezone fix, because
  `analyze.shortTimestamp` is a fourth timestamp renderer that never parses a
  time at all, and a unit test pinned its UTC-in/UTC-out behaviour as correct.
- `auto diff` and `auto apply` still refused identifiers `auto ls` prints, while
  `INVARIANTS.md` H-17 asserted as background fact that they did not.
- `device ls --pattern` lost case-insensitivity in a *consistency* commit,
  harmonised toward the sibling with no stake in the answer, while the three
  filter flags beside it in the same function kept it.

## The property

Not "every site is proven" — that is a goal, not a gate. The property is that
**no site can be silent**. A site is proven, knowingly exempt, or recorded as
debt in a file a reviewer reads. A site nobody has considered fails the build
the day it appears.

Debt is legal. Invisible debt is not.

## Format

```
#ceiling 15
<site key> = proven: <TestName>
<site key> = exempt: <why the rule does not apply here, 25+ chars>
<site key> = debt:   <what is not proven yet, 25+ chars>
```

Four things are hard errors, never ratcheted:

| failure | meaning |
|---|---|
| unclassified | the site exists and the manifest is silent — this is the closure property |
| stale | the manifest names a site that no longer exists, so the ledger has stopped describing the code |
| phantom | a `proven:` names a test no tier defines — the mechanism that rots an "Enforced by:" list into decoration |
| thin reason | `exempt: n/a`, the idiom this exists to stop from forming |

`debt:` entries are printed, not failed, up to the `#ceiling`. Raising a ceiling
is a one-line, greppable, reviewable act with an author. Forgetting a site is
not an act at all. That asymmetry is the whole design.

## The surfaces

| manifest | derived from | rule |
|---|---|---|
| `confirm` | the live cobra tree | a preview fails exactly where `--confirm` would (H-2) |
| `target` | `internal/cmd` entrypoints | an unresolvable identifier ends the command rather than becoming a plan |
| `clock` | every clock layout in the source | a rendered hour is in the reader's zone, not Home Assistant's UTC |
| `invariant` | `INVARIANTS.md` headings | a universal law is enforced by a gate that quantifies over its set |

Two gates need no manifest, because their failures are never debt:

- `TestInvariantCitationsResolve` — every test named in an "Enforced by:" list
  exists. A citation that does not resolve is a claim of proof that is false
  right now.
- `TestFilterFlagsAgreeOnCase` — every filter flag answers the same question
  whatever case the caller typed: case-insensitive, the pole D-2
  (`docs/decisions.md`) decided. The gate asserted only *parity* while no pole
  was decided — the honest gate then, but one a command whose filters are all
  case-sensitive satisfies, which is where the commit that broke `device ls
  --pattern` was headed. With the pole decided, the gate demands it.

## Adding a surface

Write an extractor returning `surfaceaudit.Surface`, a gate that calls
`surfaceaudit.Check`, and a manifest. The gate must fail when the extractor
returns nothing: an extractor that has stopped matching passes forever while
proving nothing, which is the failure mode a closure gate is most exposed to.
