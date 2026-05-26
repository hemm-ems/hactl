# AGENT.md

Norms for everyone working on hactl — human or model.

## Tests

**To run all tests, load the `run-tests` skill.** It contains the exact commands and prerequisites. Do not guess.

The only correct command for a full test run is `make test-int`. Docker must be running first.

After unit tests are updated, run linter, fix, and test again.

## Working Principles

**Plan before acting.** No change without a plan. Draft, review, then implement.

**Read before writing.** Read the concept, existing code, and tests first. No assumptions about code you haven't seen.

**Done = green tests.** A feature without tests is unfinished. A milestone without passing tests is not done.

**No speculative fixes.** Reproduce the bug first, then fix it. Guessing is not debugging.

**Security is not optional.** No secrets in the repo. Write-path always dry-run capable.

**Manage context.** Use subagents for long tasks. Use intermediate files to store knowledge.

