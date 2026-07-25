#!/bin/sh
# deadcode-gate.sh — fail when a function is unreachable from the hactl binary
# and is not on the recorded allowlist.
#
# Why this exists: `findAutomationRelations` was 86.4% covered and had no caller
# outside its own two tests. The command that used it had migrated to a weaker
# replacement, so the suite kept certifying a capability the product no longer
# had. Coverage cannot see that — coverage rises when a test calls dead code.
# Reachability from `main` can, and that is what this gate measures.
#
# The allowlist is not an exemption list. Every line is a debt entry with a
# stated reason, and the gate fails in BOTH directions:
#   - a function that is unreachable and not listed  -> wire it or delete it
#   - a listed function that is no longer unreachable -> delete the stale line
# so the file cannot quietly rot into a rubber stamp.
set -eu

ALLOW="dev/deadcode-allow.txt"
DEADCODE="${DEADCODE:-}"
if [ -z "$DEADCODE" ]; then
  DEADCODE="$(command -v deadcode 2>/dev/null || echo "$(go env GOPATH)/bin/deadcode")"
fi

if [ ! -x "$DEADCODE" ]; then
  echo "ERROR: deadcode not found (looked on PATH and in \$(go env GOPATH)/bin)."
  echo "Install: go install golang.org/x/tools/cmd/deadcode@v0.48.0"
  exit 1
fi

if [ ! -f "$ALLOW" ]; then
  echo "ERROR: $ALLOW is missing. Run this from the repository root."
  exit 1
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# `-test=false` is the whole point: a function only tests can reach is exactly
# the failure mode being detected, so test binaries must not count as callers.
"$DEADCODE" -test=false ./cmd/hactl \
  | sed -E 's/^(.+):[0-9]+:[0-9]+: unreachable func: (.+)$/\1 \2/' \
  | sort > "$tmp/now"

sed -e 's/[[:space:]]*#.*$//' -e '/^[[:space:]]*$/d' "$ALLOW" | sort > "$tmp/allow"

comm -23 "$tmp/now" "$tmp/allow" > "$tmp/new"
comm -13 "$tmp/now" "$tmp/allow" > "$tmp/stale"

status=0

if [ -s "$tmp/new" ]; then
  status=1
  echo "ERROR: unreachable from the hactl binary and not on the allowlist:"
  sed 's/^/  /' "$tmp/new"
  echo
  echo "Each of these is reachable only from tests, so the suite is certifying"
  echo "a capability the product does not have. Wire it into the command tree,"
  echo "or delete it with its tests. If it genuinely exists to serve a test or"
  echo "a gate, add it to $ALLOW with the reason on the line."
fi

if [ -s "$tmp/stale" ]; then
  status=1
  echo "ERROR: $ALLOW lists entries that are no longer unreachable (or no longer exist):"
  sed 's/^/  /' "$tmp/stale"
  echo
  echo "Delete these lines. A stale allowlist is how a gate turns into a rubber stamp."
fi

if [ "$status" -eq 0 ]; then
  echo "deadcode: ok ($(wc -l < "$tmp/allow" | tr -d ' ') allowlisted, 0 new)"
fi

exit "$status"
