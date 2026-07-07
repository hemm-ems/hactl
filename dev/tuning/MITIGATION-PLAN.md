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
