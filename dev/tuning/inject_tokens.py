# /// script
# requires-python = ">=3.11"
# ///
"""Measure manual-injection overhead in a tuning run directory.

Usage:
    uv run dev/tuning/inject_tokens.py dev/tuning/runs/<timestamp> [...]

Scans each <id>.log for injected manual blocks — lines from an
`[hactl manual ...` marker up to the `=== RESULT of hactl ...` marker —
and reports estimated tokens (chars/4) injected per prompt and per run.
Works for both delivery modes (full and progressive).
"""

import sys
from pathlib import Path


def measure_log(path: Path) -> tuple[int, int]:
    """Return (injected_token_estimate, n_injection_blocks)."""
    chars, blocks, capturing = 0, 0, False
    for line in path.read_text(errors="replace").splitlines():
        s = line.strip()
        if s.startswith("[hactl manual"):
            capturing = True
            blocks += 1
        elif capturing and s.startswith("=== RESULT of hactl"):
            capturing = False
            continue
        if capturing:
            chars += len(s) + 1
    return chars // 4, blocks


def main() -> int:
    for run_dir in map(Path, sys.argv[1:]):
        logs = sorted(run_dir.glob("e*.log"))
        if not logs:
            print(f"{run_dir}: no e*.log files")
            continue
        total = 0
        print(f"\n{run_dir.name}")
        print(f"{'id':<5} {'~tok injected':>14} {'blocks':>7}")
        for log in logs:
            tok, blocks = measure_log(log)
            total += tok
            print(f"{log.stem:<5} {tok:>14} {blocks:>7}")
        print(f"{'sum':<5} {total:>14}   (avg {total // len(logs)}/prompt)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
