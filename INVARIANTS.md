# Invariants

Cross-cutting rules the CLI must satisfy regardless of which command grows
next. Server-side counterparts (auth on every route, dry-run defaults,
backup-before-mutate) live in the hactl-companion repo's `INVARIANTS.md`.

**Discipline:** a rule without an enforcing test does not get added here.
When behavior changes intentionally, the test and this file change in the
same PR.

## H-1 — Non-idempotent requests are never auto-retried

A `POST` is retried only when the request provably never left the client
(dial/connection-refused class). A 5xx or lost response means the server may
have acted, so only idempotent methods (GET/HEAD/PUT/DELETE/OPTIONS) retry —
a create is never silently duplicated. A signed 401 (expired Ingress session)
is safe to retry for any method: the server rejected before acting.

- Enforced by: `internal/companion/client_retry_test.go`
  (`shouldRetry` truth table, `TestPostNotRetriedOn5xx`, `TestGetRetriedOn5xx`)

## H-2 — Mutating commands are dry-run by default

Every command family that writes (config, dash, ref, …) reports what it
*would* do unless `--confirm` is given, and the companion request carries
`dry_run=true` accordingly. The first `--confirm` of a write family in a
session is refused with the family how-to, so an uninformed apply cannot be
the first thing that executes.

- Enforced by: `internal/cmd/ref_test.go` (dry-run default asserted against a
  stubbed companion), `internal/cmd/confirm_guard_test.go` (first-write
  refusal + informed retry), `internal/cmd/config_delete_test.go`

## H-3 — The vendored companion contract must not drift

`testdata/companion-v1.yaml` is a verbatim copy of the companion's generated
spec (`make sync-spec` to refresh); the CLI is coded against it. Contract
tests run the real CLI against a companion built from source, so an
incompatible companion change fails before release.

- Enforced by: `make check-spec-drift` (byte-level diff against
  `../hactl-companion`), `internal/companiontest/contract_test.go`
  (CI "Companion Tests" job, companion built from `main`). The
  "Companion Tests" CI job now runs `make check-spec-drift
  COMPANION_DIR=companion-src` right after checking out
  `hactl-companion`, so a drifted vendored spec fails CI before it can
  reach a release.
