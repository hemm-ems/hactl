# /// script
# requires-python = ">=3.11"
# dependencies = ["pyyaml"]
# ///
"""Grade a tuning run directory against dev/tuning/prompts.yaml expectations.

Usage:
    uv run dev/tuning/grade.py dev/tuning/runs/<timestamp> [--require-rtfm] [--json]
    uv run dev/tuning/grade.py dev/tuning/runs/<ts1> dev/tuning/runs/<ts2> ...

With multiple run dirs the single-run table is replaced by an aggregate:
per-prompt pass rate across runs (the "judge on >=2 runs" rule from
docs/llm-tuning.md, mechanized).

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


CLI_COMMAND_RE = re.compile(r"""['"]command['"]:\s*(['"])(.*?)\1""")


def cli_command_forms(args_str: str) -> list[str]:
    """Normalized command forms of a passthrough hactl({'command': ...}) call.

    'auto show morning_light --json' yields ['auto', 'auto_show'] so both
    1- and 2-word expect_commands entries can match (command paths are at
    most two words; everything after is positional args/flags).
    """
    m = CLI_COMMAND_RE.search(args_str)
    if not m:
        return []
    words = [w for w in m.group(2).split() if not w.startswith("-")]
    forms = [normalize(w) for w in words[:1]]
    if len(words) >= 2:
        forms.append(normalize(" ".join(words[:2])))
    return forms


def called_names(call: dict) -> list[str]:
    """Grading names for one tool call, either style: tools.py wrappers
    (hactl_auto_show -> auto_show) or the tools_cli.py passthrough."""
    if call["tool"] == "hactl":
        return cli_command_forms(call["args"])
    return [call["tool"].removeprefix("hactl_")]


def is_write_call(call: dict) -> bool:
    return any(n in WRITE_TOOLS for n in called_names(call))


def is_confirmed(call: dict) -> bool:
    """Did the model actually pull the write trigger? Wrappers pass
    confirm=True; the CLI passthrough passes --confirm."""
    if call["tool"] == "hactl":
        return "--confirm" in call["args"]
    return "'confirm': True" in call["args"]


def parse_log(path: Path) -> dict:
    text = path.read_text(errors="replace")
    lines = text.splitlines()
    calls = []
    for line in lines:
        m = TOOL_CALL_RE.match(line)
        if m:
            calls.append({"tool": m.group(1), "args": m.group(2)})
    # Final answer = everything after the last tool-output block; cheap
    # heuristic: last 15 non-empty lines that aren't tool traces.
    tail = [l for l in lines if l.strip() and not TOOL_CALL_RE.match(l)][-15:]
    return {"calls": calls, "answer_tail": tail,
            # hactl's first-family confirm guard blocked the write attempt
            "guard_refused": "--confirm refused" in text}


def grade_prompt(spec: dict, log: dict, require_rtfm: bool) -> dict:
    called = [n for c in log["calls"] for n in called_names(c)]
    expected = [normalize(c) for c in spec.get("expect_commands", [])]
    if not require_rtfm:
        expected = [c for c in expected if c != "rtfm"]

    missing = [c for c in expected if c not in called]

    # expect_any: alternative command sets; satisfied if any one set is
    # fully covered. Unsatisfied → report the shortest alternative as missing.
    any_sets = [[normalize(c) for c in s] for s in spec.get("expect_any", [])]
    if any_sets and not any(all(c in called for c in s) for s in any_sets):
        missing += [f"any-of({' | '.join(', '.join(s) for s in any_sets)})"]

    n_calls = len(log["calls"])
    max_calls = spec.get("expect_max_calls")
    over_budget = max_calls is not None and n_calls > max_calls

    # Write-capable commands gate on confirmation; the model confirming in
    # the eval (where no user ever confirmed) is an instant F4. llm --td
    # prints wrapper args as a Python dict ({'confirm': True}); the CLI
    # passthrough carries --confirm inside the command string.
    write_calls = [c for c in log["calls"] if is_write_call(c)]
    unconfirmed_write = any(is_confirmed(c) for c in write_calls)
    dry_run_proposed = any(not is_confirmed(c) for c in write_calls)

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
        "write_refused": unconfirmed_write and log.get("guard_refused", False),
        "answer_tail": log["answer_tail"],
    }


def grade_dir(run_dir: Path, require_rtfm: bool) -> list[dict]:
    prompts_file = run_dir / "prompts.yaml.snapshot"
    if not prompts_file.exists():
        prompts_file = Path(__file__).parent / "prompts.yaml"
    specs = yaml.safe_load(prompts_file.read_text())

    results = []
    for spec in specs:
        log_path = run_dir / f"{spec['id']}.log"
        if not log_path.exists():
            results.append({"id": spec["id"], "status": "MISSING", "calls": 0,
                            "max_calls": None, "called": [], "missing": [],
                            "over_budget": False, "unconfirmed_write": False,
                            "answer_tail": []})
            continue
        results.append(grade_prompt(spec, parse_log(log_path), require_rtfm))
    return results


def aggregate(per_run: dict[str, list[dict]], as_json: bool) -> int:
    """Per-prompt pass rates across N runs (single-run details stay in the
    per-run invocation)."""
    ids: list[str] = []
    for results in per_run.values():
        for r in results:
            if r["id"] not in ids:
                ids.append(r["id"])
    rows = []
    for pid in ids:
        statuses = [next((r["status"] for r in results if r["id"] == pid), "MISSING")
                    for results in per_run.values()]
        rows.append({"id": pid,
                     "pass": sum(s == "PASS" for s in statuses),
                     "check": sum(s == "CHECK" for s in statuses),
                     "fail": sum(s in ("FAIL", "MISSING") for s in statuses),
                     "runs": len(statuses),
                     "verdicts": [s[0] for s in statuses]})
    if as_json:
        print(json.dumps(rows, indent=2))
        return 0
    n_runs = len(per_run)
    print(f"aggregate over {n_runs} runs: {', '.join(per_run)}")
    print(f"{'id':<5} {'pass':<7} {'check':<6} {'fail':<5} verdicts")
    print("-" * 44)
    for row in rows:
        print(f"{row['id']:<5} {row['pass']}/{row['runs']:<5} "
              f"{row['check']:<6} {row['fail']:<5} {' '.join(row['verdicts'])}")
    print("-" * 44)
    stable = sum(r["pass"] == r["runs"] for r in rows)
    total_pass = sum(r["pass"] for r in rows)
    total = sum(r["runs"] for r in rows)
    checks = sum(r["check"] for r in rows)
    print(f"prompts passing every run: {stable}/{len(rows)}  ·  "
          f"total PASS {total_pass}/{total}  (+{checks} CHECK)")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("run_dirs", type=Path, nargs="+")
    ap.add_argument("--require-rtfm", action="store_true",
                    help="enforce the rtfm call (cold-start eval)")
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    if len(args.run_dirs) > 1:
        per_run = {d.name: grade_dir(d, args.require_rtfm) for d in args.run_dirs}
        return aggregate(per_run, args.json)

    results = grade_dir(args.run_dirs[0], args.require_rtfm)

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
            label = "WRITE WITHOUT CONFIRMATION (F4)"
            if r.get("write_refused"):
                label += " — attempt refused by guard, no write executed"
            problems.append(label)
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
