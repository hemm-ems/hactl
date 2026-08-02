#!/usr/bin/env bash
#
# The one answer to "what version comes next".
#
# Every release path calls this. There is no second opinion, because there used
# to be: patch-release derived the next version from the latest tag while
# auto-release derived it from the calendar and never looked at the tags at all.
# Two rules producing two answers is how a release ends up BEHIND the one before
# it — which would drag `latest`, the Homebrew formula and the add-on store
# backwards with it.
#
# The rule, and the whole of it:
#
#   next = HA's series .0        when HA's series is ahead of the newest tag
#   next = newest tag, patch+1   otherwise
#
# and then the only thing that actually matters is asserted rather than assumed:
# THE RESULT IS STRICTLY GREATER THAN EVERY EXISTING TAG. If it is not, this
# exits non-zero and the release stops. A version that goes backwards is not a
# release to be repaired later; it is one that must never be cut.
#
# Note the second branch is what runs when the newest tag is AHEAD of Home
# Assistant — a series opened before HA published the one it names. That is
# fine and needs no repair: the series is extended, never reissued, never
# rewound. Being ordered matters; being in step with HA is what the number is
# for when the two do not conflict.
#
# Usage:   next-version.sh <ha-series>  < tags-on-stdin
# Example: git tag | next-version.sh 2026.8   ->   v2026.8.2
#
# Pure: no network, no git, no clock. Tags come in on stdin so this is testable.
# See next-version.test.sh for the table it must satisfy.

set -euo pipefail

ha_series="${1:-}"
if ! printf '%s' "$ha_series" | grep -qE '^[0-9]{4}\.([1-9]|1[0-2])$'; then
  echo "next-version: bad HA series '${ha_series}' (want YYYY.M)" >&2
  exit 2
fi

# Keep only real CalVer release tags, then sort numerically per field. Field
# order matters: a lexical sort puts v2026.8.9 above v2026.8.10.
tags=$(grep -E '^v[0-9]{4}\.([1-9]|1[0-2])\.[0-9]+$' || true)
if [ -z "${tags}" ]; then
  echo "next-version: no CalVer tags (vYYYY.M.PATCH) on stdin" >&2
  exit 2
fi

newest=$(printf '%s\n' "${tags}" \
  | sed 's/^v//' \
  | sort -t. -k1,1n -k2,2n -k3,3n \
  | tail -1)

n_year=${newest%%.*}
n_rest=${newest#*.}
n_month=${n_rest%%.*}
n_patch=${n_rest#*.}

ha_year=${ha_series%%.*}
ha_month=${ha_series#*.}

if [ $((ha_year * 12 + ha_month)) -gt $((n_year * 12 + n_month)) ]; then
  next="${ha_year}.${ha_month}.0"
else
  next="${n_year}.${n_month}.$((n_patch + 1))"
fi

# The guarantee. Everything above is a proposal; this is what makes it safe.
x_year=${next%%.*}; x_rest=${next#*.}; x_month=${x_rest%%.*}; x_patch=${x_rest#*.}
if [ $((x_year * 12 + x_month)) -lt $((n_year * 12 + n_month)) ] ||
   { [ $((x_year * 12 + x_month)) -eq $((n_year * 12 + n_month)) ] && [ "${x_patch}" -le "${n_patch}" ]; }; then
  echo "next-version: refusing to go backwards — computed v${next}, newest tag is v${newest}" >&2
  exit 1
fi

printf 'v%s\n' "${next}"
