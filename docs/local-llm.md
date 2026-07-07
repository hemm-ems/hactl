# Running hactl with a local LLM

How to drive hactl with a model that runs on your own hardware — what works,
what it costs, and where the limits are. Everything here is backed by the
eval loop in `dev/tuning/` (12 operations prompts against a live HA instance,
graded on correct commands, call budget, and write safety); methodology and
run history live in [llm-tuning.md](llm-tuning.md).

## What to expect

With the validated setup below and a ~120B-class local model, hactl-class
operations tasks ("what broke tonight?", "is this sensor stuck?", "disable
that automation") complete in ~30 s at 2–5 hactl calls each, with pass rates
around 10–12 of 12 on the eval set. Three task shapes account for nearly all
residual failures:

- **Read/diagnose/lookup** — reliable. This is the productive core today.
- **Writes** — safe but interactive: every mutating command is dry-run by
  default, so the worst case of a model mistake is a printed plan, not a
  changed instance. Expect to confirm each write.
- **Broad sweeps under a tight call budget** — the weak spot. Local models
  drill into the first interesting finding instead of finishing the sweep.

## Requirements

- **Server:** any OpenAI-compatible endpoint (LM Studio, mlx server, vLLM,
  llama.cpp server) with **tool calling** enabled. hactl is tuned against
  qwen-class instruction models; ~5 s per tool turn is a comfortable pace.
- **Model:** must handle multi-turn tool chains and JSON arguments. The
  reference setup is Qwen3.5-122B (mxfp4) — large, but the architecture
  (below) is model-agnostic; smaller models mainly lose sweep discipline
  and vocabulary precision, not the basic loop.
- **Context:** the progressive manual costs ~2.5k tokens per task on top of
  your harness's own prompt. 32k context is plenty.

## Quickstart (llm CLI — the validated path)

The bundled integration uses Simon Willison's [`llm`](https://llm.datasette.io)
CLI. One-time setup:

```bash
export HACTL_LLM_BASE_URL="http://<your-server>:8000/v1"
export HACTL_LLM_MODEL="<model-id>"
export HACTL_LLM_API_KEY="<key, if your server checks one>"
./integrations/llm/install.sh          # builds the `hactl` llm template
```

Then:

```bash
./integrations/llm/hactl-llm "what went wrong in the last 24h?"
./integrations/llm/hactl-llm --td "is sensor.wp_vl behaving normally?"   # show tool traces
```

`hactl-llm` wires the model to hactl either through per-command wrappers
(`tools.py`, default) or through a single shell-style passthrough
(`HACTL_TOOLS_PY=integrations/llm/tools_cli.py`) — the latter is what a
generic agent with a shell tool sees, and it works nearly as well at a
third of the tool-schema tokens. Keep a **chain limit** (`--cl 8`): a local
model that loses the plot burns turns fast, and eight calls cover every
eval task with room to spare.

## How the model learns hactl (progressive manual delivery)

You do not need to paste the manual into a system prompt — measured, that is
the *worst* delivery mode. When hactl detects an agent-shaped caller (both
stdout and stderr captured), it delivers [the manual](manual.md) on stderr,
progressively:

- the **core** (routing table, conventions, flags, ~1.4k tokens) arrives
  with the result of the session's first hactl command;
- each command family's how-to arrives with the first command of that
  family;
- a `=== RESULT of hactl … ===` marker separates manual from output, and
  stdout stays byte-identical for pipes and `--json`.

Sessions are per instance, keyed by `HACTL_SESSION` (default: shared key,
30-minute idle timeout). `HACTL_MANUAL_MODE` switches modes: `progressive`
(default) | `full` | `off`. Measured across the eval set, progressive
matches full injection on quality at roughly a third of the injected tokens
and none of the measured safety regressions; `off` (model must call `rtfm`
itself) loses about half the prompts — models rarely obey a "read the
manual first" instruction.

## Safety model

Layered, and all of it binary-level — it holds no matter which harness or
model you use:

1. **Dry-run by default.** Every mutating command (`svc call`, `auto
   apply/create/delete`, `dash save`, …) prints a plan; only repeating the
   command with `--confirm` executes it.
2. **First-contact confirm guard.** An agent-shaped caller that fires
   `--confirm` as its *first* command of a family in a session is refused
   before execution, and the family's how-to is delivered with the refusal
   — the retry is an informed one. (This closes the one measured failure
   where a model jumped straight to `--confirm` before the relevant manual
   section had arrived.) A proper dry-run → confirm sequence never
   triggers it.
3. **Errors teach.** Misses respond with the correct next step (e.g.
   `ent ls --domain helper` explains that helpers are listed with
   `helper ls`) instead of an empty table a model would misread as "none
   exist".
4. **MCP is read-only by default.** `hactl mcp` rejects mutating commands
   unless started with `--allow-writes`.

The confirmation is only as truthful as the harness: hactl cannot verify
that a human actually approved the plan the model presented. Keep writes
interactive; don't run a local model against your instance unattended with
`HACTL_MANUAL_MODE=off`.

## Scripts and pipelines (not LLMs)

A shell script that pipes hactl output looks agent-shaped too. Scripts that
perform writes should set `HACTL_MANUAL_MODE=off` — this silences manual
delivery *and* the confirm guard, restoring plain CLI semantics for
`--confirm`.

## Which harness?

Measured head-to-head (same model, prompts, instance, grader —
`dev/tuning/HARNESS-COMPARISON.md`):

| Harness | PASS/12 (2 runs) | Wall clock/run | Notes |
|---|---|---|---|
| tuned llm-CLI loop (above) | 9–10 | ~6 min | best budget discipline |
| Kilo CLI (`--auto`) | 7–9 | ~17–22 min | explores more, verifies more |
| Codex CLI (`codex exec`) | 6–6 | ~11–18 min | coding persona over-probes |

Coding agents *work* — the progressive manual reaches them unmodified — but
their explore-first instincts fight the frugality that ops tasks want. For
interactive HA operations, the plain llm-CLI loop (or any thin harness with
a shell tool and a chain limit) is the sweet spot: highest accuracy,
lowest tokens, fastest answers. Save the heavyweight coding agents for
coding.

## Tuning it further

The eval harness is in the repo: `dev/tuning/run.sh` runs the prompt set
against your endpoint, `dev/tuning/grade.py <run-dir>` grades a run, and
passing several run dirs prints per-prompt pass rates across runs (verdicts
on a single run are noise — judge on at least two). Add prompts that match
your instance's real workload to `dev/tuning/prompts.yaml`, and read
[llm-tuning.md](llm-tuning.md) before changing `docs/manual.md`: delivery
beats content, and every durable rule in that file was measured, not
assumed.
