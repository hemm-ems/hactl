# Manual Tuning Loop

Iteratively refine `docs/manual.md` against the local Qwen LLM via Claude Code.

## Goal

`./integrations/llm/hactl-llm "<prompt>"` returns, on the eval set, a correct,
short diagnosis on ≥90% of runs — without hallucinations and within 4 tool
calls per prompt.

## What's in place

```
integrations/llm/
├── install.sh        # registers `hactl` template + LM Studio model alias
├── tools.py          # read-only hactl wrappers exposed as llm tools
└── hactl-llm         # entry point: cd's away from the repo (so `./hactl`
                     # binary doesn't shadow the template name) and runs
                     # `llm -t hactl --functions tools.py "$@"`

dev/tuning/
├── CLAUDE.md         # per-session Claude Code instructions
├── prompts.yaml      # eval prompts (e01–e08) + expected commands / call cap
├── run.sh            # eval runner: snapshots manual + prompts, executes all
                     # 8 prompts, writes runs/<ts>/<id>.log
├── patterns.md       # iteration log (what changed, why, what improved)
└── runs/             # per-iteration scratch (gitignored)
```

Backend: LM Studio at `http://192.168.42.119:1234`, Qwen3.6-27B
(`qwen3.6-27b-jang_4m-crack`, 41k ctx). All env defaults are in
`hactl-llm` and overridable via `HACTL_LLM_*` env vars.

## Per-iteration workflow

1. **Run:** `./dev/tuning/run.sh` — collects raw output and tool traces in
   `dev/tuning/runs/<timestamp>/<id>.log`.
2. **Triage:** classify each failing prompt (F1–F7 below).
3. **Hypothesise:** which manual change would fix it?
4. **Edit:** make the *minimal* change in `docs/manual.md` (one fix at a time,
   never multiple in the same iteration).
5. **Re-run:** same prompt set, fresh scores.
6. **Decide:** commit if ≥2 prompts improved and nothing regressed; otherwise
   revert.

One hypothesis per iteration. Otherwise we don't know what worked.

## Eval prompts

`dev/tuning/prompts.yaml` — kept in English (German adds noise for the local
Qwen — empirically reduced scores in iteration 0).

Add 1–2 prompts per iteration that mirror real-world questions. Don't replace
the originals; the score history matters.

## Failure categories

When triaging, label every failed answer with one of:

| Code | Problem                            | Typical manual fix                        |
|------|------------------------------------|-------------------------------------------|
| F1   | Hallucinated command/flag          | Tighten command reference / flags table   |
| F2   | Wrong command picked               | Sharpen decision tree or workflow section |
| F3   | Too many tool calls                | Add "start with X" / workflow example     |
| F4   | Write without confirmation         | Promote safety section, explicit rule     |
| F5   | Output interpreted incorrectly     | Add output-format example to manual       |
| F6   | Unwanted language switch           | Fix in system prompt, not manual          |
| F7   | Agent gives up too early           | Check chain-limit, add retry hint         |

## Patterns log (`dev/tuning/patterns.md`)

Append after each iteration:

```
## YYYY-MM-DD <iteration label>
- F<n> on e<id>: <what went wrong, one line>.
  Fix: <what changed in the manual, one line>.
  Effect: <next run delta>.
```

Becomes the playbook over time — useful as launch-time evidence ("here's how
the manual was tuned").

## Stop conditions

- 10 iterations with no measurable progress → reconsider the model (different
  Qwen variant? larger ctx?).
- Manual exceeds ~15 KB → restructure rather than keep adding.
- Same failure unfixable in 3 iterations → adjust hactl itself (clearer
  subcommand names, missing flags, etc.).

## Out of scope for this loop

- Benchmarking against other models (separate, later).
- UI / chat interface.
- Latency measurement.
- Integration tests across HA versions (already in CI).

One loop at a time.
