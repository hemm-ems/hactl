#!/usr/bin/env bash
#
# Kilo-CLI arm of the harness comparison: run the eval prompts from
# dev/tuning/prompts.yaml through `kilo run --auto` (@kilocode/cli) against
# the same rapid-mlx backend as the hactl-llm and codex arms. hactl calls
# are captured by the PATH shim in dev/tuning/harness/bin/hactl, so the run
# dir is graded by the unchanged grade.py:
#
#   ./dev/tuning/run-kilo.sh [prompt-id]      # id filter for smoke tests
#   uv run dev/tuning/grade.py dev/tuning/runs/<ts>-kilo
#
# Output per prompt:
#   <id>.log       Tool-call ledger + kilo final output (grade.py input)
#   <id>.kilo.txt  full raw kilo output
#
# Requires: @kilocode/cli (npm -g), ~/.rapid-mlx-api-key, go toolchain, uv.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

FILTER="${1:-}"

RUN_DIR="$REPO_ROOT/dev/tuning/runs/$(date +%Y-%m-%d-%H%M)-kilo"
mkdir -p "$RUN_DIR"
cp dev/tuning/prompts.yaml "$RUN_DIR/prompts.yaml.snapshot"
cp docs/manual.md "$RUN_DIR/manual.md.snapshot"
cp dev/tuning/harness/brief.md "$RUN_DIR/brief.snapshot"
kilo --version > "$RUN_DIR/kilo.version" 2>&1 || true

echo "→ building hactl..."
go build -o hactl ./cmd/hactl

export HACTL_REAL="$REPO_ROOT/hactl"
export HACTL_DIR="${HACTL_DIR:-$HOME/dev/repos/hactl-dev/jansHA}"
export PATH="$REPO_ROOT/dev/tuning/harness/bin:$PATH"
export RAPID_MLX_API_KEY="$(cat "$HOME/.rapid-mlx-api-key")"

# Empty scratch workspace (plus the project-level kilo.jsonc): nothing to
# explore, so the only route to the instance is the hactl CLI — same
# footing as the other arms. Kilo has no OS sandbox; the safety net is
# hactl's dry-run-by-default writes plus the F4 grading rule.
WORKSPACE="$(mktemp -d "${TMPDIR:-/tmp}/kilo-arm.XXXXXX")"
trap 'rm -rf "$WORKSPACE"' EXIT
cp dev/tuning/harness/kilo.jsonc "$WORKSPACE/kilo.jsonc"

BRIEF_TEMPLATE="$(cat dev/tuning/harness/brief.md)"

uv run --with pyyaml python -c "
import yaml
for x in yaml.safe_load(open('dev/tuning/prompts.yaml')):
    print(f\"{x['id']}\t{x['prompt']}\")
" | while IFS=$'\t' read -r id prompt; do
    [[ -n "$FILTER" && "$id" != "$FILTER" ]] && continue
    log_file="$RUN_DIR/$id.log"
    raw_file="$RUN_DIR/$id.kilo.txt"
    export HACTL_SESSION="$(basename "$RUN_DIR")-$id"
    export HACTL_CALL_LOG="$log_file"

    {
      echo "=== $id"
      echo "prompt: $prompt"
      echo "---"
    } > "$log_file"

    brief="${BRIEF_TEMPLATE//\{\{PROMPT\}\}/$prompt}"
    echo "=== $id: $prompt ==="
    start=$SECONDS
    # --auto (approve-all; Jan authorized 2026-07-06): kilo 7.4.1 headless
    # consults no allow rules — without the flag every bash call is
    # auto-rejected. Containment: empty scratch workspace, hactl-only
    # brief, hactl's dry-run-by-default writes, local model + local HA.
    (cd "$WORKSPACE" && kilo run --auto -m rapid-mlx/qwen3.5-122b-mxfp4 \
        "$brief" </dev/null) > "$raw_file" 2>&1 \
      || echo "(kilo exited non-zero: $?)" >> "$raw_file"
    duration=$((SECONDS - start))

    {
      echo "--- kilo output (${duration}s) ---"
      cat "$raw_file"
    } >> "$log_file"
    echo "    ${duration}s, calls: $(grep -c '^Tool call:' "$log_file" || true)"
done

echo "→ $RUN_DIR"
