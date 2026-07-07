#!/usr/bin/env bash
#
# Codex-CLI arm of the harness comparison: run the eval prompts from
# dev/tuning/prompts.yaml through `codex exec --profile local` (rapid-mlx
# backend, dirigent Route A) instead of the hactl-llm loop. hactl calls are
# captured by the PATH shim in dev/tuning/harness/bin/hactl, so the run dir
# is graded by the unchanged grade.py:
#
#   ./dev/tuning/run-codex.sh [prompt-id]     # id filter for smoke tests
#   uv run dev/tuning/grade.py dev/tuning/runs/<ts>-codex
#
# Output per prompt:
#   <id>.log        Tool-call ledger + codex final output (grade.py input)
#   <id>.codex.txt  full raw codex exec output (tokens-used line at the end)
#
# Requires: codex CLI with the `local` profile (~/.codex/local.config.toml),
# ~/.rapid-mlx-api-key, go toolchain, uv.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

FILTER="${1:-}"

RUN_DIR="$REPO_ROOT/dev/tuning/runs/$(date +%Y-%m-%d-%H%M)-codex"
mkdir -p "$RUN_DIR"
cp dev/tuning/prompts.yaml "$RUN_DIR/prompts.yaml.snapshot"
cp docs/manual.md "$RUN_DIR/manual.md.snapshot"
cp dev/tuning/harness/brief.md "$RUN_DIR/brief.snapshot"

echo "→ building hactl..."
go build -o hactl ./cmd/hactl

export HACTL_REAL="$REPO_ROOT/hactl"
export HACTL_DIR="${HACTL_DIR:-$HOME/dev/repos/hactl-dev/jansHA}"
export PATH="$REPO_ROOT/dev/tuning/harness/bin:$PATH"
export RAPID_MLX_API_KEY="$(cat "$HOME/.rapid-mlx-api-key")"

# Empty scratch workspace: nothing to explore, so the only route to the
# instance is the hactl CLI — same footing as the hactl-llm arm.
WORKSPACE="$(mktemp -d "${TMPDIR:-/tmp}/codex-arm.XXXXXX")"
trap 'rm -rf "$WORKSPACE"' EXIT

# workspace-write keeps codex's own sandbox in play (part of the harness
# bundle under test), but hactl needs the HA network and its instance cache:
SANDBOX_ARGS=(
  -s workspace-write
  --skip-git-repo-check
  -c 'sandbox_workspace_write.network_access=true'
  -c "sandbox_workspace_write.writable_roots=[\"$HACTL_DIR\"]"
)

TIMEOUT_CMD=()
if command -v gtimeout >/dev/null; then TIMEOUT_CMD=(gtimeout 480)
elif command -v timeout >/dev/null; then TIMEOUT_CMD=(timeout 480); fi

BRIEF_TEMPLATE="$(cat dev/tuning/harness/brief.md)"

uv run --with pyyaml python -c "
import yaml
for x in yaml.safe_load(open('dev/tuning/prompts.yaml')):
    print(f\"{x['id']}\t{x['prompt']}\")
" | while IFS=$'\t' read -r id prompt; do
    [[ -n "$FILTER" && "$id" != "$FILTER" ]] && continue
    log_file="$RUN_DIR/$id.log"
    raw_file="$RUN_DIR/$id.codex.txt"
    export HACTL_SESSION="$(basename "$RUN_DIR")-$id"
    # Ledger must live inside a seatbelt-writable root (the workspace):
    # appends to $RUN_DIR are denied for sandboxed commands ("Operation
    # not permitted"), which silently loses ledger lines. Merged below.
    export HACTL_CALL_LOG="$WORKSPACE/.ledger.log"
    rm -f "$HACTL_CALL_LOG"

    {
      echo "=== $id"
      echo "prompt: $prompt"
      echo "---"
    } > "$log_file"

    brief="${BRIEF_TEMPLATE//\{\{PROMPT\}\}/$prompt}"
    echo "=== $id: $prompt ==="
    start=$SECONDS
    (cd "$WORKSPACE" && ${TIMEOUT_CMD[@]+"${TIMEOUT_CMD[@]}"} codex exec --profile local \
        "${SANDBOX_ARGS[@]}" "$brief" </dev/null) > "$raw_file" 2>&1 \
      || echo "(codex exited non-zero: $?)" >> "$raw_file"
    duration=$((SECONDS - start))

    # Merge ledger, then codex output (final message + tokens-used) for
    # grade.py's answer_tail.
    {
      cat "$HACTL_CALL_LOG" 2>/dev/null || true
      echo "--- codex output (${duration}s) ---"
      cat "$raw_file"
    } >> "$log_file"
    echo "    ${duration}s, calls: $(grep -c '^Tool call:' "$log_file" || true)"
done

echo "→ $RUN_DIR"
