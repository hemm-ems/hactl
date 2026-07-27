# Blind audit, 2026-07-26

Three auditors were run against this tree with **no knowledge of issue #94**.
None was told what to look for. One was additionally forbidden from reading
`INVARIANTS.md`, `dev/surfaces/` and `internal/surfaceaudit/`, so that its
findings would be its own.

The exercise had two purposes: check whether the new closure gates
(`make test-surface`) surface the reported defects to someone who does not know
about them, and find out what else is there.

**Result: three of the four reported defects were rediscovered independently,
and twenty further defects were found. Every claim below was re-verified by
hand against the source before being written down.**

The pattern in issue #94 — a fix applied at the site where the symptom was
observed rather than to the class it belongs to — is not a description of four
mistakes. It is the repository's dominant defect mechanism. Nine of the findings
below are the *same shape*: a correct fix, a sibling left untouched, and often a
comment in the fixed file explaining exactly why the sibling is wrong.

---

## Rediscovered without being told (issue #94)

| # | defect | found by |
|---|---|---|
| 1 | `auto apply` dry run prints `validation: ok` + a plan for an id that names no automation, exit 0 | all three |
| 2 | `auto diff` / `auto apply` refuse the identifier `auto ls` prints | the blinded auditor |
| 3 | `trace show` renders UTC, and drops the date the manual documents | the blinded auditor |
| 4 | `device ls --pattern` is case-sensitive while its siblings are not | *not* rediscovered — caught by `TestFilterFlagsAgreeOnCase` |

Finding 4 is worth noting: no auditor found it by reading. The executable parity
gate found it immediately, and found that it is wider than reported — **all four
`--pattern` filters are case-sensitive while every `--name`/`--area`/`--label`
sibling is not**.

---

## New — severity 1

### N1 · Non-idempotent POSTs to Home Assistant are retried on 5xx
`internal/haapi/client.go:310` — `if resp.StatusCode >= 500 && attempt < maxAttempts-1`, with **no method check**.

`CallService` → `doPost` → `doWithRetry`. Seven POST sites route through it:
`CallService`, `CallServiceWithResponse`, `UpdateAutomationConfig`,
`RenderTemplate`, `StartConfigFlow`, `StartOptionsFlow`, `StepFlow`.

`hactl svc call notify.mobile_app --confirm` against an HA that sends the push
and *then* raises → 500 → hactl silently re-POSTs twice → **three
notifications**. Same for `lock.unlock`, `cover.open_cover`,
`counter.increment`, and for `script run --confirm`.

This is H-1 verbatim — "Non-idempotent requests are never auto-retried" — and
H-1's entire "Enforced by:" is `internal/companion/client_retry_test.go`. The
*companion* client has the fix, and carries the comment that states the rule:
"A 5xx means the server received the request, so only idempotent methods retry
it." The HA client in the same repository does the forbidden thing.

`doPostOnce` is the tell. It exists because retrying a flow-start was found to
leave dangling flows — and it was applied to the two call sites where that
symptom appeared, not to `doWithRetry`. Recorded in `dev/surfaces/retry.manifest`.

### N2 · `auto rollback --confirm` always reports `reload: ok`
`internal/writer/writer.go:175-183` — the reload error is logged, then
`ApplyResult{… Reloaded: true}` is returned hardcoded. `writer.Apply`, twenty
lines above, sets the field correctly.

The agent reports the rollback as live. The old config is on disk and HA is
still running the broken one — the worst possible moment to be wrong about
reload state. `docs/manual.md:92` promises the opposite.

### N3 · `cache refresh <unrecognised category>` refreshes nothing, silently
`internal/cmd/cache.go:134-135`. `refreshTraces := category == "" || category == "traces"`;
an unmatched value leaves both bools false, both blocks are skipped, and the
function returns nil having written **zero bytes**. `Args: cobra.MaximumNArgs(1)`
accepts anything.

`hactl cache refresh trace` (singular) → exit 0, empty stdout, nothing on
stderr, cache untouched. Every later `trace show` answers from stale data while
the operator believes it is current.

---

## New — severity 2

### N4 · `helper delete` leaves the ghost the manual promises it removes
`removeOrphanedEntity` (`internal/cmd/orphan.go:25`) is called by `auto delete`,
`script delete` and `tpl delete` — and **not** by `runHelperDelete`
(`internal/cmd/helper.go:352-386`). `docs/manual.md:100` documents the removal
for a block that covers `auto delete` *and* `helper delete`.

The shared helper was created by #90 specifically so the three families would
stop diverging. The fourth family was not in the sentence.

### N5 · `script ls`'s `runs_24h` is capped at HA's stored-trace limit
`internal/cmd/script.go:451-468` counts entries of `traces[entityID]`. HA caps
stored traces per item (default 5). A script invoked 40× reports 5.

`internal/cmd/auto.go:235-237` uses the logbook instead, and says why in a
comment: *"traces are bounded per automation, so they undercount runs_24h
dramatically for high-fire rules."* H-18 was written about the automation
column. The identical column on the sibling command was never touched — and the
comment explaining the defect lives in the file that was fixed.

`docs/manual.md:190-192` explicitly tells the reader the script column is
uncapped.

Same function, same shape: `script.go:462` takes the **first** error trace for a
column named `last_err`, while `auto.go:683-687` compares timestamps precisely
because "HA does not guarantee trace order".

### N6 · `ent related --json` cannot distinguish "does not exist" from "no relations"
`internal/cmd/ent.go:1407-1409`. The `--json` branch returns `[]` before the
`known` flag is consulted, and the non-empty path guards the "not in the
registry" header behind `if !flagJSON`. The comment at `ent.go:1396-1399` names
this exact indistinguishability as the thing the check exists to prevent.

`TestEmptyResultJSON_EntRelated` currently pins `[]` for a gone entity **as
correct** — the third instance in this repository of a test certifying the
defect it should catch.

### N7 · `--json` is not JSON for ten commands
`auto create --json` writes `validation: ok (HA validate_config)` to the same
writer *before* the JSON object (`auto.go:1047` → `:1024`, then `:1055`).
`json.Valid(stdout)` is false on a **successful** command.

Nine previews ignore `--json` entirely and emit prose: `auto apply`,
`script apply`, `script run`, `svc call`, `dash replace`,
`companion wireguard config|up|down`, and `helper show`
(`internal/cmd/helper.go:260-275` never reads `flagJSON` at all).

`TestPreviewJSONIsMachineReadable`, cited by H-2 as enforcing "a preview is
machine-readable", exercises `helper create` and nothing else.
`TestJSONContract` cannot see them because `isMutating` excludes every
`--confirm` command, and `helper show` sits on an 8-wide `companionRequired`
skip list that is logged but not asserted on.

N7 is the meta-failure in miniature: #68's validation gate and #90's
machine-readable previews are each correct alone, and nothing ever checked one
against the other.

### N8 · `--full` silently removes the `--top` cap
`internal/format/format.go:42-47` — `opts.Full` short-circuits the cap for all
fifteen table renderers. `docs/manual.md:725` says `--full` "**Changes nothing
on tables**". The code advertises the opposite in its own hint string:
`"use --full or --top N to see more"`.

An agent told `--full` is free on tables adds it, blows past `--tokensmax`, and
gets a **byte-truncated mid-row** table instead of the clean `…+N more` marker.

### N9 · `companion wireguard status` output changes between identical runs
`internal/cmd/wireguard_cmd.go:122-126` — `for _, v := range m.Resolved { ip = v; break }`
over a `map[string]string`, labelled "the most recent resolved address". Sixty
runs against a byte-identical fixture produced three distinct outputs.

Direct H-16 breach ("two invocations against an unchanged HA MUST produce
byte-identical output"). It is the only genuine ordering defect among the 28
map-ranges in the module — the other 27 are sorted, single-key-guarded, or
set-building.

### N10 · `ent ls --area` refuses the `area_id` that `area ls` prints
`internal/cmd/ent.go:1109-1119` matches the area **name** only;
`internal/cmd/device.go:249` matches id **or** name. So `ent ls --area kitchen_id`
returns zero rows, exit 0, while `device ls --area kitchen_id` returns the row.
`--label` has an existence pre-check and errors; `--area` does not, so the miss
is a silent empty table.

H-17's own mechanism, in a family H-17 does not mention. Partially mitigated:
the flag help does say "name" — but H-17's clause is "printing is a promise".

---

## New — severity 3

| # | defect | site |
|---|---|---|
| N11 | `svc call` previews a plan without contacting HA at all — the service name is only split on `.`; `/api/services` is never consulted | `internal/cmd/svc.go:63-67`, returns before `config.Load` |
| N12 | `dash create` previews without validating `--url-path` (its own help says "must contain a hyphen") or contacting HA | `internal/cmd/dash.go:526-534` |
| N13 | `--confirm` is refused on a family's first command in any agent-shaped session — documented nowhere in `docs/manual.md`; the manual's own worked example at lines 85-91 trips it | `internal/cmd/inject.go:114-146` |
| N14 | `anom:` stable ids do not exist. `docs/manual.md:709` documents them; no `GetOrCreate(… "anom" …)` exists anywhere | `internal/cmd/ent.go:627,897` |
| N15 | `log --errors --warnings --unique` — the manual's #1 routing entry — emits no `log:` ids, so the prescribed `log show <id>` drill-down has nothing to reference | `internal/cmd/log.go:97-125` |
| N16 | `--stats` is not printed when a command fails; `docs/manual.md:714` says "after any command" | `internal/cmd/root.go:199-223` |
| N17 | `hactl health` prints `⚠` — `docs/manual.md:711` says "no emojis, no color". The only such glyph in the codebase, on the most-called command | `internal/cmd/health.go:140` |
| N18 | `tpl delete`'s error says "use 'tpl ls'"; there is no `tpl ls`, and `docs/manual.md:31` says "No other commands exist — never invent one" | `internal/cmd/tpl.go:308` |
| N19 | `ref replace --json` suppresses the `dry_run` marker, so a preview and a completed rename return structurally identical documents differing in one cell | `internal/cmd/ref.go:223-243` |
| N20 | `docs/manual.md:411-414` tells the reader `helper create`'s dry run does **not** read the file. It does, and refuses correctly (`helper.go:292-295`). The doc is stale in the direction that makes callers add redundant validation | — |

---

## What the auditors cleared

Stated so the report is not read as exhaustive-by-omission. Verified sound:
`--json` never truncated by `--tokensmax`; `--top` never shortens `--json`;
`apply`/`rollback` dry runs write nothing; candidate validation runs in both
modes; `log`/`cc logs` ignore the 24h default unless `--since` is passed;
`ref replace` aborts before any write when the companion is unreachable;
`config file`/`block` misses are non-zero errors; `--resample 0m` and negatives
are refused; `ent hist`/`who`/`anomalies` fail on an unknown entity;
`cache clear` drops `ids.json`; `--color` is a genuine no-op; the MCP gate is
exhaustive and fails closed. Twelve of the eighteen ledger debt entries turned
out to be behaviour that is correct but unasserted, not defects.

---

## The counter-example

Four invariants have **zero** gap between what they claim and what they check:
H-3, H-13, H-14, H-19. All four derive their set from the source —
`companion.Endpoints`, the degeneracy sweep, the AST test walker, a byte-level
spec diff — so the list cannot drift from the set.

They are also the four that have never produced a repeat defect.

That is the entire argument for `dev/surfaces/`.
