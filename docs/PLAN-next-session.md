# Plan — write path + remaining read gaps

**Pick this up cold.** Everything you need is in the repo:
`docs/HANDOFF-2026-07-23-oracle-testing.md` (what happened, what's open),
`INVARIANTS.md` H-8..H-11 (the test pattern), `docs/testing.md` (the tiers).

**Definition of done for every phase below:** `make gates` green. That runs lint,
the unit tier and all three Docker tiers. Nothing merges on `make test` alone.

---

## How to run this with subagents

This plan was written after running the same shape of work with subagents. The
operational rules below are not style preferences — each one is here because
ignoring it cost time in the previous session.

**1. Strict, non-overlapping file ownership.** Give every agent an explicit list
of files it owns and forbid everything else. Three agents editing `internal/cmd/`
concurrently is fine *if* no two share a file; it is chaos otherwise. When an
agent believes a fix needs someone else's file, it must stop and report rather
than edit.

**2. One owner for shared files.** `INVARIANTS.md`, `docs/manual.md` and
`Makefile` must have exactly one owner per wave, or be updated by the
orchestrator after the wave lands. Concurrent appends to `INVARIANTS.md`
produced out-of-order sections last time.

**3. RED first, and paste the failure.** Write the test, run it, observe it fail,
put the failure output in the report and the PR. A test that has never failed is
not evidence. This is `AGENTS.md` policy and it caught a real mistake last time:
an assertion that looked right was wrong about `shortenStep`, and only the red
run revealed the code was correct and the expectation was not.

**4. Mutation-check every fix.** After green, deliberately break the fix and
confirm the test fails *for the stated reason*. Do this by copying the file
aside (`cp x.go /tmp/x.go.bak`), editing, testing, restoring.
**Never use `git checkout <file>` to restore** — it reverts to HEAD and silently
destroys uncommitted work. That happened twice last session.

**5. Expect tests that assert the bug.** Three did last time, one literally named
*"user_id wins over event_type/name (rule order)"*. When you invert an
assertion, say so explicitly in the report and the commit — the old expectation
being wrong is the finding.

**6. Do not trust the brief.** An agent last session was told labels inherit from
devices; it checked Home Assistant's own source, found they do not, implemented
only the area fallback and pinned the label behaviour with a test. That was the
single most valuable thing any agent did. Brief agents to verify claims against
HA rather than accept them.

**7. Never hand-write an expectation HA can compute.** Use the oracle harness
(`internal/integration/oracle_test.go`). This is H-9 and it is the whole point.

---

## Phase 0 — prerequisites (orchestrator, no agents)

- [ ] Merge `fix/read-surface-oracle-2026-07-23`.
- [ ] Point branch protection at the **`All Gates Green`** check. Until then the
      aggregator runs but does not block, and a skipped Docker tier still merges.
- [ ] `make hooks` on every dev machine.

---

## Phase 1 — documentation truth (1 agent, ~half a day)

The manual *is* the contract for LLM callers, so a wrong manual is a defect that
ships to every agent using the tool. Cheapest high-value work in this plan.

**Owns:** `docs/manual.md` only.

Fix R9–R15 from the handover:

| id | fix |
|---|---|
| R9 | Add prose for `ref scan\|replace\|validate` and `dash grep\|replace` — real, working commands with zero manual coverage |
| R10 | `dash grep` matches any JSON string value, not "an exact entity_id" — correct both the manual and `--help` |
| R11 | Delete or correct "ISO only with `--full`": `--full` never produces ISO in table mode, and `--json` produces ISO without it |
| R12 | `ent show`'s "(+N hidden)" is the total attribute count, not a hidden delta |
| R13 | Document `--color` as a no-op (deliberate: removing a documented flag breaks callers) |
| R14 | Document `auto cat` |
| R15 | Confirm `--top` is described as table-only and that this is now true |

**Verification:** every claim the agent writes must be executed against the live
test instance first. No prose describing behaviour nobody ran.

---

## Phase 2 — the write path (the priority)

Actual writes have never been verified black-box against HA except
`auto apply`/`rollback` (H-4). `docs/testing.md` already admits `script apply`
and `dash save` "can each be replaced with a stub without any test failing".

### Propose and land H-12 first

> **H-12 — A write is proven by reading it back from HA.**
> A write test reads current state from HA directly, writes via hactl with
> `--confirm`, reads back from HA directly, and compares the whole document —
> including at least one field the command never mentioned, as an independent
> witness that the entire document was written and not just the field the
> renderer happens to show. The restore is asserted too.

`internal/integration/write_roundtrip_test.go` already implements exactly this
for automations, including a `canonicalize()` helper that folds HA's legacy
singular schema keys (`trigger`→`triggers`, `service`→`action`) because HA
rewrites an automation's schema on write. **Reuse that helper; do not re-derive it.**

### Wave 2a — no companion needed (2 agents, parallel)

Both land in `internal/integration/` (build tag `integration`), so they ride the
existing `make test-int` and need no CI change.

**Agent W-A — entity registry writes.**
Owns: `internal/cmd/ent.go`, `internal/cmd/label.go`,
`internal/integration/write_entity_test.go` (new).
Covers `ent set-label`, `ent set-area` with `--confirm`: label *merge* semantics
(the manual says "preview merged labels" — prove it merges rather than
replaces), area assignment, and removal. Also fixes R-open: `ent set-label` and
`ent set-area` disagree on the same unregistered entity — one errors, one does
not; make them consistent and say which behaviour you chose.

**Agent W-B — dashboard writes.**
Owns: `internal/cmd/dash.go`, `internal/integration/write_dash_test.go` (new).
Covers `dash create`, `dash save`, `dash delete` with `--confirm`. The read half
is already trustworthy: `dash show --raw` was proven byte-faithful to
`lovelace/config` in the audit, so a full round trip is testable today. Assert
the whole config document survives, not just the title.

### Wave 2b — companion required (2 agents, after 2a proves H-12)

These belong in `internal/companiontest/` (build tag `companion`) because they
need the companion sidecar; a plain Docker HA has no Supervisor.

**Agent W-C:** `script apply|create|delete` — owns `internal/cmd/script.go` +
new companion-tier tests.
**Agent W-D:** `tpl create|delete` and `helper create|delete` — owns
`internal/cmd/tpl.go`, `internal/cmd/helper.go` + new companion-tier tests.
`tpl create` corrupted `template.yaml` for trigger-based entries once already
(fixed in v2026.7.3); that regression deserves a permanent gate.

### Wave 2c — write-path defects already known

**Agent W-E — dry-run honesty.** Owns the dry-run preview paths across write
commands (coordinate ownership carefully; this touches many files, so run it
**alone**, not in parallel with 2a/2b).

- **13 write commands accept a fabricated ID** and print a confident "would do X"
  plan at exit 0. Only `ent set-area` and `dash replace` validate. Under the
  manual's "stop at the first miss" rule a typo reads as a successful plan.
  *Fix shape: resolve the target before printing the plan, so the dry run fails
  exactly where the confirmed run would.*
- **`--json` is a no-op on nearly every dry-run preview** — byte-identical with
  and without the flag. Either give previews a JSON shape and fold them into the
  H-10 sweep (`internal/cmd/json_contract_test.go`, currently excludes mutating
  commands), or document them as text-only. Decide, don't drift.

### Hazards for every write agent

- Write tests mutate the shared container. Either take your own instance
  (`hatest.StartShared` + `sync.Once`, as `getOracleHA` does) or restore in
  `t.Cleanup` and **assert the restore** — `internal/integration/auto_label_test.go`
  documents why best-effort cleanup is not enough.
- A new lazily-started container **must** get a matching teardown line in
  `internal/integration/main_test.go` or it leaks.
- `hatest`'s `copyFixtureToTemp` silently skips subdirectories: a fixture can
  never ship `custom_components/` or `.storage/`. Build registry state
  programmatically after start.

---

## Phase 3 — remaining read gaps (2 agents, parallel)

**Agent R-A — cross-command consistency.** Owns `internal/cmd/ent.go`,
`internal/cmd/auto.go`.
- R1: `ent related`'s area-neighbours are silently domain-scoped, narrower than
  `ent ls --area` and than HA's `area_entities()`.
- R2: `auto ls --pattern` / `ent ls --pattern` reject the config `id:` that
  `auto show` accepts for the same automation. **Decide which identifier is
  canonical and make every command agree** — a command must not display an
  identifier another command refuses.
- R3: `runs_24h` counting is inconsistent for condition-blocked runs, so it
  disagrees with `auto show`'s own trace table.
- R5: `ent hist`/`ent who`/`ent anomalies` cannot distinguish a nonexistent
  entity from a quiet one (both exit 0, no rows) while `ent show` correctly
  404s. This one matters most — it turns a typo into a verified negative.

**Agent R-B — history and cache.** Owns `internal/cmd/cache.go`,
`internal/analyze/resample.go`, and `ent hist`'s render path (coordinate with
R-A on `ent.go`; if they collide, run R-B after R-A).
- R4: non-numeric `ent hist` emits one row per raw sample instead of aggregating
  real state runs (37 × 40 min became 287 × `5m0s`).
- R7: `cache clear` leaves `cache/ids.json` despite promising to wipe all cache.
- R8: `--resample 0m` and negative durations silently accepted.
- R6: `log --since` / `cc logs --since` are no-ops. Probably *correct* (HA's
  system log is a fixed in-memory buffer with no time window) — if so, make the
  flag say that instead of silently accepting a value.

---

## Phase 4 — design decisions (orchestrator + user, no agents)

These need a call, not a patch. Do not let an agent guess.

- **R16** `helper ls`/`helper show` hard-depend on the companion though all
  helpers are readable over core APIs, while the routing table offers
  `helper ls` as a one-call answer that fails on HA Container. Drop the
  dependency for the read path, or state it in the routing table.
- **R17** `dash show` with no argument cannot deliver "views summary for default
  dashboard" when the default dashboard is auto-generated. It fails honestly
  today; the promise still does not hold for the common case.
- **R18** `cc show --full` cannot report documentation/dependencies/iot_class/
  codeowners because `haapi.IntegrationManifest` decodes only four fields.
  Extend the struct. (The previous agent correctly refused to fabricate these.)
- **R19** `anom:` stable IDs are minted but no command consumes them — wire or retire.
- **R20** `ent show`'s `changed_by` and `ent who` disagree for entities HA's
  logbook excludes. Each is individually right; decide how to present that.
- **R21** `ref scan`/`ref validate` remain UNPROVEN — covering them means
  companion-tier work.

---

## Phase 5 — verify from outside (round 3)

After Phases 1–3 land, re-run the black-box audit **from a third angle** and on
**new material**. The point is to be validated by something other than the tests
written to drive the fixes.

Angles already used, so pick a different one:
- Round 1: per-command conformance against HA.
- Round 2: regression on new material + cross-command triangulation + untouched territory.
- **Round 3 suggestion:** drive the documented *agent workflows* end to end — take
  each row of the manual's routing table ("What went wrong?", "Daily report",
  "Which automation failed?") and run the exact prescribed sequence against a
  deliberately broken instance, asserting the sequence reaches a correct
  conclusion. That tests the tool the way its primary consumer actually uses it,
  which nothing so far has done.

Rebuild the rig per `docs/HANDOFF-2026-07-23-oracle-testing.md` §6. Consider
promoting the recorder-backfill script into the repo — it is the only way to
backdate history, and therefore the only honest way to test `ent anomalies` and
long `ent hist` windows.

---

## Ready-to-paste subagent brief template

```
Repo /Users/jan/dev/repos/hactl-dev/hactl, branch <BRANCH>. Go CLI for Home Assistant.

You own these files exclusively; do not edit anything else — other agents are
working concurrently in this repo:
  <FILE LIST>

Process, non-negotiable (AGENTS.md): write the test FIRST, run it, observe it
fail, paste the failure output verbatim into your report, then fix. Red → fix →
green. After green, mutation-check: copy the file aside, break the fix, confirm
the test fails for the stated reason, restore from your copy. Never use
`git checkout <file>` to restore — it destroys uncommitted work.

Truth sources: Home Assistant's own API, and INVARIANTS.md H-8..H-11. Never
hand-write an expectation HA can compute — use the oracle harness in
internal/integration/oracle_test.go (oracleAreaEntities, oracleTraceItemIDs,
oracleCustomIntegrations, oracleLogNames, assertSameSet). Verify any claim in
this brief against HA rather than assuming it is correct; a previous agent
found a briefing error that way and it was the most valuable finding of the run.

If an existing test asserts the defect as correct, invert it and say so
explicitly — the old expectation being wrong is a finding.

Task: <TASK>

Done = `make gates` green (lint + unit + all three Docker tiers; Docker must be
running). Report: RED output verbatim, changes and rationale, GREEN output,
any inverted assertions, and anything you could not do and why.
```
