# Handover — oracle testing, the write path, and the remaining read issues

**Date:** 2026-07-23
**Branch:** `fix/read-surface-oracle-2026-07-23` (hactl), commits `17039dd` + `9e810a1` (+ gate-enforcement commit)
**Status:** read-surface root causes fixed and gated. Write path is **unproven**. 22 read findings remain open.

This document exists so the next person can continue without re-deriving anything.
Read it with `hactl/INVARIANTS.md` (H-8..H-11) and `hactl/docs/testing.md` open.

> **Update, same day (evening):** §7's steps 1–3 are done and merged (#85, #87,
> #88): the branch landed, R9–R15 are fixed, and the write families needing no
> companion are gated by **H-12** — a write is proven by reading it back from HA.
> R1, R4, R5, R6, R7 and R8 are fixed too. What remains from §4 is the
> companion-tier write path (W1/W3/W4) and the dry-run-honesty defects; from §5,
> R2/R3 and every design question. See `docs/PLAN-next-session.md` for the
> per-item status table. Branch protection still needs pointing at
> `All Gates Green`.


---

## 1. What happened, in one paragraph

An independent black-box audit ran hactl against a purpose-built live Home
Assistant 2026.7.2 and compared every read against HA's own API. It found 26
defects while ~850 unit and ~240 integration tests were green. Two root causes
accounted for most of the damage: **traces were looked up by the entity
object_id while HA keys them by the automation's config `id:`**, and **an
entity's device-inherited area was never resolved**. Both were invisible to the
existing suite *by construction* — no fixture could express them. The fixes
landed with a new test pattern (below); a second audit pass from different
angles re-proved them on material created after the fact and found four more
defects of the same classes, which were also fixed.

**The lesson worth carrying forward:** the failure was never coverage. It was
that expectations were hand-written by the same people who wrote the code, so
tests and implementation shared a modelling mistake and agreed with each other
while disagreeing with Home Assistant.

---

## 2. The test pattern — reuse this verbatim

Four invariants, all in `hactl/INVARIANTS.md` with enforcing tests.

### H-8 — Distinguishability, and exercise

The fixture must let every identifier that *can* differ actually differ, **and**
must exercise the code that consumes it.

The old fixtures already had automations where config `id ≠ slug(alias)` — six
of seven, in fact. It never mattered, because every one of them was triggered by
an event that never fired, so no trace existed and there was nothing to look up
with the wrong key. A distinguishing fixture nobody exercises proves nothing.

`internal/integration/oracle_identity_test.go` guards both clauses:
`TestOracleFixtureIsDistinguishing` fails if a fixture entry lets identifiers
collide; `TestOracleFixtureIsExercised` fails if HA holds no traces.

### H-9 — Home Assistant is the oracle

For any read HA can answer itself, compute the expectation **from HA at test
time**. Never hardcode, never golden-file a set.

`internal/integration/oracle_test.go` is the harness. Accessors available:

| helper | asks HA |
|---|---|
| `oracleAreaEntities(t, inst, areaID)` | `{{ area_entities(...) }}` — the canonical definition of area membership, inheritance included |
| `oracleLabelEntities(t, inst, labelID)` | `{{ label_entities(...) }}` |
| `oracleEntityArea(t, inst, entityID)` | `{{ area_name(...) }}` |
| `oracleTraceItemIDs(t, inst, domain)` | `trace/list`, keyed as HA keys it |
| `oracleErroredTraceItemIDs(...)` | traces carrying an `error` |
| `oracleCustomIntegrations(t, inst)` | `manifest/list` where `is_built_in == false` |
| `oracleLogNames(t, inst)` | `system_log/list`, names + summed `count` |
| `assertSameSet(t, what, want, got)` | symmetric diff, labelled "missing from hactl" / "invented by hactl" |

Why this is not optional: `area_entities()` would have caught the inheritance
bug on day one. No hand-written expectation ever would, because whoever writes
it repeats the implementation's mistake — that is precisely what happened.

### H-10 — `--json` is a machine contract

`internal/cmd/json_contract_test.go` walks the **cobra command tree**, not a
hand-kept list, so a new command is covered without anyone remembering. It
currently gates 33 read commands on: parses strictly; `--top 1` and `--top 1000`
yield the same element count; no human header precedes the JSON. Eight
companion-dependent commands are skipped and **printed in the test output**, so
the gap is visible rather than silent.

### H-11 — No fabrication, and counts reconcile

Every identifier hactl prints must exist in HA's own answer, and every count
must reconcile with the count its source reported.

### The fixture

`hactl/testdata/fixtures/oracle/` — three top-level YAML files (`hatest`'s
`copyFixtureToTemp` silently skips subdirectories, so a fixture can never ship
`custom_components/` or `.storage/`). It enables `demo:` for one reason: HA has
no "create device" WS command, devices only come from integrations, and without
devices the inheritance path cannot be tested at all. Registry state (floors,
areas, labels, device placement) is built programmatically in
`buildOracleRig()`, and `exerciseOracleRig()` fires the automations for real.

---

## 3. Mandatory Docker roundtrip — now enforced

`make gates` is the only definition of done. It runs lint + unit + **all three
Docker tiers** and refuses to start if Docker is not running, rather than
silently narrowing what was verified.

```bash
make gates     # lint, test, test-int, test-companion, test-int-discovery
make hooks     # installs dev/hooks/pre-push, which runs the gates on every push
```

`make test` now prints a banner saying it is the unit tier only and never
acceptance. CI gained an aggregating **`All Gates Green`** job that `needs:` every
other job and fails if any result is not `success` — so a skipped or cancelled
Docker tier cannot pass. **Point branch protection at that single check.**

> **Action for the maintainer:** update the required status checks in branch
> protection to `All Gates Green`. Until that is done, the aggregator runs but
> does not block.

---

## 4. The write path — completely unproven, and the top priority

The audit deliberately covered reads only. What is known:

- **Dry-run safety holds.** 21+ write commands were run without `--confirm` and
  provably mutated nothing (registries, states, config entries, dashboards,
  in-progress flows, config directory all identical before/after).
- **Actual writes have never been verified black-box against HA.** Only
  `auto apply`/`rollback` have a byte-level round-trip gate (H-4,
  `internal/integration/write_roundtrip_test.go`). `docs/testing.md` already
  admits `script apply` and `dash save` "can each be replaced with a stub
  without any test failing" — the exact state the automation write path was in
  before H-4.

### Proposed H-12 — a write is proven by reading it back from HA

Generalise H-4 to every write family. A write test must:

1. Read the current state **from HA directly** (not via hactl).
2. Write via hactl with `--confirm`.
3. Read back **from HA directly** and compare the whole document, not just the
   field the renderer happens to show.
4. Assert on a field the command did *not* mention — a second, independent
   witness that the whole document was written.
5. Restore, and assert the restore too.

`write_roundtrip_test.go` already does this for automations, including a
`canonicalize()` helper that folds HA's legacy singular schema keys
(`trigger`→`triggers`, `service`→`action`) because HA rewrites an automation's
schema on write. **Steal that helper verbatim.**

### Write-path work list, in priority order

| # | Target | Why it matters | Notes |
|---|---|---|---|
| W1 | `script apply` / `script create` / `script delete` | no round-trip gate at all; backup + validation helpers are stubbable | needs companion; mirror H-4 |
| W2 | `dash save` / `dash create` / `dash delete` | same; `dash show --raw` was proven byte-faithful, so the read half of the round trip is trustworthy | no companion needed — `lovelace/config` is core WS |
| W3 | `tpl create` / `tpl delete` | block-aware editing already corrupted `template.yaml` once (v2026.7.3) | needs companion |
| W4 | `helper create` / `helper delete` | untested write path | needs companion |
| W5 | `ent set-label` / `ent set-area` | pure core WS, cheapest to gate | no companion needed |
| W6 | `auto rollback` destructive-clobber | a prior review flagged `auto rollback` can clobber; confirm or retire that finding | see `project_testing-overhaul-2026-07-22` |
| W7 | `config flow-start` / `flow-step` with `--confirm` | stateful, multi-step, creates real config entries | isolate: these leave entries behind |

**Two hazards to plan for.** Write tests mutate the shared container, so they
need either their own instance (`hatest.StartShared` + a `sync.Once`, as
`getOracleHA` does) or strict `t.Cleanup` restoration — `internal/integration/
auto_label_test.go` documents why cleanup must be asserted, not best-effort.
And several families need the companion, which means the work belongs in
`internal/companiontest/` (build tag `companion`) rather than the integration
tier.

### Known write-path defects already found, not yet fixed

- **Dry-run previews do not validate the target exists.** 13 write commands
  accept a fabricated ID and print a confident "would do X" plan with exit 0.
  Only `ent set-area` and `dash replace` validate. For an agent following the
  manual's "stop at the first miss" rule, a typo reads as a successful plan.
  *Fix shape: resolve the target before printing the plan; make the dry-run
  fail the same way the confirmed run would.*
- **`--json` is a no-op on nearly every dry-run preview** — output is
  byte-identical with and without the flag. The H-10 sweep deliberately excludes
  mutating commands; either give previews a JSON shape and fold them into the
  sweep, or document them as text-only.
- **`ent set-label` and `ent set-area` disagree** on the same unregistered
  entity — one errors, the other does not.

---

## 5. Remaining read issues, triaged

None of these are fixed. Severity is my judgement; the evidence is in
`scratchpad/proof/reports/` (per-agent round 1 + `r2-*.md` round 2).

### Should fix — behaviour is wrong or misleading

| # | Issue | Evidence |
|---|---|---|
| R1 | `ent related`'s area-neighbours are silently **domain-scoped**, so they are narrower than `ent ls --area` and than HA's `area_entities()`. Manual promises "area neighbors" without qualification. Reproduced 6× | `r2-triangulation.md` |
| R2 | `auto ls --pattern` / `ent ls --pattern` **reject the config `id:`** that `auto show` accepts and resolves for the same automation — an identifier one command displays and another refuses | `r2-triangulation.md` |
| R3 | `runs_24h` counting is inconsistent: falls back to raw trace count when *all* runs in the window were condition-blocked, but drops blocked runs when *some* succeeded, so it disagrees with `auto show`'s own trace table | `r2-triangulation.md` |
| R4 | Non-numeric `ent hist` emits one row per raw sample (`duration: 5m0s` ×287) instead of aggregating real state runs (37 × 40 min) | `c-history-analysis.md` |
| R5 | `ent hist` / `ent who` / `ent anomalies` cannot distinguish a nonexistent entity from a quiet one — both exit 0 with no rows, while `ent show` in the same family correctly 404s. Under "stop at the first miss" a typo reads as a verified negative | `r2-new-territory.md` |
| R6 | `log --since` / `cc logs --since` are complete no-ops. Arguably correct (HA's `system_log` is a fixed in-memory buffer with no time window) — but then the flag should say so instead of silently accepting a value | `r2-new-territory.md` |
| R7 | `cache clear` leaves `cache/ids.json` behind despite promising to "wipe all local cache" | `g-output-…md`, `c-history-…md` |
| R8 | `--resample 0m` and negative durations silently accepted | `c-history-analysis.md` |

### Documentation is wrong (cheap, high value for agent users)

| # | Issue |
|---|---|
| R9 | Manual has **zero prose** for the `ref scan\|replace\|validate` family and for `dash grep\|replace` — all real, working, `--help`-documented commands |
| R10 | `dash grep --help` says "an exact entity_id"; it string-matches **any** JSON string value (it hit a markdown card's prose) |
| R11 | "ISO only with `--full`" is wrong in both directions: `--full` never produces ISO in table mode, and `--json` produces ISO without `--full` |
| R12 | `ent show`'s "(+N hidden)" is the **total** attribute count, not a hidden delta |
| R13 | `--color` is a documented no-op (deliberate — removing a documented flag would break callers). `--help` now says so; the manual should too |
| R14 | `auto cat` is in the command set with no manual prose |
| R15 | `--top` is documented as "max rows in tables"; it is now genuinely table-only, so the docs and behaviour agree — **verify this stays true** |

### Design questions — need a decision, not a patch

| # | Issue |
|---|---|
| R16 | `helper ls` / `helper show` hard-depend on the companion, though all 30 helpers are readable over plain core APIs (`/api/states`, `input_boolean/list`). The routing table sends agents to `helper ls` as a one-call answer, which fails on HA Container. **Either drop the companion dependency for the read path, or say so in the routing table.** |
| R17 | `dash show` with no argument cannot deliver the promised "views summary for default dashboard" when the default dashboard is HA's normal auto-generated state. It fails honestly with HA's `config_not_found` rather than fabricating — but the promise does not hold for the common case |
| R18 | `cc show --full` cannot report documentation / dependencies / iot_class / codeowners / issue tracker because `haapi.IntegrationManifest` decodes only `domain`, `name`, `version`, `is_built_in`. **Extend the struct** — the fix agent correctly refused to fabricate placeholders |
| R19 | `anom:` stable IDs are minted but no command consumes them |
| R20 | Attribution views disagree for entities HA's logbook excludes (continuous sensors): `ent show`'s `changed_by` reports a real recent change while `ent who` / `changes` correctly report nothing. Each is individually right; the disagreement is confusing |
| R21 | `ref scan` / `ref validate` remain **UNPROVEN** — they need the companion, which a plain Docker HA has no Supervisor for. Covering them means work in `internal/companiontest/` |
| R22 | `--json` on dry-run previews (see write path above) |

---

## 6. The rig — how to reproduce any of this

The audit rig lives in the session scratchpad and is **ephemeral**; rebuild it
from `scratchpad/proof/` if you still have it, or re-create with:

- `docker run -d --name hactl-proof-ha -p 8127:8123 -v <config>:/config ghcr.io/home-assistant/home-assistant:stable`
- onboard via `POST /api/onboarding/users` → `/auth/token` → `core_config` →
  WS `auth/long_lived_access_token` (see `internal/hatest/hatest.go`, which
  automates exactly this)
- `gt/ws` and `gt/rest` are thin wrappers that talk to HA **directly**; hactl is
  never in the ground-truth path
- 72h of recorder history was written by inserting rows straight into
  `home-assistant_v2.db` (schema v53) with the container stopped, because HA's
  API cannot backdate history. That is what made gap/stuck/spike detection
  testable — and `ent anomalies` passed it perfectly, including the negative
  control.

**Consider promoting the rig into the repo.** Most of it is now redundant with
`testdata/fixtures/oracle/` + the oracle harness, but the recorder-backfill
script has no equivalent and is the only way to test `ent anomalies` and long
`ent hist` windows honestly.

---

## 7. Suggested sequencing

1. **Merge the current branch** and point branch protection at `All Gates Green`.
2. **R9–R15 (docs)** — a single afternoon, and they matter disproportionately
   because the manual *is* the contract for LLM callers.
3. **W5 + W2** (`ent set-label`/`set-area`, `dash save`) — the write families
   that need no companion, so they land in the integration tier and prove the
   H-12 pattern cheaply.
4. **W1, W3, W4** in `internal/companiontest/` once H-12 has a working shape.
5. **R1–R3** — the cross-command inconsistencies; they need a decision about
   which identifier is canonical, so pair them with R16's design call.
6. **R4–R8** as capacity allows.

## 8. Things not to do

- Do not weaken a test to get past the gates. Three tests in this repo asserted
  defects as correct — including one literally named *"user_id wins over
  event_type/name (rule order)"* — and that is exactly how the defects survived.
- Do not add a hand-written expectation for anything HA can answer itself.
- Do not add a fixture entry where identifiers coincide; H-8's guard will fail,
  and it is failing for a good reason.
- Do not mark a Docker tier optional to make a run green.
