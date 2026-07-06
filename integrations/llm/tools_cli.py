"""Single hactl passthrough exposed as an llm tool (CLI-mode harness).

Unlike tools.py (one wrapper per command), this exposes ONE function that runs
any hactl command line — the shape an agent with a generic shell tool sees.
Manual delivery is the binary's job here: with stdout and stderr captured and
HACTL_SESSION set, hactl injects the progressive manual on stderr, ending with
the `=== RESULT of hactl … ===` marker. This wrapper returns stderr before
stdout, so the model reads manual → marker → output: the tuned layout.

The docstring of hactl() below is tuning-sensitive prompt surface — it mirrors
the MCP tool description in internal/mcpserver/server.go; keep them aligned.

Env:
  HACTL_BIN      hactl binary (default "hactl" on PATH)
  HACTL_DIR      instance directory (forwarded as --dir)
  HACTL_SESSION  manual-state session key; defaults to llm-<pid>-<time>, i.e.
                 a fresh session per llm process (= per eval prompt in run.sh)
"""

import os
import shlex
import subprocess
import time

HACTL_BIN = os.environ.get("HACTL_BIN", "hactl")
HACTL_DIR = os.environ.get("HACTL_DIR")
TIMEOUT_S = 120
SESSION = os.environ.get("HACTL_SESSION") or f"llm-{os.getpid()}-{int(time.time())}"
_MANUAL_MODE = os.environ.get("HACTL_MANUAL_MODE", "progressive")


def hactl(command: str) -> str:
    """Run a hactl command. hactl is a Home Assistant analysis and management CLI tuned for LLM use: output is plain text capped at ~500 tokens by default. Use --tokens to add a compact [~N tok] size header.

    Pass the command line without the binary name, e.g. 'ent ls --domain light' or 'auto show <id>'. Useful global flags: --json (structured output), --tokens (compact token estimate), --tokensmax N (raise/remove the output cap, 0 = uncapped), --since 7d, --top N, --full.

    The manual is delivered together with your first results — read it before interpreting anything; it documents every command, flag, and workflow. (Also available on demand via 'rtfm'.)

    Every mutating command (svc call, auto apply/create/delete, script apply/run, ...) is dry-run by default; repeat it with --confirm only after the user explicitly confirmed the exact action — the original request is not that confirmation.
    """
    try:
        args = shlex.split(command)
    except ValueError as e:
        return f"ERROR: cannot parse command: {e}"
    cmd = [HACTL_BIN]
    if HACTL_DIR:
        cmd += ["--dir", HACTL_DIR]
    cmd += args
    env = {**os.environ, "HACTL_SESSION": SESSION, "HACTL_LOG_LEVEL": "error"}
    try:
        result = subprocess.run(
            cmd, capture_output=True, text=True, timeout=TIMEOUT_S, env=env
        )
    except subprocess.TimeoutExpired:
        return f"ERROR: hactl {command} timed out after {TIMEOUT_S}s"
    out = ""
    if result.stderr.strip():
        out = result.stderr.rstrip() + "\n"
    out += result.stdout
    if result.returncode != 0:
        out += f"\n(exit {result.returncode})"
    return out


# With delivery disabled, tell the model to fetch the manual itself — the
# same description switch hactl mcp --no-manual-inject makes (server.go).
if _MANUAL_MODE == "off":
    hactl.__doc__ = hactl.__doc__.replace(
        "The manual is delivered together with your first results — read it "
        "before interpreting anything; it documents every command, flag, and "
        "workflow. (Also available on demand via 'rtfm'.)",
        "Start by running 'rtfm' once: it prints the full manual of all "
        "commands — read it before interpreting anything.",
    )
