#!/usr/bin/env bash
#
# Install the `hactl` template for Simon Willison's `llm` CLI, pointed at a
# local LM Studio (or any OpenAI-compatible endpoint).
#
# Idempotent: re-running rebuilds the template with the current docs/manual.md
# and rewrites the LM Studio model entry in extra-openai-models.yaml.
#
# Env overrides:
#   HACTL_LLM_BASE_URL  default http://192.168.42.119:1234/v1
#   HACTL_LLM_MODEL     default qwen3.6-27b-jang_4m-crack
#   HACTL_LLM_ALIAS     default hactl-qwen
#   HACTL_LLM_API_KEY   default lm-studio (LM Studio ignores auth; set a real
#                       key for authenticated endpoints like rapid-mlx)
#   HACTL_LLM_SYSTEM_FILE  optional path: use this file as the system prompt
#                       instead of docs/manual.md (cold-start mode — the
#                       agent must self-teach via `hactl rtfm`)
#
# Usage: ./integrations/llm/install.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
MANUAL="$REPO_ROOT/docs/manual.md"

BASE_URL="${HACTL_LLM_BASE_URL:-http://192.168.42.119:1234/v1}"
MODEL_NAME="${HACTL_LLM_MODEL:-qwen3.6-27b-jang_4m-crack}"
ALIAS="${HACTL_LLM_ALIAS:-hactl-qwen}"
API_KEY="${HACTL_LLM_API_KEY:-lm-studio}"
KEY_NAME="lmstudio-dummy"

if ! command -v llm >/dev/null 2>&1; then
  echo "error: 'llm' CLI not found. Install with: uv tool install llm" >&2
  exit 1
fi

if [ ! -f "$MANUAL" ]; then
  echo "error: manual not found at $MANUAL" >&2
  exit 1
fi

TEMPLATES_DIR="$(llm templates path)"
LLM_DIR="$(dirname "$TEMPLATES_DIR")"
EXTRA_MODELS="$LLM_DIR/extra-openai-models.yaml"
TEMPLATE_FILE="$TEMPLATES_DIR/hactl.yaml"

mkdir -p "$TEMPLATES_DIR"

# Rewrite the LM Studio entry in extra-openai-models.yaml.
# We own the file managed by this installer; if you have other custom models,
# add them to a separate file or merge manually after running this script.
cat > "$EXTRA_MODELS" <<EOF
- model_id: $ALIAS
  model_name: $MODEL_NAME
  api_base: $BASE_URL
  api_key_name: $KEY_NAME
  supports_tools: true
EOF

# LM Studio doesn't enforce auth, but the OpenAI client requires *some* value.
llm keys set "$KEY_NAME" --value "$API_KEY" >/dev/null

# Build the template: model alias + system prompt (manual.md by default,
# or HACTL_LLM_SYSTEM_FILE for cold-start evals where the agent rtfm's).
SYSTEM_SRC="${HACTL_LLM_SYSTEM_FILE:-$MANUAL}"
if [ ! -f "$SYSTEM_SRC" ]; then
  echo "error: system prompt file not found at $SYSTEM_SRC" >&2
  exit 1
fi
{
  echo "model: $ALIAS"
  echo "system: |"
  sed 's/^/  /' "$SYSTEM_SRC"
} > "$TEMPLATE_FILE"

echo "✓ extra-openai-models.yaml → $EXTRA_MODELS"
echo "✓ template 'hactl' → $TEMPLATE_FILE"
echo "✓ alias '$ALIAS' → $BASE_URL ($MODEL_NAME)"
echo
echo "Smoke test:"
echo "  llm -t hactl --no-stream 'in einem satz: was macht hactl?'"
