# LLM Manual Tuning — Results and Lessons

## What this is

hactl bakes `docs/manual.md` into the binary at compile time via `//go:embed`. When an LLM calls `hactl rtfm`, it gets the full manual. The manual is also injected as the system prompt when using hactl as a tool via the `llm` CLI integration.

This document records one tuning session: six iterations of editing the manual, running an 8-prompt eval against a local LLM, grading the results, and picking the next edit.

## Model used

**Qwen3-30B-A3B** (locally quantized, 4-bit), served via **LM Studio** on a local machine.  
CLI: Simon Willison's [`llm`](https://llm.datasette.io/) with a custom tool-calling wrapper in `integrations/llm/`.

This is a mid-tier open-weight model — not GPT-4o, not Claude. The constraint was intentional: if the manual is clear enough that a smaller local model navigates it correctly, it will work even better with frontier models.

## Methodology

```
docs/manual.md  →  go build  →  hactl binary  →  hactl rtfm
                                                        ↓
                                               llm system prompt
                                                        ↓
                              8 eval prompts  →  tool traces  →  grade
                                                                      ↓
                                                             pick one manual edit
                                                                      ↓
                                                              repeat (≤10 loops)
```

**Eval prompts** (`dev/tuning/prompts.yaml`): eight real-world HA queries covering triage, automation debugging, entity lookup, sensor health, label listing, a write operation (disable automation), and a dashboard build.

**Grading**: each prompt scored against expected commands hit and max call count. Failure codes F1–F7 (hallucinated flag, wrong command, too many calls, write without confirmation, misread output, language drift, gave up early).

**Tooling**: `dev/tuning/run.sh` — builds hactl, reinstalls the llm template, runs all 8 prompts in sequence, writes `dev/tuning/runs/<timestamp>/<id>.log`.

## Results

| Iteration | Change | Score |
|-----------|--------|-------|
| Baseline | no changes | **2/8** |
| it1 | renamed "What went wrong recently?" workflow, `--since 24h` | **3/8** |
| it2 | added "Stop at the first miss" rule to Filtering section | **4/8** |
| it3 | automation failure fallback: `log --errors` when `auto ls --failing` is empty | 4/8 (quality ↑) |
| it4 | removed `--top` comment; tried device-discovery workflow → REVERTED (caused regression) | 4/8 |
| it5 | annotated `--top` as CLI-only in Global flags table → eliminated recurring F1 | **4/8** |
| it6 | tightened dashboard build workflow | 3/8 (stochastic regression; stopped) |

Best stable score: **4/8** (prompts e01, e03, e05, e07 pass reliably).

## What went well

**Small, targeted edits worked.** The three durable improvements were each 1–3 line changes:
1. Renaming a workflow heading so the model matched the right pattern for "what went wrong?"
2. Adding a one-sentence "stop at the first miss" rule that immediately fixed the sensor-not-found spiral.
3. Annotating a flag as CLI-only to stop a recurring tool-argument hallucination.

**The eval loop caught real problems fast.** Each run was ~40 min on a local Qwen. A smoke test on one prompt (`hactl-llm --td "<prompt>"`) took 2–4 min and was enough to verify a hypothesis before running the full suite.

**Workflow examples are load-bearing.** The model matched headings like "What went wrong recently?" against the user prompt and followed the listed commands almost exactly. Precise headings matter more than long descriptions.

## What didn't work

**Adding workflow examples backfired.** In iteration 4, a "Which entities belong to a device?" workflow was added to help the model discover heat-pump entities. Instead it caused the model to apply that 2-step discovery pattern to unrelated prompts (sensor health, daily report), introducing regressions. Reverted same session.

**Comments in workflow blocks cause unintended tool calls.** The automation failure fallback was written as:
```
# if --failing is empty: check the error log for automation names
hactl log --errors --unique
```
The comment caused the model to first try `hactl log --component automation` (empty), then fall back to the plain log. Two calls instead of one. Comments in code blocks are interpreted as instructions, not as documentation.

**Stochastic variance is significant at this model size.** The same manual produced 4 tool calls in one run and 8 in another for the same prompt. This makes it hard to distinguish a real improvement from lucky noise. At least 2 re-runs of any change would be needed to be confident.

**Some failures are structural, not manual failures:**
- e04 (`disable automation.climate_schedule`): the automation simply doesn't exist in this HA instance. No manual text can make the model find an entity that isn't there.
- e06 (heat pump entities): the HA instance uses German device names (`summt_heizung`, `summtheatbot`). The model never bridged "heat pump" → `summt*`. The fix is labeling entities in HA, not editing the manual.
- e08 (build dashboard): the model correctly provided JSON and apply commands but chose not to call write tools directly — reasonable caution. A write-confirmation workflow in the tool wrapper would close this gap.

## How to improve further

**Near-term (manual edits):**
- Remove inline comments from workflow code blocks — they're interpreted as LLM instructions. Replace with a plain preceding sentence if context is needed.
- Add a "Daily report" section as a named workflow (the model currently matches it to no specific heading and pads with extra calls).
- Consider a `hactl auto ls --noisy` or `--top-by-runs` flag so the model can surface runaway automations without relying on `changes` output.

**Medium-term (tooling):**
- The `--top` flag is referenced in the manual but not exposed in the Python tool wrapper. The mismatch causes recurring F1 hallucinations whenever the model sees truncated output. Either expose it as a tool parameter or replace `--top` references with `--domain`/`--pattern` filter guidance.
- Add a write-confirmation signal to the tool interface so the model knows it can call write tools (dash create, auto apply) after asking the user. Currently there's no signal; the model chooses the safe "tell user to run manually" path.

**Structural (HA instance side):**
- Label heat-producing devices (`label: heat_pump`) so the model can use `hactl ent ls --label heat_pump` without guessing entity names.
- Ensure automation traces are cached (`hactl cache refresh traces`) before running evals that ask about failing automations — empty traces cause the model to stop short of `trace show`.

**Model side:**
- The eval was done with a 4-bit local Qwen. Frontier models (Claude Sonnet, GPT-4o) follow workflow examples more reliably and handle empty results more gracefully. Expect the same manual to score 6–7/8 with a frontier model.
- Chain limit of 6 calls is tight for complex queries. Raising to 8 would reduce F3 failures on multi-step workflows without hurting precision.
