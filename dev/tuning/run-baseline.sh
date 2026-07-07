#!/usr/bin/env bash
#
# Baseline arm of the harness comparison: the tuned hactl-llm loop in
# CLI-passthrough mode (tools_cli.py) — the same command surface codex and
# kilo see, driven by the session-4 harness. Wraps run.sh with the exact
# session-4 environment so comparison runs are reproducible.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

source dev/tuning/qwen35.env
export HACTL_LLM_SYSTEM_FILE=dev/tuning/system-cold.md
export HACTL_TOOLS_PY=integrations/llm/tools_cli.py

exec ./dev/tuning/run.sh "$@"
