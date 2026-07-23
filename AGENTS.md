# AGENTS.md

Norms for everyone working on hactl — human or model.

> Filing a bug or a PR? Read [CONTRIBUTING.md](CONTRIBUTING.md) first. This file is about
> *working on the code*.

## Tests

**To run all tests, load the `run-tests` skill.** It contains the exact commands and prerequisites. Do not guess.

The only correct command for a full test run is **`make gates`**. It runs lint,
the unit tier, and all three Docker tiers (integration, companion, discovery).
Docker must be running first; `make gates` refuses to start without it rather
than silently narrowing what was verified.

`make test` is the unit tier alone. It starts no Home Assistant and is never
acceptance — hactl's job is to report what a real HA contains, and the unit
tier cannot see a wrong lookup key or a missing registry fallback. Install the
pre-push hook with `make hooks` so this is enforced rather than remembered.

After unit tests are updated, run linter, fix, and test again.

## Working Principles

**Plan before acting.** No change without a plan. Draft, review, then implement.

**Read before writing.** Read the concept, existing code, and tests first. No assumptions about code you haven't seen.

**Done = green tests.** A feature without tests is unfinished. A milestone without passing tests is not done.

**No speculative fixes.** Reproduce the bug first, then fix it. Guessing is not debugging.

**Security is not optional.** No secrets in the repo. Write-path always dry-run capable.

**Manage context.** Use subagents for long tasks. Use intermediate files to store knowledge.

