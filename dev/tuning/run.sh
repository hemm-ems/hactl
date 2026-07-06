#!/usr/bin/env bash
#
# Eval runner: snapshot the manual, rebuild the llm template, run every prompt
# in dev/tuning/prompts.yaml against the local LLM with tool execution, and
# write per-prompt logs into a timestamped runs/ directory.
#
# Output:
#   dev/tuning/runs/<timestamp>/
#     manual.md.snapshot     manual at run time
#     prompts.yaml.snapshot  prompt set at run time
#     <id>.log               raw llm output (tool traces + final answer)
#
# Requires: bash, uv (for inline python YAML parsing), llm (uv tool installed),
# the local LM Studio at HACTL_LLM_BASE_URL with the configured model loaded.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

RUN_DIR="dev/tuning/runs/$(date +%Y-%m-%d-%H%M)"
mkdir -p "$RUN_DIR"

cp docs/manual.md "$RUN_DIR/manual.md.snapshot"
cp dev/tuning/prompts.yaml "$RUN_DIR/prompts.yaml.snapshot"

# Recompile hactl so `hactl rtfm` returns the current docs/manual.md.
echo "→ building hactl..."
go build -o hactl ./cmd/hactl

# Rebuild the llm template against the current docs/manual.md.
./integrations/llm/install.sh >/dev/null

# Iterate prompts. Use uv to parse the YAML so we don't need yq on PATH.
uv run --with pyyaml python -c "
import yaml, sys
items = yaml.safe_load(open('dev/tuning/prompts.yaml'))
for x in items:
    print(f\"{x['id']}\t{x['prompt']}\")
" | while IFS=$'\t' read -r id prompt; do
    log_file="$RUN_DIR/$id.log"
    echo "=== $id: $prompt ==="
    {
      echo "=== $id"
      echo "prompt: $prompt"
      echo "---"
      ./integrations/llm/hactl-llm --td --no-stream --cl "${HACTL_LLM_CHAIN_LIMIT:-8}" "$prompt" </dev/null 2>&1 || echo "(llm exited non-zero)"
    } | tee "$log_file"
    echo
done

echo "→ $RUN_DIR"
