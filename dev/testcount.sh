#!/usr/bin/env bash
#
# testcount.sh — how many test functions each tier has, derived.
#
# Why this exists: TC-7 says a count a document states must be generated or
# omitted. `docs/testing.md` stated four of them by hand and every one had
# drifted — in both directions, twice. Worse, three different hand-counting
# methods disagreed with each other (285 by `//go:build integration` tag, 289 by
# directory, 303 by an earlier pass), so there was no way to tell which stale
# number to correct. This script is the generator the document now points at.
#
# Output is one `<tier> <count>` line per tier on stdout, nothing else, so it
# reads the same to a person and to a pipeline:
#
#     unit <n>
#     integration <n>
#     companion <n>
#     discovery <n>
#
# The shape is the example; the numbers are deliberately not written down here.
# A sample count in a comment is a hand-written count with a comment's immunity
# from every gate — which is exactly what this script exists to abolish, and it
# had already drifted once.
#
# Diagnostics go to stderr. Needs no Docker and no Home Assistant.
#
# # The oracle
#
# The count comes from the assertion-floor gate (H-19, internal/testaudit),
# which already derives it. That gate parses every `*_test.go` in the repository
# with go/ast, reads the build constraint on each file to place it in a tier, and
# keeps only the functions `go test` would actually run — so TestMain, helpers
# and benchmarks are excluded, which is correct: TestMain is not a test. It
# prints that tally on every run because the tally is the number it holds at zero
# missing assertions.
#
# Reading the gate's tally is deliberate. A second, independent counter here
# would be a second thing to keep true, and two derivations that disagree is the
# problem this script exists to end, one level up.
#
# # Why not `go test -tags=<tier> -list`
#
# `-list` looks like the authority — it reports what would run — and for the
# untagged build it is. It is not usable per tier: `-list` is handled inside
# `testing.M.Run`, so a package's TestMain executes first, and the companion and
# discovery tiers build the companion image and start a Docker Compose stack in
# theirs. Those tiers therefore need Docker just to be listed, and when the stack
# cannot start the package lists *zero* tests: the tag's total comes out *below*
# the untagged total and a subtraction silently yields a negative tier.
#
# It remains the authority for the untagged build, where no TestMain does any
# work, so the unit tier is cross-checked against it below. That is the one tier
# where both numbers are obtainable, and it is enough to prove the AST scan
# agrees with what `go test` runs.
set -euo pipefail

# The tiers the gate reports, in the order the document discusses them.
TIERS="unit integration companion discovery"

if [ ! -f go.mod ]; then
  echo "ERROR: no go.mod here. Run this from the repository root." >&2
  exit 1
fi

if ! tally="$(go test ./internal/testaudit/ -count=1 -run TestAssertionFloor -v 2>&1)"; then
  printf '%s\n' "$tally" >&2
  echo "ERROR: the assertion-floor gate is red. Fix it before trusting a count —" >&2
  echo "       a tier with unasserted tests has fewer real tests than it reports." >&2
  exit 1
fi

# tier=unit        tests=<n> asserting=<n> exempt=<n>  missing=<n>
counts="$(printf '%s\n' "$tally" |
  sed -nE 's/.*tier=([a-z_]+)[[:space:]]+tests=([0-9]+).*/\1 \2/p')"

report=""
unit=""
for tier in $TIERS; do
  n="$(printf '%s\n' "$counts" | awk -v t="$tier" '$1 == t { print $2 }')"
  if [ -z "$n" ]; then
    printf '%s\n' "$tally" >&2
    echo "ERROR: the gate reported no count for tier '$tier'." >&2
    echo "       Its tally line is a t.Logf in internal/testaudit/assertions_test.go." >&2
    echo "       If that changed shape, fix this script — do not hand-count." >&2
    exit 1
  fi
  report="${report}${tier} ${n}
"
  if [ "$tier" = unit ]; then
    unit="$n"
  fi
done

if ! listing="$(go test -list '.*' ./... 2>&1)"; then
  printf '%s\n' "$listing" >&2
  echo "ERROR: 'go test -list' failed, so the unit tier cannot be cross-checked." >&2
  exit 1
fi

# `-list` prints one bare name per test, benchmark, fuzz target and example, plus
# per-package status lines. Only test functions start with `Test`, which is
# exactly the corpus the gate counts.
listed="$(printf '%s\n' "$listing" | grep -cE '^Test' || true)"
if [ "$listed" -ne "$unit" ]; then
  echo "ERROR: the two derivations disagree on the unit tier:" >&2
  echo "         assertion-floor gate: $unit" >&2
  echo "         go test -list:        $listed" >&2
  echo "       One of them is wrong. Do not publish either number until you know" >&2
  echo "       which — a count nobody can reproduce is how docs/testing.md got" >&2
  echo "       three mutually contradictory ones." >&2
  exit 1
fi
echo "cross-check: 'go test -list' agrees on the unit tier ($listed)" >&2

printf '%s' "$report"
