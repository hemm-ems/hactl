# /// script
# requires-python = ">=3.11"
# dependencies = ["pyyaml"]
# ///
"""Rebuild the Tool-call ledger of a codex arm run from its transcripts.

The first codex runs (2026-07-06) lost ledger lines because the PATH shim's
append target lay outside the seatbelt's writable roots. The hactl commands
themselves are all in <id>.codex.txt as `/bin/zsh -lc '...' in /...` exec
lines, so the <id>.log files can be regenerated for grade.py:

    uv run dev/tuning/harness/recover-codex-ledger.py dev/tuning/runs/<dir>

Rewrites every <id>.log as header + recovered Tool-call lines + full codex
output. Idempotent.
"""

import re
import sys
from pathlib import Path

import yaml

# Codex quotes the command in its exec display only when it needs quoting;
# a bare single token (`/bin/zsh -lc hactl in /...`) appears unquoted.
EXEC_RE = re.compile(r"^/bin/zsh -lc (?:(['\"])(.*)\1|(\S+)) in /")
SPLIT_RE = re.compile(r"\|\||&&|;|\|")


def hactl_calls(inner: str) -> list[str]:
    """hactl arg-strings inside one shell command line, pipeline-aware."""
    calls = []
    for seg in SPLIT_RE.split(inner):
        seg = seg.strip()
        if seg == "hactl" or seg.startswith("hactl "):
            args = seg[len("hactl"):].strip()
            args = args.replace("2>&1", "").strip()
            args = args.replace('\\"', '"')
            # Same normalization as the shim: single-line, no double quotes.
            args = args.replace("\n", " ").replace('"', "'")
            calls.append(args)
    return calls


def main() -> int:
    run_dir = Path(sys.argv[1])
    prompts = {p["id"]: p["prompt"]
               for p in yaml.safe_load(
                   (run_dir / "prompts.yaml.snapshot").read_text())}
    for raw in sorted(run_dir.glob("e*.codex.txt")):
        pid = raw.name.split(".")[0]
        calls = []
        for line in raw.read_text(errors="replace").splitlines():
            m = EXEC_RE.match(line)
            if m:
                calls.extend(hactl_calls(m.group(2) or m.group(3) or ""))
        log = run_dir / f"{pid}.log"
        with log.open("w") as f:
            f.write(f"=== {pid}\nprompt: {prompts.get(pid, '?')}\n---\n")
            for c in calls:
                f.write(f'Tool call: hactl({{"command": "{c}"}})\n')
            f.write("--- codex output (recovered ledger) ---\n")
            f.write(raw.read_text(errors="replace"))
        print(f"{pid}: {len(calls)} calls recovered")
    return 0


if __name__ == "__main__":
    sys.exit(main())
