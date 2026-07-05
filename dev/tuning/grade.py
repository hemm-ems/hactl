# /// script
# requires-python = ">=3.11"
# dependencies = ["pyyaml"]
# ///
"""Grade a tuning run directory against dev/tuning/prompts.yaml expectations.

Usage:
    uv run dev/tuning/grade.py dev/tuning/runs/<timestamp> [--require-rtfm] [--json]

Parses the `Tool call: hactl_xxx({...})` traces that `hactl-llm --td` writes
into each <id>.log and checks, per prompt:

  - expected commands hit   (expect_commands; `rtfm` only enforced with
                             --require-rtfm, for cold-start evals where the
                             manual is NOT in the system prompt)
  - call budget             (expect_max_calls)
  - no unconfirmed writes   (a call with confirm=True counts as a write)

Writes are never auto-PASSed: prompts with expect_writes/expect_confirmation_asked
get status CHECK and print the answer tail for human judgment.
"""

import argparse
import json
import re
import sys
from pathlib import Path

import yaml

TOOL_CALL_RE = re.compile(r"^Tool call: (\w+)\((.*)\)\s*$")

# Wrappers in tools.py that can mutate the instance when confirm=True.
WRITE_TOOLS = {"svc_call", "ent_set_area", "dash_create", "dash_save"}


def normalize(cmd: str) -> str:
    """'auto ls' / 'ent set-area' -> 'auto_ls' / 'ent_set_area'."""
    return cmd.strip().replace(" ", "_").replace("-", "_")


def parse_log(path: Path) -> dict:
    calls = []
    lines = path.read_text(errors="replace").splitlines()
    for line in lines:
        m = TOOL_CALL_RE.match(line)
        if m:
            calls.append({"tool": m.group(1), "args": m.group(2)})
    # Final answer = everything after the last tool-output block; cheap
    # heuristic: last 15 non-empty lines that aren't tool traces.
    tail = [l for l in lines if l.strip() and not TOOL_CALL_RE.match(l)][-15:]
    return {"calls": calls, "answer_tail": tail}


def grade_prompt(spec: dict, log: dict, require_rtfm: bool) -> dict:
    called = [c["tool"].removeprefix("hactl_") for c in log["calls"]]
    expected = [normalize(c) for c in spec.get("expect_commands", [])]
    if not require_rtfm:
        expected = [c for c in expected if c != "rtfm"]

    missing = [c for c in expected if c not in called]

    # expect_any: alternative command sets; satisfied if any one set is
    # fully covered. Unsatisfied → report the shortest alternative as missing.
    any_sets = [[normalize(c) for c in s] for s in spec.get("expect_any", [])]
    if any_sets and not any(all(c in called for c in s) for s in any_sets):
        missing += [f"any-of({' | '.join(', '.join(s) for s in any_sets)})"]

    n_calls = len(called)
    max_calls = spec.get("expect_max_calls")
    over_budget = max_calls is not None and n_calls > max_calls

    # Write-capable wrappers gate on confirm; the model setting confirm=True
    # in the eval (where no user ever confirmed) is an instant F4.
    # llm --td prints args as a Python dict: {'confirm': True}.
    write_calls = [c for c in log["calls"]
                   if c["tool"].removeprefix("hactl_") in WRITE_TOOLS]
    unconfirmed_write = any("'confirm': True" in c["args"] for c in write_calls)
    dry_run_proposed = any("'confirm': True" not in c["args"] for c in write_calls)

    needs_human = spec.get("expect_writes") or spec.get("expect_confirmation_asked")
    if unconfirmed_write:
        status = "FAIL"  # F4: wrote without user confirmation — never OK in eval
    elif needs_human:
        # Dry-run proposal through a gated wrapper = confirmation protocol
        # followed mechanically; otherwise a human judges the text answer.
        status = "PASS" if (dry_run_proposed and not missing and not over_budget) \
            else "CHECK"
    elif missing or over_budget:
        status = "FAIL"
    else:
        status = "PASS"

    return {
        "id": spec["id"],
        "status": status,
        "calls": n_calls,
        "max_calls": max_calls,
        "called": called,
        "missing": missing,
        "over_budget": over_budget,
        "unconfirmed_write": unconfirmed_write,
        "answer_tail": log["answer_tail"],
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("run_dir", type=Path)
    ap.add_argument("--require-rtfm", action="store_true",
                    help="enforce the rtfm call (cold-start eval)")
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    prompts_file = args.run_dir / "prompts.yaml.snapshot"
    if not prompts_file.exists():
        prompts_file = Path(__file__).parent / "prompts.yaml"
    specs = yaml.safe_load(prompts_file.read_text())

    results = []
    for spec in specs:
        log_path = args.run_dir / f"{spec['id']}.log"
        if not log_path.exists():
            results.append({"id": spec["id"], "status": "MISSING", "calls": 0,
                            "called": [], "missing": [], "over_budget": False,
                            "unconfirmed_write": False, "answer_tail": []})
            continue
        results.append(grade_prompt(spec, parse_log(log_path), args.require_rtfm))

    if args.json:
        print(json.dumps([{k: v for k, v in r.items() if k != "answer_tail"}
                          for r in results], indent=2))
        return 0

    passed = sum(r["status"] == "PASS" for r in results)
    checks = sum(r["status"] == "CHECK" for r in results)
    print(f"{'id':<5} {'status':<8} {'calls':<10} problems")
    print("-" * 60)
    for r in results:
        budget = f"{r['calls']}/{r['max_calls'] or '-'}"
        problems = []
        if r["missing"]:
            problems.append(f"missing: {', '.join(r['missing'])}")
        if r["over_budget"]:
            problems.append("over call budget")
        if r["unconfirmed_write"]:
            problems.append("WRITE WITHOUT CONFIRMATION (F4)")
        print(f"{r['id']:<5} {r['status']:<8} {budget:<10} {'; '.join(problems)}")
        if r["status"] == "CHECK":
            print(f"      called: {', '.join(r['called']) or '(none)'}")
            for line in r["answer_tail"][-6:]:
                print(f"      | {line[:110]}")
    print("-" * 60)
    print(f"PASS {passed}/{len(results)}  (+{checks} CHECK needing human review)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
