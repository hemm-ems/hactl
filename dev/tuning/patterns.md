# Tuning patterns — what we've changed and why

Append one block per iteration. Date in ISO. Cite the run timestamp so the
diagnosis can be re-derived from `runs/<ts>/<id>.log`.

Format:

```
## YYYY-MM-DD <iteration label>
- F<n> on e<id>: <one-line description of what went wrong>.
  Fix: <one-line description of manual change>.
  Effect: <what the next run showed>.
```

---

## 2026-05-05 Iteration 1 — workflow rename
- F2 on e01: model called `auto_ls --failing` instead of `changes` for "what went wrong?" prompt.
  Fix: renamed "What broke in the last hour?" → "What went wrong recently?" and changed `--since 1h` to `--since 24h`.
  Effect: e01 fixed (PASS). Model now calls [health, log, changes] cleanly.

## 2026-05-05 Iteration 2 — stop-when-blocked rule
- F3 on e03/e04/e06: model kept broadening pattern searches after empty results.
  Fix: added "Stop at the first miss" blockquote to Filtering section.
  Effect: e03 fixed (PASS, 4 calls). e04/e06 partially improved but still F3/F7.
  Score: 4/8 pass (e01, e03, e05, e07).

## 2026-05-05 Iteration 3 — automation failure fallback
- F3 on e02: model called `changes` (irrelevant) before finding automations via log.
  Fix: dropped `health` from "Why did my automation fail?" workflow; added fallback comment
  `# if --failing is empty: check the error log for automation names`.
  Effect: e02 path improved (no wasted `changes` call), but still 5 calls + crash. No score change.

## 2026-05-05 Iteration 4 — (partial regression, partially reverted same session)
- F1 on e06: model hallucinated `top` kwarg on `ent_ls` call (saw "paged via --top" in comment).
  Fix-A: removed `(paged via --top)` from ent ls comment.
  Fix-B (BAD): added "Which entities belong to a device?" workflow.
  Effect-B: caused e03 regression (extra ent_ls) and e06 spiral (12 calls). Reverted Fix-B immediately.

## 2026-05-05 Iteration 5 — --top at source fix
- F1 on e06: `--top` hallucination persisted via global flags table reference.
  Fix: added "(CLI only — not a tool kwarg; use filters instead)" to `--top` row in Global flags table.
  Effect: F1 eliminated from e06. No further `top` kwarg attempts across 3 subsequent runs.
  Score stable at 4/8. e02 clean 4-call stop on this run (stochastic).

## 2026-05-05 Iteration 6 — dashboard workflow (STOP)
- F7 on e08: model did 2-3 entity discovery calls then stopped without attempting write path.
  Fix: tightened dashboard workflow heading and body ("one call, stop here" for discovery).
  Effect: e08 used 1 call and gave correct output + instructions. e02/e05 regressed (stochastic).
  Score: 3/8 strict (stochastic variance). Stopping — plateau reached.

---

## Final state after 6 iterations

| Prompt | it0 | it6 | Verdict |
|--------|-----|-----|---------|
| e01 "what went wrong?" | F2 (3 calls, wrong cmd) | PASS (3 calls) | Fixed: workflow rename |
| e02 "which automation failed?" | F3 (5+crash) | F3 (5+crash, stochastic) | Quality improved; trace_show unachievable (traces: none in data) |
| e03 "is sensor.wp_vl normal?" | F3 (5+crash) | PASS (4 calls) | Fixed: stop-when-blocked rule |
| e04 "disable climate_schedule" | F7 (crash) | F7 (crash) | Data gap: automation doesn't exist |
| e05 "daily report" | PASS | PASS (usually) | Stable; occasional stochastic call explosion |
| e06 "heat pump entities" | F3+F1 | F3 (no F1) | F1 fixed; F3 unfixable (concept-mapping gap) |
| e07 "list all labels" | PASS | PASS | Stable |
| e08 "build energy dashboard" | F7 | F7-safe (1 call, good output) | Better behavior; formal pass requires write tool confirmation |

Net: baseline 2/8 → best stable 4/8 strict (4/8 e01, e03, e05, e07).

---

# Session 2026-07-05 — qwen3.5-122b on rapid-mlx (direct, no dirigent)

Model switch: Qwen3.6-27B/LM Studio → qwen3.5-122b-mxfp4 on rapid-mlx
(192.168.42.114:8000/v1, hermes tool parser). ~5 s/tool-turn instead of
2–4 min: a full 8-prompt eval now takes ~7 min, not 42.

## 2026-07-05 Run 1 (2046) — new-model baseline, old eval set
- Old eval set scored 2/8 strict, but most FAILs were stale expectations,
  not model errors: e06 solved via NEW `device ls` path (May's "concept gap"
  gone), e02/e04 honest "nothing failed / not found" against data gaps.
- e08 revealed architecture bug: manual in system prompt AND rtfm exposed as
  tool → model called rtfm mid-chain, 7k redundant tokens, chain-limit death.

## 2026-07-05 Run 2 (2053) — eval-set refresh (no manual change)
- Re-anchored e03→sensor.aussen_temperatur_mittel, e04→automation.standby_nachts;
  e02 drops unachievable trace_show; e06 accepts device path (expect_any).
  Added grade.py (automated grading, expect_any support). 3/8 + 2 CHECK.
- e05 "daily report" collapsed to a single `changes` dump — no Daily-report
  workflow anchor in manual (May backlog item, still open). Chain limit of 6
  aborts runs invisibly (llm counts responses, incl. final answer) → raised
  harness --cl to 8 (grader still enforces per-prompt budgets).

## 2026-07-05 Run 3 — cold start (manual OUT of system prompt, rtfm-first)
- New: system-cold.md (compact agent rules), HACTL_LLM_SYSTEM_FILE in
  install.sh. Rationale: MCP-era agents get no hactl system prompt; the
  manual only works if fetched via rtfm. prompts.yaml call budgets already
  assumed the rtfm call.
- Result 2/8 strict but crisp signal: prompts WHERE THE MODEL READ rtfm were
  excellent (e04: exact svc-call proposal + confirmation question; e05 full
  report at budget). Prompts where it skipped rtfm spiraled (e01 8 calls,
  e06 7 calls) — prompt-level "call rtfm first" is obeyed ~50%.

## 2026-07-05 Run 4 — rtfm gate in tools.py
- All tools return "ERROR: read the manual first" until hactl_rtfm() ran
  (per-process = per-conversation). Deterministic rtfm-first instead of
  prompt persuasion. Result: see below.

## 2026-07-05 Run 4 — hard rtfm gate: REJECTED
- Gate enforced compliance but taxed the eval: models leading with a work
  tool burned a failed round on the gate error (e07 3/2 over budget; e01
  truncated its workflow under budget pressure). 2/8. Mechanism wrong.

## 2026-07-05 Run 5 — manual auto-injection: WINNER (4/8 + 2 correct CHECKs)
- First real tool call gets the manual prepended to its result: zero wasted
  rounds, deterministic delivery. Best run of the day; e04/e08 CHECKs are
  behaviorally correct (exact command proposed + confirmation asked).
- New failure mode: model re-called rtfm up to 3× (21k tokens) — the exposed
  rtfm tool invites redundant reads.
- Deployable idea for hactl itself: `hactl mcp` could prepend the manual
  resource to the first tool response of a session.

## 2026-07-05 Run 6 — rtfm dedupe (repeat rtfm returns short notice)
- Dedupe works (single rtfm everywhere). 3/8 + 2 CHECK — the delta vs run 5
  is e05/e08 stochastic flips, not the change. Variance >= single-change
  effect at n=1: judge configs on repeated runs only (May lesson, confirmed).
- e06 stable-FAIL across all runs: English concept → German entity names
  needs exploration; queued manual hint "search with the shortest
  distinctive substring" (run-1 luck: name='heat' matched Summtheatbot;
  'heat pump' matches nothing).

## 2026-07-05 Runs 7–9 — manual restructure + concept hint, variance check
- Run 7 (workflows moved to manual top): 4/8 + 2 CHECK, ties best. e04/e08
  answered in ONE call with the exact right command quoted from the manual.
- Run 8 (concept-search workflow added): 3/8. Hint not yet effective for e06.
- Run 9 (repeat, no change): 3/8. e06 followed the device path but needs >4
  calls on this instance (German names); e01 spiraled to 11 calls once.

## Session verdict after 9 runs
- Stable PASS every run: e02, e03, e07.
- Stable behaviorally-correct CHECK: e04, e08 (honest refusal + exact command
  + confirmation question; blocked only by missing svc_call/dash wrappers).
- Stable FAIL: e01 (model prefers depth — log show drilling — over finishing
  the breadth sweep with `changes`; manual hint queued), e06 (budget 4 too
  tight for concept mapping on a German-named instance — consider budget 6
  or HA-side labeling, as in May).
- Flapping: e05 (2/5 runs pass; needs a "Daily report" workflow anchor —
  May backlog item, STILL open, now top of queue).
- Net vs May: strict score similar (3–4/8) under a HARDER contract (cold
  start, no manual in prompt) with far better answer quality: honest
  evidence-based reports, correct commands quoted verbatim, zero F1
  hallucinations, zero F4 unconfirmed writes across all 72 prompt-runs.
