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

## H-4 — An automation write is verified against HA, not against hactl's output

`auto apply` and `auto rollback` are proven by reading the config back from HA
and comparing it, never by asserting on the CLI's echo. `applied: <id>` is
printed unconditionally once the write call returns nil, so an assertion on it
holds whether or not anything reached HA.

This exists because stubbing `haapi.Client.UpdateAutomationConfig` to
`return nil` — discarding every automation write — left both the unit tier and
the whole integration package green. The prior test asserted only that the
automation still existed after each step, which is true either way.

Comparison folds HA's legacy singular keys (`trigger`/`condition`/`action`,
and `service` within a step) onto the modern plural ones, because writing
through the Config API migrates the schema; everything else must match exactly.

- Enforced by: `internal/integration/write_roundtrip_test.go`
  (`TestAutoApplyRollbackRoundTrip`, `make test-int`)

## H-5 — No automation write without a successful backup

`Writer.Apply` returns an error rather than writing when the backup fails.
Without the backup the previous config is unrecoverable, so `auto rollback`
would have nothing to restore; warning and writing anyway traded the user's
only undo for a log line that `HACTL_LOG_LEVEL` routinely hides.

- Enforced by: `internal/writer/writer_test.go`
  (`TestWriter_Apply_BackupFailureAborts`)

## H-6 — A backup belongs to exactly one automation

Backup selection matches the whole id after the timestamp, never a trailing
underscore-delimited segment of it. Segment matching made
`auto rollback door` select `bathroom_light_on_door`'s backup and write that
config back under the id the user asked for — one automation's config restored
over another's. Underscore-suffixed ids are ordinary in real HA configs.

- Enforced by: `internal/writer/writer_test.go` (`TestContainsAutoID`, the
  collision cases)

## H-7 — A decode that yields nothing never renders as success

When a wire decode produces an all-zero value, the result is reported as
`UNPARSED`, not as a passing outcome. `overallResult` returns `StepUnknown`
when both `script_execution` and `state` are empty, and `Condense` forces it
when the decode carries no identity and no steps at all; an empty domain and
item_id render as nothing rather than a bare `.`.

Empty was previously spelled "success": a `trace/get` struct whose tags did not
match HA's wire shape unmarshalled to zero, and every automation run — including
failures — rendered as `  .    PASS` for months while the whole suite stayed
green. The marker is also scanned for by the integration harness itself, so
every command a test runs is checked for it, including tests that assert
nothing of their own.

- Enforced by: `internal/analyze/trace_unparsed_test.go`
  (`TestOverallResult_EmptyIsNotPass`, `TestCondense_EmptyDecodeIsUnknown`,
  `TestFormatCondensed_UnparsedNeverLooksLikePass`),
  `internal/integration/degeneracy_test.go` (`looksDegenerate`, wired into
  `runHactl`/`runHactlDir`/`runHactlErr`/`runHactlDirErr`)
