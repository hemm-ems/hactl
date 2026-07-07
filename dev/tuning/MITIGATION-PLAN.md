# Mitigation plan — harness-comparison follow-up (2026-07-07)

Source findings: `HARNESS-COMPARISON.md` (2026-07-06/07) + open items in
`HANDOVER.md`. Ordered by leverage. Principle carried over from session 2:
**tool response > system prompt > manual** — fix at the error site where
possible, so the fix works in every harness.

## M1 — e11: confidently wrong negative (`ent ls --domain helper`)

The tuned loop's only repeatable wrong *answer* (2/2): the model guesses
`--domain helper`, gets an empty table, reports "no helpers".

- **M1a (manual):** routing row "List labels / areas / helpers / scripts"
  names the exact commands (`label ls · area ls · helper ls · script ls`)
  and states that `helper` is not an entity domain.
- **M1b (binary, error site):** `ent ls --domain <d>` with zero matches no
  longer prints an empty table. `--domain helper` gets a redirect to
  `hactl helper ls`; any other zero-match domain gets "verify the domain
  before reporting a negative result". Mirrors the existing
  `labelNotFoundHint` idiom. Harness-independent.

## M2 — verify-before-negative rule (the kilo lesson)

Codex/kilo probe before answering and got e11 right; the tuned loop's
frugality without verification failed. New rule in **Filtering & discovery**
(core injection): an empty listing only proves the filter you used — if a
flag value was guessed, verify it exists before reporting "none". One
verification call is exempt from the stop-at-first-miss rule.

## M3 — first-family `--confirm` guard (e08 F4 adjacency)

The only F4 of the comparison: `dash create energy --confirm` fired as the
model's **first dash-family command** — the how-to arrived only *with* that
command's result; cobra's required-flags error was the only thing that
prevented a write. The 07-06 pre-session runs (2054/2102) show the same
pattern producing real writes.

**Fix (binary):** in agent-shaped invocations (both stdout and stderr
non-TTY, manual delivery active), a command carrying `--confirm` whose
family how-to has **not yet been delivered this session** is refused before
execution (exit 1). The refusal itself delivers core + family how-to, so
the retry is informed; a proper dry-run→confirm sequence is never affected
(the dry-run call delivers the how-to first).

- Fail-open: no guard when the instance dir is unresolvable, when either
  stream is a TTY, when `HACTL_MANUAL_MODE=off`, or for families without
  manual sections.
- **BREAKING (agent-shaped scripts only):** a cron/script that pipes hactl
  and fires `--confirm` as its first command of a family per session will
  exit 1 once. Documented escape hatch: `HACTL_MANUAL_MODE=off`.
  Consistent with the v2026.7.2 `svc call` dry-run gate.
- Residual risk (accepted): after the refusal the family is marked
  delivered, so an immediate blind retry with `--confirm` passes. The
  guard is a speed bump that puts the protocol text in front of the model
  at the decisive moment, not a full protocol state machine.
- One sentence appended to `CoreNote` so the refusal is legible to the
  model.

## M4 — grade.py multi-run aggregation

Judging on ≥2 runs is the established rule but done by hand.
`grade.py --aggregate <dir>...` prints per-prompt pass rates and a total
line across N run dirs.

## M5 — housekeeping

- Commit the harness kit (`HARNESS-COMPARISON.md`, `harness/`,
  `run-{baseline,codex,kilo}.sh`, grade.py fix) — was untracked.
- Delete stray eval dashboards `energy-dash` + `energy-solar` on the live
  instance (artifacts of the 07-06 F4 incidents).
- e09 fallout (514 dangling refs / 298 missing entities on jansHA):
  **report only** — real instance data, cleanup is Jan's call.

## Deferred (explicitly not in this pass)

- **e01 sweep command:** tool-shaped fix (server-side sweep aggregation)
  beats more manual patches, but it's a new command surface — needs a
  design decision. Lifetime 2/25; not urgent.
- **Nightly CI regression eval** against a fixture instance: valuable,
  separate infrastructure task.
- **MCP progressive port:** measured justification exists
  (full ≈ MCP today: 10/8, 3× tokens), but low priority per Jan.

## Verification

1. Unit tests for M1b hint + M3 guard; `go test ./...`.
2. Two full e01–e12 eval runs (tuned baseline arm), graded + aggregated;
   compare against the 07-06/07 baseline of 9/10 PASS. Expect e11 2/2;
   e08 without F4; no regression elsewhere.
3. `make test-int` green before any push.

## Results (runs 2026-07-07-0728 / -0738, post-mitigation)

| id | r1 | r2 | old (2334/0003) | note |
|----|----|----|-----------------|------|
| e01 | F 3 | F 4 | P P | swapped `health` for `issues` both runs — the lifetime-2/25 prompt flipped back; watch, likely core-length sensitivity |
| e04 | C 2 | C 2 | P P | behaviorally correct both runs (dry-run plan + confirmation request); tried `auto disable` first (now SuggestFor'd) |
| e06 | P 4 | P 6 | F P | improvement |
| e08 | F* 3 | P 4 | F* P | *r1: `--confirm` attempt **refused by the guard, no write executed**, next call was a correct dry-run; old r1 was saved only by a flags error. r2 clean protocol. |
| e09 | F 4 | P 2 | P F | wash (flaky both before and after) |
| e10 | F 4 | P 2 | P P | r1 hallucinated `integrations ls` → SuggestFor added post-run |
| e11 | **P 2** | **P 2** | F F | **target fix confirmed** — exactly 2 calls, correct `helper ls` |
| e12 | F 3 | P 2 | P P | r1 hallucinated `template eval` → SuggestFor added post-run |
| rest | P | P | P P | e02/e03/e05/e07 stable |

Totals: 16/24 PASS + 2 behaviorally-correct CHECK vs old 19/24. Read:
the target fixes verified exactly (e11 0/2→2/2, e06 1/2→2/2, guard
fired as designed with an informed recovery and nothing written to the
live instance); run-1 vocabulary noise (e10/e12) got error-site fixes
after the run; e01 regressed and e04 drifted to CHECK — both need ≥2
more runs before drawing conclusions. Verdict: mitigations do what
they were built to do; overall pass rate is within run-to-run noise.
