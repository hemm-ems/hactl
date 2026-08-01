#!/usr/bin/env bash
#
# sweep.sh — run every tier and report what it proved, keyed to the ledger.
#
# Why this exists: the acceptance gate for the live-fire work is "every tier
# green on both profiles", and the only way to read that was `go test` output —
# eight separate invocations, each answering "did the code compile and did the
# assertions hold", none of them answering the question actually being asked,
# which is "which of the 90 findings does this run still hold closed?".
#
# So this runs the tiers and then reads the run back through the ledger. Every
# sweep case in internal/livefire cites the finding it exists for in its doc
# comment; that citation is the key. The report is therefore DERIVED from the
# source rather than maintained beside it — a case whose finding number is
# edited moves in the report, and a finding whose case is deleted stops
# appearing, without anybody updating a table.
#
# Usage:
#   dev/sweep.sh                 # rig profile only (what CI can do)
#   dev/sweep.sh --live          # adds the real instance; needs HACTL_LIVEFIRE_DIR
#   dev/sweep.sh --live --quick  # skips the three non-livefire Docker tiers
#
# Exit status is non-zero if any tier failed, so it is usable as a gate and not
# only as a thing to read.
#
# # What it deliberately does NOT do
#
# It does not decide whether a skip is acceptable. It prints every skip with its
# reason and the count, because the sweep's skips are rig capability debt (R11,
# mostly) and the one thing that must never happen is a skip count drifting
# upward unnoticed while the run still reads "ok". Reconciling that is a
# person's job; making it impossible to miss is this script's.

set -uo pipefail

cd "$(dirname "$0")/.." || exit 1

LIVE=0
QUICK=0
for arg in "$@"; do
  case "$arg" in
    --live)  LIVE=1 ;;
    --quick) QUICK=1 ;;
    -h|--help) sed -n '2,32p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $arg (try --help)" >&2; exit 2 ;;
  esac
done

if [ "$LIVE" -eq 1 ] && [ -z "${HACTL_LIVEFIRE_DIR:-}" ]; then
  echo "ERROR: --live needs HACTL_LIVEFIRE_DIR pointing at a configured instance." >&2
  echo "The live profile writes nothing outside pg_* and refuses to try, but it" >&2
  echo "still talks to a real house, so it is never implicit." >&2
  exit 2
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
rule() { printf '%s\n' "----------------------------------------------------------------------"; }

# ---------------------------------------------------------------------------
# 1. The tiers
# ---------------------------------------------------------------------------
#
# Order is cheapest-first so a broken build is reported in seconds rather than
# after twenty minutes of containers. Every tier runs even when an earlier one
# fails, for the same reason `make lint` runs all five tag sets: a gate has to
# report everything it knows in one run, or one fix becomes four round trips.

TIERS=()
add_tier() { TIERS+=("$1|$2"); }

add_tier "lint"          "make lint"
add_tier "deadcode"      "make deadcode"
add_tier "assert-floor"  "make test-assert-floor"
add_tier "surface"       "make test-surface"
add_tier "unit"          "go test ./... -count=1"
if [ "$QUICK" -eq 0 ]; then
  add_tier "integration" "make test-int"
  add_tier "companion"   "make test-companion"
  add_tier "discovery"   "make test-int-discovery"
fi

status_of() { grep -E "^$1\|" "$WORK/tiers" | cut -d'|' -f2; }

: > "$WORK/tiers"
for entry in "${TIERS[@]}"; do
  name="${entry%%|*}"; cmd="${entry#*|}"
  printf '  %-14s ' "$name"
  start=$(date +%s)
  if eval "$cmd" > "$WORK/$name.log" 2>&1; then
    result="PASS"
  else
    result="FAIL"
  fi
  printf '%s  (%ss)\n' "$result" "$(( $(date +%s) - start ))"
  echo "$name|$result|$(( $(date +%s) - start ))" >> "$WORK/tiers"
done

# The sweep tier is run separately: its per-case JSON is what the ledger report
# below is built from, so it cannot share the plain-output path above.
printf '  %-14s ' "sweep"
sweep_start=$(date +%s)
if [ "$LIVE" -eq 1 ]; then
  HACTL_LIVEFIRE_DIR="$HACTL_LIVEFIRE_DIR" \
    go test -tags=livefire -count=1 -timeout 1800s -v -json ./internal/livefire/... \
    > "$WORK/sweep.json" 2> "$WORK/sweep.err"
else
  go test -tags=livefire -count=1 -timeout 1800s -v -json ./internal/livefire/... \
    > "$WORK/sweep.json" 2> "$WORK/sweep.err"
fi
sweep_status=$?
[ "$sweep_status" -eq 0 ] && sweep_result="PASS" || sweep_result="FAIL"
printf '%s  (%ss)\n' "$sweep_result" "$(( $(date +%s) - sweep_start ))"
echo "sweep|$sweep_result|$(( $(date +%s) - sweep_start ))" >> "$WORK/tiers"

# ---------------------------------------------------------------------------
# 2. The per-case outcomes
# ---------------------------------------------------------------------------
#
# `go test -json` emits one object per event; the ones that matter carry
# "Action":"pass|fail|skip" together with a "Test" name. Subtests arrive as
# `Parent/rig` and `Parent/live`, which is exactly the granularity the report
# wants — a case's answer is per profile.

# -E throughout, and that is not a style choice. The first version used BRE with
# `\|` alternation, which GNU sed accepts and BSD sed does not — so on macOS
# every extraction matched nothing, the ledger printed a dash for all 58 cases,
# and the report looked like a clean run of a suite that had told it nothing.
# A reporting script that silently degrades to "no data" is the same failure
# class as a gate that silently stops matching.
extract_outcomes() {
  sed -nE 's/.*"Action":"(pass|fail|skip)".*"Test":"([^"]*)".*/\1 \2/p' "$WORK/sweep.json" |
    sort -u -k2,2
}
extract_outcomes > "$WORK/outcomes"

outcome_for() { # case, profile
  awk -v want="$1/$2" '$2 == want { print toupper($1); found=1 } END { if (!found) print "-" }' "$WORK/outcomes"
}

# ---------------------------------------------------------------------------
# 3. The ledger key, derived from the source
# ---------------------------------------------------------------------------
#
# Each sweep case names its finding in the comment block above it — "Finding
# #46", "finding #104", "findings #63 and #64", "Findings #28 #29 #63 #64". The
# awk below carries the most recent citations seen and attaches them to the next
# `func TestSweep...`, which is the same rule a reader applies.
#
# Deriving it is the point. A hand-written table mapping findings to tests is a
# second thing to keep true, and this project's whole 2026-07 story is what
# happens when the enumeration and the code drift apart.

awk '
  /^\/\/ *$/ { next }
  /^\/\// {
    line = $0
    if (line ~ /[Ff]inding/ || pending != "") {
      while (match(line, /#[0-9]+/)) {
        found = substr(line, RSTART + 1, RLENGTH - 1)
        pending = pending (pending == "" ? "" : ",") found
        line = substr(line, RSTART + RLENGTH)
      }
    }
    next
  }
  /^func Test/ {
    name = $2
    sub(/\(.*/, "", name)
    if (pending != "") print pending "\t" name
    pending = ""
    next
  }
  { pending = "" }
' internal/livefire/*_test.go | sort -t'	' -k1,1V > "$WORK/ledger"

# ---------------------------------------------------------------------------
# 4. The report
# ---------------------------------------------------------------------------

echo
rule
bold "TIERS"
rule
failed=0
while IFS='|' read -r name result seconds; do
  printf '  %-14s %-5s %5ss' "$name" "$result" "$seconds"
  if [ "$result" = "FAIL" ]; then
    failed=1
    printf '   <- %s' "$WORK/$name.log"
  fi
  echo
done < "$WORK/tiers"

echo
rule
if [ "$LIVE" -eq 1 ]; then
  bold "LEDGER — every finding with a standing case, on both profiles"
else
  bold "LEDGER — every finding with a standing case (rig profile only)"
fi
rule
printf '  %-10s %-8s %-8s %s\n' "FINDING" "RIG" "LIVE" "CASE"
while IFS=$'\t' read -r findings testname; do
  rig=$(outcome_for "$testname" "rig")
  live=$(outcome_for "$testname" "live")
  # A case with no per-profile subtests answers once, at the top level.
  if [ "$rig" = "-" ] && [ "$live" = "-" ]; then
    rig=$(awk -v want="$testname" '$2 == want { print toupper($1) } ' "$WORK/outcomes" | head -1)
    [ -z "$rig" ] && rig="-"
    live="n/a"
  fi
  printf '  %-10s %-8s %-8s %s\n' "#${findings}" "$rig" "$live" "$testname"
done < "$WORK/ledger"

echo
rule
bold "SKIPS — every one, with its reason. Reconcile these; never read them as green."
rule
skips=$(awk '$1 == "skip"' "$WORK/outcomes" | wc -l | tr -d ' ')
sed -nE 's/.*"Action":"skip".*"Test":"([^"]*)".*/\1/p' "$WORK/sweep.json" | sort -u |
  while read -r name; do
    reason=$(grep -F "\"Test\":\"$name\"" "$WORK/sweep.json" |
             sed -nE 's/.*"Output":"( *[a-z_]*_test\.go:[0-9]*: )?([^"]*)".*/\2/p' |
             grep -viE '^\s*$|^--- SKIP|^=== ' | tail -1)
    printf '  %-64s %s\n' "$name" "${reason:-<no reason recorded>}"
  done
echo
echo "  $skips skipped. A skip is rig capability debt or an unconfigured profile,"
echo "  never a pass. If this number moved, find out which one and why."

echo
rule
bold "COLLATERAL — did the run leave each instance as it found it?"
rule
if grep -q "COLLATERAL:" "$WORK/sweep.err"; then
  grep "COLLATERAL:" "$WORK/sweep.err" | sed 's/^/  /'
  failed=1
else
  echo "  clean — the run-level census found nothing changed outside pg_*."
fi

echo
rule
if [ "$failed" -eq 0 ] && [ "$sweep_result" = "PASS" ]; then
  bold "ALL TIERS GREEN"
  [ "$LIVE" -eq 0 ] && echo "  (rig profile only — re-run with --live before calling the gate met)"
  exit 0
fi
bold "NOT GREEN — see the tier table above"
exit 1
