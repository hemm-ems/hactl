# Tuning Session

You are helping iteratively refine `docs/manual.md` against a local LLM.
Goal: lift the eval score (correct command picks, no F-class failures) without
introducing regressions.

## Setup (2026-07 config)

```bash
source dev/tuning/qwen35.env                       # rapid-mlx + live HA env
export HACTL_LLM_SYSTEM_FILE=dev/tuning/system-cold.md   # cold-start mode
./dev/tuning/run.sh                                # full eval (~7 min)
uv run dev/tuning/grade.py dev/tuning/runs/<ts>    # automated grading
```

Architecture: cold start. The manual is NOT in the system prompt; the tool
layer injects it with the first tool call's result (`integrations/llm/tools.py`).
Set `HACTL_NO_RTFM_GATE=1` to disable injection (manual-in-prompt mode).

## Per-session workflow

1. Re-anchor first: verify eval prompts still reference entities/automations
   that exist in the live instance; fix `prompts.yaml` before touching the manual.
2. Run the eval, grade it, read every FAIL/CHECK log in `runs/<ts>/`.
3. Classify failures (F1–F7 below), append notes to `patterns.md`.
4. Propose **one** minimal `docs/manual.md` change and show the diff.
5. Re-run. Variance flips 2–3 prompts per run: confirm any conclusion on
   at least 2 runs before logging it as an effect.
6. Wait for user OK before committing anything.

## Rules

- The manual should get **shorter** where possible, not longer.
- Behavioral content (rules, workflows) belongs at the TOP of the manual;
  the model skims the tail when the manual arrives mid-conversation.
- No comments inside workflow code blocks — they get executed as instructions.
- F4 (write without confirmation) is always priority 1, never deferable.
- F6 (language drift) is fixed in the system prompt, not the manual.
- Never commit `docs/manual.md` without explicit user OK. Always show the diff first.

## Out of scope

- Fine-tuning, LoRA, embeddings, or RAG.
- New dependencies.

## Failure categories

| Code | Problem                            | Typical manual fix                        |
|------|------------------------------------|-------------------------------------------|
| F1   | Hallucinated command/flag          | Tighten command reference / flags table   |
| F2   | Wrong command picked               | Sharpen decision tree or workflow section |
| F3   | Too many tool calls                | Add "start with X" / workflow example     |
| F4   | Write without confirmation        | Promote safety section, explicit rule     |
| F5   | Output interpreted incorrectly     | Add output-format example to manual       |
| F6   | Unwanted language switch           | Fix in system prompt, not manual          |
| F7   | Agent gives up too early           | Check chain-limit, add retry hint         |
