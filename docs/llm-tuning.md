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

---

# Session 2 — 2026-07-05, qwen3.5-122b (rapid-mlx), cold-start rtfm

Second tuning session, new setup: **qwen3.5-122b-mxfp4** on rapid-mlx
(M3 Ultra, 262k ctx, hermes tool parser) called directly via the `llm` CLI —
~5 s per tool turn, a full 8-prompt eval in ~7 min (was 42). Architecture
switched from manual-as-system-prompt to **cold start**: minimal system
prompt (`dev/tuning/system-cold.md`), manual delivered by the tool layer.

## What was measured (14 runs, `dev/tuning/patterns.md` has per-run details)

Manual-delivery architectures (runs 1–9):

| Config | Result |
|---|---|
| Manual in system prompt + rtfm tool exposed | Model re-reads rtfm mid-chain, 7k redundant tokens, chain death |
| Cold start, rtfm-first as prompt rule | Obeyed ~50%; skippers spiral (8 calls), readers excel |
| Cold start, hard rtfm gate (tools error until rtfm) | Compliance 100% but gate errors burn rounds — net worse |
| Cold start, **manual auto-injected with first tool result** | Winner: no wasted rounds, deterministic delivery |

Tool-surface completion (runs 10–14, on the winning architecture):

| Change | Result |
|---|---|
| Confirm-gated `svc_call` wrapper | 5/8; str param for JSON data 400'd the chain (schema-enforcing server) |
| `data: dict` fix + gated dash wrappers | 6/8 + 1 CHECK — first e08 pass via dry-run proposal |
| Sweep-completion manual edit | **F4: real dashboard created on the live instance** — the manual's own workflow block contained `--confirm` |
| F4 fix: dry-run forms in blocks, "the original request is not confirmation" in prose + system prompt | 5/8, write held at dry-run boundary |
| Verify-first docstring, honest e06 budget | **7/8, zero CHECK, zero F4** (May best: 4/8 with the manual in the system prompt) |
| 2× repeat runs | 7/8 both — confirmed; e01 the only stable fail (17 runs) |
| Routing table at manual top; CLI svc gate; wrappers + 4 new-surface prompts | **12/12** — first perfect run, and e01's first pass |
| Manual diet (human setup content → docs/setup.md) | 10/12 + 1 correct CHECK — no regression; e01 passed again |

## Rules for future manual (rtfm) updates

1. **Write for mid-conversation delivery.** The manual arrives inside a tool
   result (injection, rtfm, MCP resource) — not as a system prompt. Order:
   behavioral rules and workflows first, command reference after,
   human-only setup content last. The model reads the head and skims the tail.
2. **Workflows are the load-bearing part.** Headings must match verbatim user
   phrasings ("What went wrong recently?"). No comments inside code blocks —
   they are executed as instructions (May finding, still true). Guidance goes
   in prose after the block.
3. **Every PR that adds/changes a command must update the manual AND append
   an eval prompt.** The May→July drift (device/ref/tpl/config landed, eval
   never heard of them) silently invalidated half the eval set. Eval ids are
   append-only; never renumber.
4. **Re-anchor the eval set to the live instance before each session.**
   Prompts referencing entities/automations that no longer exist measure
   nothing. `uv run dev/tuning/grade.py <run-dir>` grades a run in seconds.
5. **One hypothesis per run, but judge on repeats.** At this model size,
   2–3 prompts flip per run stochastically; an n=1 delta is noise. Runs are
   cheap now — verify any conclusion on at least 2 runs.
6. **Tool surface must match the manual's promises.** If the manual documents
   a write path, expose a confirm-gated wrapper (confirm=False → plan text
   only, never executes). Documented-but-unexposed paths (svc call, dash *)
   produce stalls or honest refusals — fine behavior, failed task.
7. **Smart models need affordances, not scripts.** The frontier-vs-local gap
   moved: qwen3.5 reasons through workflows fine but needs *facts* it cannot
   guess — localized/vendor names ("search the shortest distinctive
   substring"), which tools exist, what confirmation protocol applies.
8. **Nothing goes in a code block that must not be executed.** Run 12: the
   dashboard workflow block contained `--confirm`, the "confirm with user"
   guidance sat in a code comment — the model executed the flag and ignored
   the comment, creating a real dashboard on the live instance. Blocks show
   the dry-run form; prose carries the protocol.
9. **Spell out confirmation semantics: the original request is not
   confirmation.** The model treated "build me a dashboard" as consent to
   write. Safety text is strongest in the tool's own response (the svc_call
   DRY-RUN text held every run), then the system prompt, then the manual —
   in that order. Manual comments rank last and lose.
10. **Design tool signatures for how models call them.** Strict
    schema-enforcing servers (rapid-mlx) turn type mismatches into fatal
    chain errors: JSON payloads are `dict` parameters, never stringified
    JSON. Error messages must teach the next step — the "+N more (try
    --pattern)" cap hint and the url-path hyphen error both steered the
    model to self-correct.

## Open items (next session)

- e01: after 17 straight fails, passed twice in a row once the **routing
  table** landed at the manual top ("question → exact call sequence").
  Promising but n=2 — keep watching before declaring it solved.
- e06 still flaps: the model keeps searching full phrases ("heat pump")
  despite the shortest-substring rule; its ask-back answers are reasonable.
  HA-side labeling (label: heat_pump) remains the robust fix (May idea).
- Auto-generate the Command Reference from cobra definitions — the largest
  manual section, the only one that can silently drift from the binary.
- grade.py could aggregate pass-rates across N runs (variance dashboard).
- CI regression eval against a fixture instance (no live HA dependency).
- MCP elicitation (spec feature) would move write confirmation to the
  client UI instead of trusting the model to relay it.

Done this session (part 2): `svc_call` + dash gated wrappers; CLI-level
`svc call` dry-run gate (breaking); `hactl mcp` manual injection with
tests; e09–e12 new-surface prompts + wrappers; routing table; manual diet
(human setup → docs/setup.md); 7/8 → 12/12 on the extended set.

---

# Session 3 — 2026-07-06, progressive manual delivery

**Hypothesis (Jan):** don't inject the full 7k-token manual with the first
tool result — inject a ~1.4k core (routing table, mental model, filtering,
output conventions, global flags, confirmation prose) and deliver each
command family's how-to with the result of the *first* call into that
family. The timing argument: the routing table + tool docstrings are enough
to form a correct first call, every risky first call is dry-run-gated, and
the family detail arrives exactly before calls 2..n of a workflow.

Implemented in `integrations/llm/tools.py` behind `HACTL_MANUAL_MODE`
(`full` = default, unchanged; `progressive` = core + per-family). Sections
are parsed from `hactl rtfm` output by heading, so `docs/manual.md` needed
no changes. `dev/tuning/inject_tokens.py` measures injection overhead.

## Results (6 progressive runs vs full-mode runs 18/19)

| | full (n=2) | progressive (n=6) |
|---|---|---|
| PASS rate | 22/24 | 62/72 |
| PASS rate excl. e01 | 20/22 | 62/66 |
| injected tok/prompt | ~7.2k | ~2.1k (-71%) |
| wall time/run | ~7 min | ~4–5 min |
| F4 unconfirmed writes | 0 | 0 |

- Quality is on par outside e01; the entire gap is e01 (0/6 progressive vs
  2/2 in runs 18/19 — but 2/19 lifetime; see patterns.md for why that
  comparison is regression to the mean). e01 remains the unsolved
  sweep-completion prompt in both modes.
- Two refinements tested, neither measurable at n=2: a "complete the
  routing-table sequence before drilling" line in family headers, and
  moving the cross-family sweep workflows into the core. The latter is kept
  anyway as the more principled split (cross-family behavior in core,
  single-family workflows with their family).
- Injection adjacency cuts both ways: the device section landing next to a
  missed search helped e06 (5/6, better than full mode); log-show examples
  landing next to log results tempt mid-sweep drill-downs (e01/e02 budget
  fails).

## Rule 11 (extends "Rules for future manual updates")

11. **Deliver reference lazily, behavior eagerly.** The routing table and
    cross-family workflows must be in the first injection; per-family
    reference can arrive with the family's first result at ~30% of the
    context cost and no measured quality loss. When editing the manual,
    keep family sections self-contained (they are injected standalone) and
    keep heading names stable — `tools.py` **and**
    `internal/manual/families.go` map families to headings verbatim. The Go
    side is CI-enforced: `internal/manual` guardrail tests fail on renamed
    headings, and `TestTopLevelCommandsHaveManualCoverage` fails when a new
    top-level command is neither mapped nor exempt.

## Session 4 — progressive delivery in the Go binary (CLI mode, 2026-07-06)

The progressive scheme now lives in the binary itself (`internal/manual`,
stderr hook in `internal/cmd/inject.go`): any agent running `hactl` through a
shell tool (both streams captured, no TTY) gets the core with its first
command and family how-tos on first family use, tracked per instance in
`cache/manual-state.json` (session = `HACTL_SESSION` or a 30-min-TTL shared
key). `tools.py` keeps doing its own injection and pins the child binary to
`HACTL_MANUAL_MODE=off`. CLI-mode eval harness: single passthrough
`integrations/llm/tools_cli.py`, selected via `HACTL_TOOLS_PY` in `hactl-llm`.

A/B (arm A = multi-function tools.py progressive, arm B = single-function
tools_cli.py + binary injection), e01–e12, qwen3.5-122b, 2026-07-06:

| run | arm | manual | PASS/12 | injected tok/prompt | turns | wall |
|---|---|---|---|---|---|---|
| 1911 | A | as-is | 11 | 2261 | 32 | 639s |
| 1928 | A | as-is | 11 | 2279 | 30 | 301s |
| 1921 | B | as-is | 7 | 2283 | 49 | 379s |
| 1933 | B | as-is | 6 (+1 CHECK) | 2452 | 36 | 689s |
| 1949 | B | +cmd index | 11 | 2554 | 42 | 379s |
| 1955 | B | +cmd index | 10 (+1 CHECK) | 2524 | 33 | 252s |
| 2000 | A | +cmd index | **12** | 2415 | 33 | 237s |

Findings (each confirmed on 2 runs unless noted):

1. **Vocabulary gap, the one real CLI-mode failure mode.** tools.py's ~20
   function *names* (hactl_helper_ls, hactl_tpl_eval, …) are an implicit
   command index in the tool schema. The bare passthrough has none, and
   progressive family docs only arrive after the first *correct* family
   command — chicken-and-egg. Baseline B failures were all of this shape:
   `ent ls --domain helper` instead of `helper ls` (e11), `template eval`
   (e12), `refs`/`ent refs`/`int ls` flailing (e09/e10) burning budget.
2. **Fix: "Full command set" index appended to `## Quick routing`**
   (~150 tok, core-injected, no heading/map changes). B jumps 7/6 → 11/10,
   on par with arm A; the A control run with the index scored 12/12
   (single run — lifetime best, grain of salt). The models self-corrected
   via `--help` and `rtfm` even without the index, just over budget.
3. **Token economics.** Tool-schema prefix per request: A ≈ 3286 tok,
   B ≈ 825 tok (first-turn input, median) — the passthrough saves ~2.5k
   tok of schema on every request, but rapid-mlx prefix-caches A's
   identical schema across conversations (~60% cached input vs ~25% for
   B), so total *uncached* input per run comes out comparable (A ≈ 58–66k,
   B ≈ 69–117k, noisy with turn count). Verdict: CLI mode ≈ wrapper mode
   on end-to-end tokens and quality, with a far smaller fixed schema and
   zero client-side wrapper code — any agent with a shell tool gets the
   tuned cold start. Injection overhead itself is unchanged
   (~2.3–2.6k tok/prompt; the eval re-pays the core every prompt because
   each prompt is a fresh HACTL_SESSION; a real conversation pays it once).
4. **Write discipline held in CLI mode:** no F4 in any of the 4 B runs;
   e08 proposed dry-runs and stopped for confirmation (CHECK by design).

Residual B non-passes are known-flaky prompts (e06 discovery, e10 budget),
not CLI-mode-specific. The command-index manual diff needs Jan's OK before
commit (Rule: manual changes are shown as diffs first).

## Open items

- ~~Decide default for the `llm` tools path~~ DECIDED 2026-07-06:
  progressive is the default (quality-neutral at -71% context cost);
  full-mode baseline runs now need explicit `HACTL_MANUAL_MODE=full`. The
  `hactl mcp` port (core with first tool result; family sections keyed on
  the first token of the `command` string) stays LOW PRIORITY per Jan —
  when it happens, `internal/manual` (Session 4) already has the
  sectioning/state pieces.
- e01: unchanged. Next idea after routing table did not confirm: put the
  sweep sequence in the *system* prompt for chat-style agents, or accept
  6-call budget.
- A `ref` family section does not exist in the manual (e09 passes on
  docstrings alone) — write one if ref grows beyond validate/scan.
- Variance dashboard (grade.py over N runs) got more urgent: run 25 shows
  single-run noise still dominates variant deltas at n=2.
