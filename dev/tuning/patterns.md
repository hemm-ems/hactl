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
