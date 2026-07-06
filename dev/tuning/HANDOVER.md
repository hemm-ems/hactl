# Handover — 2026-07-05 tuning session (branch `tuning/qwen35-cold-start`)

Supersedes the 2026-05-04 handover. Full run-by-run history in
`patterns.md`; durable rules in `docs/llm-tuning.md` (session-2 section).

> **Session-3 addendum (2026-07-06):** progressive manual delivery
> (`HACTL_MANUAL_MODE=progressive` in tools.py) validated over 6 runs:
> quality on par with full injection except the historically unsolved e01,
> at ~2.1k instead of ~7.2k injected tokens per prompt. Details in
> `docs/llm-tuning.md` session-3 section and `patterns.md` runs 20–25.
> Progressive is now the DEFAULT for the llm tools path (Jan approved);
> full-mode baseline runs need explicit `HACTL_MANUAL_MODE=full`. The
> `hactl mcp` port is low priority — Jan prefers the llm-CLI path.

## State

- Branch `tuning/qwen35-cold-start`, NOT pushed (local only by request).
- Eval: **12/12 PASS** on the extended prompt set (run 18); diet
  regression run 19: 10/12 + 1 behaviorally-correct CHECK. The 8-prompt
  set held 7/8 across three consecutive runs (14–16). May best was 4/8
  with the manual in the system prompt. Zero unconfirmed writes since the
  run-12 F4 fix.
- Live-instance artifact from the run-12 F4 incident: dashboard
  `energy-dash` — Jan deletes it with
  `hactl dash delete energy-dash --confirm`.

## The setup (works, keep)

```bash
cd ~/dev/repos/hactl-dev/hactl
source dev/tuning/qwen35.env                # rapid-mlx + jansHA instance
export HACTL_LLM_SYSTEM_FILE=dev/tuning/system-cold.md
./dev/tuning/run.sh                         # 8 prompts, ~7 min
uv run dev/tuning/grade.py "$(ls -td dev/tuning/runs/*/ | head -1)"
```

Model: qwen3.5-122b-mxfp4, rapid-mlx (192.168.42.114:8000/v1, key in
~/.rapid-mlx-api-key), ~5 s/tool-turn. Winning architecture: **cold start +
manual auto-injected with the first tool call's result** (tools.py).

## What this session established

1. Delivery beats content: injection > prompt-rule > hard gate (runs 3–5).
2. Behavior first: workflows moved to the manual top; model skims the tail.
3. Code blocks are executed, comments ignored — `--confirm` inside the
   dashboard workflow block caused a real write (run 12). Dry-run forms in
   blocks; protocol in prose; "the original request is not confirmation".
4. Safety text strength: tool response > system prompt > manual.
5. Tool signatures follow model instincts (dict for JSON; a str param
   400'd the chain on the schema-enforcing server).
6. Errors must teach the next step (token-cap hint, hyphen rule both worked).
7. Judge on repeated runs; single-run deltas are stochastic noise.

## Session part 3 (all six queue items done)

1. 7/8 confirmed across runs 14–16 (e01 sole stable fail at that point).
2. **Routing table** at the manual top — e01 passed runs 18 AND 19 after
   17 straight fails. n=2, promising, keep watching.
3. **`hactl mcp` manual injection** (feat commit, tested): first tool
   result of a session carries the manual; `--no-manual-inject` opts out.
4. **`svc call` CLI dry-run gate** (BREAKING): bare `svc call` prints the
   plan; `--confirm` executes. Three call-path tests updated.
5. **e09–e12** eval prompts + wrappers (ref validate/scan, helper ls,
   config entries): all four pass at exactly 1 call.
6. **Manual diet**: human setup/troubleshooting → docs/setup.md (README
   linked); manual's Setup is now a 4-line agent stub.

## Suggested improvements (beyond this session)

- **e01 remains unsolved** (never passed): sweep-completion vs the model's
  drill-down instinct. Routing table at manual top is the next hypothesis.
- **Variance dashboard**: grade.py could aggregate N runs (pass-rate per
  prompt) instead of single-run tables — the data is all in runs/.
- **CI regression eval**: nightly run.sh + grade.py --json against a
  recorded fixture instance (testcontainers HA?) would catch manual/eval
  drift without the live instance.
- **Confirmation UX**: the eval proves models ask correctly, but nothing
  verifies the *user's* answer reaches confirm=True truthfully. For MCP,
  elicitation support (MCP spec) would move confirmation to the client.
- **Model watch**: rapid-mlx model slots change quarterly; re-baseline when
  the slug changes (qwen35.env pins the current one).
