#!/usr/bin/env bash
#
# The table next-version.sh must satisfy. Runs in CI; needs nothing but bash.
#
# Every case here is an ordering question, because ordering is the only property
# a version scheme owes anyone. The cases that would have caught the real bug
# are marked — they are the ones where HA's series is BEHIND the newest tag, and
# the old auto-release would have proposed a version below the current release.

set -uo pipefail
cd "$(dirname "$0")" || exit 2

pass=0 fail=0

# ok <name> <ha-series> <expected> <tags...>
ok() {
  local name=$1 ha=$2 want=$3; shift 3
  local got
  got=$(printf '%s\n' "$@" | ./next-version.sh "$ha" 2>&1) || {
    printf 'FAIL  %s\n      wanted %s, script exited non-zero: %s\n' "$name" "$want" "$got"
    fail=$((fail + 1)); return
  }
  if [ "$got" = "$want" ]; then
    pass=$((pass + 1))
  else
    printf 'FAIL  %s\n      wanted %s, got %s\n' "$name" "$want" "$got"
    fail=$((fail + 1))
  fi
}

# refuses <name> <ha-series> <tags...>
refuses() {
  local name=$1 ha=$2; shift 2
  local got
  if got=$(printf '%s\n' "$@" | ./next-version.sh "$ha" 2>&1); then
    printf 'FAIL  %s\n      expected refusal, got %s\n' "$name" "$got"
    fail=$((fail + 1))
  else
    pass=$((pass + 1))
  fi
}

# --- HA in step with the newest tag: extend the series ---
ok "extends within the series"        2026.7 v2026.7.12 v2026.7.11 v2026.7.10
ok "patch is numeric, not lexical"    2026.8 v2026.8.11 v2026.8.9 v2026.8.10
ok "unsorted input finds the newest"  2026.7 v2026.7.16 v2026.7.15 v2026.6.4 v2026.7.9

# --- HA has moved on: open the new series ---
ok "rolls when HA advances"           2026.9 v2026.9.0  v2026.8.5
ok "rolls across the year boundary"   2027.1 v2027.1.0  v2026.12.3

# --- HA is BEHIND the newest tag (a series cut early) ---
# These are the regression cases. The old auto-release computed v{HA series}.0
# with no reference to the tags, so here it proposed v2026.7.0 while v2026.8.1
# was the current release — a version below the one already shipped.
ok "does not rewind to HA's series"   2026.7 v2026.8.2  v2026.8.1 v2026.7.15
ok "extends a series months early"    2026.6 v2026.8.2  v2026.8.1
ok "extends across a year, early"     2026.12 v2027.1.1 v2027.1.0

# --- garbage in ---
refuses "no tags"                     2026.7
refuses "no CalVer tags"              2026.7 v1.0.0 nightly
refuses "bad HA series"               2026.13 v2026.7.1
refuses "empty HA series"             "" v2026.7.1

# --- the invariant itself, over the whole table ---
# Whatever the rule decides, the answer must outrank every tag it was given.
newer_than_all() {
  local ha=$1; shift
  local got; got=$(printf '%s\n' "$@" | ./next-version.sh "$ha") || return 1
  local top; top=$(printf '%s\n' "$@" "$got" | sed 's/^v//' \
    | sort -t. -k1,1n -k2,2n -k3,3n | tail -1)
  [ "v${top}" = "$got" ]
}
for probe in "2026.7 v2026.8.1 v2026.7.15" "2026.9 v2026.8.5" "2027.1 v2026.12.3" \
             "2026.8 v2026.8.9 v2026.8.10" "2026.6 v2026.8.1"; do
  # shellcheck disable=SC2086
  if newer_than_all $probe; then
    pass=$((pass + 1))
  else
    printf 'FAIL  invariant violated for: %s\n' "$probe"
    fail=$((fail + 1))
  fi
done

printf '\nnext-version: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
