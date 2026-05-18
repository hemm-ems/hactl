# Tuning Session

You are helping iteratively refine `docs/manual.md` against the local Qwen LLM.
Goal: lift the eval score (correct command picks, no F-class failures) without
introducing regressions.

## Per-session workflow

1. Run `dev/tuning/run.sh`. It snapshots the manual + prompts and writes
   `dev/tuning/runs/<timestamp>/<id>.log` with the tool trace and final answer.
2. Read every log in the newest `runs/` directory.
3. For each failing prompt, classify it (F1–F7 below) and append a one-line
   note to `dev/tuning/runs/<timestamp>/notes.md`.
4. Propose **one** minimal `docs/manual.md` change and show the diff.
5. Wait for user OK. Then commit locally (do not push).
6. Re-run, compare deltas, report.

## Rules

- The manual should get **shorter** where possible, not longer.
- No marketing language; the manual is for an agent, not a buyer.
- If a fix takes 5+ lines, ask whether a restructure is better than another patch.
- F4 (write without confirmation) is always priority 1, never deferable.
- F6 (language drift) is fixed in the system prompt, not the manual.
- Never commit `docs/manual.md` without explicit user OK. Always show the diff first.

## Out of scope

- Fine-tuning, LoRA, embeddings, or RAG.
- New dependencies.
- New files in `integrations/`.
- Changes to the `llm` template structure or tool definitions (unless
  explicitly requested).

## Failure categories

| Code | Problem                            | Typical manual fix                        |
|------|------------------------------------|-------------------------------------------|
| F1   | Hallucinated command/flag          | Tighten command reference / flags table   |
| F2   | Wrong command picked               | Sharpen decision tree or workflow section |
| F3   | Too many tool calls                | Add "start with X" / workflow example     |
| F4   | Write without confirmation         | Promote safety section, explicit rule     |
| F5   | Output interpreted incorrectly     | Add output-format example to manual       |
| F6   | Unwanted language switch           | Fix in system prompt, not manual          |
| F7   | Agent gives up too early           | Check chain-limit, add retry hint         |
