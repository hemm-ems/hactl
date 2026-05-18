# hactl – Testing Guide

This document explains how hactl is tested, what the tests actually verify, how you can run them yourself, and where we know the coverage is thin. It is written for someone new to the project — or to software testing in general — so it tries to explain the *why* at each step, not just the *what*.

Testing a command-line tool that talks to Home Assistant is harder than testing a pure library. The tool's job is to translate real HA state into useful output, so testing against a mock would only confirm that the code calls the mock correctly — not that it actually works. This is why hactl's test suite goes to some lengths to run against a real, live HA instance for the tests that matter most.

---

## The Three Layers

hactl's tests are organized into three layers, each with a different scope and a different cost.

**Unit tests** are the fastest and cheapest. They cover individual functions: parsing logic, formatting, anomaly detection algorithms, cache storage, config file loading. No network, no Docker, no HA. They run in a few seconds and serve as a quick sanity check.

**Integration tests** are the main event. They start a real Home Assistant instance in a Docker container, run hactl commands against it, and check the output. These tests are slower (a couple of minutes the first time, faster once Docker has cached the image), but they are the ones that tell us whether the tool actually works with real HA.

**Companion tests** cover the optional companion service — a small sidecar that gives hactl filesystem write access to the HA config directory. They use Docker Compose to stand up both HA and the companion together, then exercise the companion's API for CRUD operations, security boundaries, and service discovery.

Each layer has its own `make` target, and each layer is also enforced independently in CI. You can think of the layers as a pyramid: many small unit tests at the base, a broad set of integration tests in the middle, and a focused companion suite at the top.

---

## Layer 1: Unit Tests

Unit tests live alongside the code they test, in files named `*_test.go` with no build tag. Go's standard testing toolchain picks them up automatically. To run them:

```bash
make test
# equivalent: go test ./... -count=1
```

This takes roughly five seconds and requires nothing beyond Go itself — no Docker, no running HA instance.

What the unit tests cover:

- **`internal/analyze/`** — The trace condensing logic, time-series resampling, log deduplication, and anomaly detection algorithms. These are non-trivial computations that deserve their own deterministic tests with known input/output pairs. The trace test fixtures (sample JSON files in `testdata/traces/`) are used here.
- **`internal/cache/`** — Reading and writing time-series data and trace metadata to the filesystem cache. Tests verify that stored values round-trip correctly and that stale entries expire as expected.
- **`internal/config/`** — Configuration loading: `.env` file parsing, environment variable precedence, and the instance discovery fallback chain.
- **`internal/format/`** — Output formatting: table alignment, text truncation, JSON rendering. The exact shape of hactl's output matters for token efficiency, so these tests pin the formatting behaviour.
- **`internal/haapi/`** — The low-level HA HTTP client: authentication headers, retry logic, and WebSocket connection handling. These tests use a small in-process HTTP server to simulate HA responses, which is the appropriate level of isolation for protocol-level concerns.
- **`internal/cmd/`** — Command-level utility functions: `parseSince()` for relative time expressions, entity pattern matching, domain filtering, token budget policy, and a few others. The command tests also verify that `hactl --help` produces output and that `hactl version` includes the expected string.
- **`internal/writer/`** — YAML diff generation, automation file detection, and backup file naming. The writer is used by the write-path commands (`auto apply`, `rollback`), and it is important that diffs are correct before anything is written to disk.

Approximately 199 unit tests exist across these packages as of the current state of the codebase. That number will drift as the code evolves; treat it as a rough order of magnitude.

---

## Layer 2: Integration Tests

Integration tests live in `internal/integration/` and carry the build tag `//go:build integration`. The tag is what keeps them out of a plain `go test ./...` invocation — you have to opt in explicitly. This is a deliberate design choice: running integration tests requires Docker, and many development workflows (editing, linting, quick feedback loops) should not be blocked on Docker availability.

To run the full test suite including integration tests:

```bash
make test-int
# equivalent: go test ./... -tags=integration -count=1 -timeout 300s
```

The first run takes roughly two minutes because Docker has to pull the Home Assistant container image (~1 GB). Subsequent runs are much faster (~60 seconds) because Docker caches the image locally and testcontainers (the Go library managing the container lifecycle) can reuse a cached container in some cases.

### How the container is managed

Starting a Home Assistant instance is not as simple as `docker run`. HA requires an interactive onboarding step before its API becomes available — there is no flag to skip it. `internal/hatest/hatest.go` automates the entire flow:

1. Start `ghcr.io/home-assistant/home-assistant:stable` (or a configured override) with a fixture directory bind-mounted as `/config/`.
2. Poll `GET /api/onboarding` until HA is ready to accept the onboarding request.
3. `POST /api/onboarding/users` to create an owner account; this returns a one-time `auth_code`.
4. `POST /auth/token` with `grant_type=authorization_code` to exchange that code for short-lived tokens.
5. `POST /api/onboarding/core_config` and `/api/onboarding/analytics` to complete the remaining onboarding steps.
6. Create a long-lived access token via the WebSocket API (`auth/long_lived_access_token`), since there is no REST endpoint for this.
7. Return an `Instance` object with `URL()`, `Token()`, and `Dir()` — a temp directory containing a `.env` file that hactl can read directly.

Starting one container per test would make the suite take 20+ minutes. Instead, each test *package* starts a single container in `TestMain`, shares it across all tests in that package, and tears it down at the end. Tests that only read from HA can safely share a container because they do not interfere with each other.

Write-path tests (those that actually modify HA configuration) are isolated in `write_test.go` and run in a specific order within that file. The error-path tests use a completely separate container with a deliberately broken fixture.

### Fixtures

Fixtures are directories in `testdata/fixtures/` that are mounted as the HA config directory when a container starts. There are three of them:

**`basic/`** is the default. It contains a minimal `configuration.yaml` that enables the standard integrations (recorder, REST API, automation engine) and an `automations.yaml` with three simple automations. Most integration tests run against this fixture.

**`faulty/`** exists specifically for testing error handling. Its `automations.yaml` contains a Jinja template that references a non-existent sensor, a disabled automation, and one working automation for comparison. Tests using the faulty fixture call `getFaultyHA(t)` to get a lazily-initialized container for that fixture — it is only started if a faulty test actually runs.

**`realistic/`** is modelled after a real HA installation. It includes template sensors, input helpers, a configured `system_log`, and 11 diverse automations (door lights, climate schedules, humidity-based ventilation, morning and night routines, a power spike alert, guest and vacation mode automations, and one deliberately disabled legacy automation). Before the realistic tests run, entity history is seeded by calling HA's service API directly — this allows the `ent hist` and `ent anomalies` commands to be tested against data that has meaningful variance.

### Golden files

Some hactl commands produce output that is hard to check with a simple string assertion — a formatted table, for example, where the exact column widths or the precise ordering of rows should not change accidentally. For these, the test suite uses golden files.

A golden file is a committed snapshot of what the command's output should look like. The test runs the command, sanitizes dynamic values (timestamps, HA version strings, random port numbers), and compares the result against the committed file. If they differ, the test fails.

Golden files live in `testdata/golden/`. They are ordinary text files, checked into source control, so any change to them shows up in a pull request diff and can be reviewed deliberately.

When intentional changes to output format are made, the golden files need to be regenerated:

```bash
HACTL_UPDATE_GOLDEN=1 make test-int
```

This runs all integration tests but writes the actual output back to the golden files instead of comparing against them. After regenerating, the diff in the PR shows exactly what changed in the output, and a reviewer can decide whether the change was intentional.

### What the integration tests cover

Almost every hactl command has a corresponding integration test file:

| Command area | Test file | What it checks |
|---|---|---|
| `health` | `health_test.go` | API status, config retrieval, JSON output, error log count |
| `auto ls/show` | `auto_test.go` | Listing automations, JSON schema, label/pattern filtering |
| `auto diff/apply/rollback` | `write_test.go` | Full write cycle: diff → dry-run → apply → rollback |
| `ent ls/show/hist/anomalies` | `ent_test.go` | Entity listing, domain filter, history, WebSocket |
| `tpl` | `tpl_test.go` | Template evaluation via the real Jinja engine |
| `log` | `log_test.go` | Log retrieval, component filter |
| `trace show` | `trace_test.go` | Condensed and full trace output, trigger analysis |
| `cache status/refresh/clear` | `cache_test.go` | Cache lifecycle |
| `changes` | `changes_test.go` | Change history by time range |
| `issues` | `issues_test.go` | Issue reporting from HA |
| `cc` | `cc_test.go` | Custom component commands |
| `svc` | `svc_test.go` | Service calls, `@file` argument support |
| `script ls/show` | `script_test.go` | Script listing, pattern matching |
| `version` | `version_test.go` | Version string format |
| `area/floor/label ls` | `registry_test.go` | Registry reads and label create |
| `dash` | `dash_test.go` | Dashboard CRUD, Lovelace card creation/deletion |
| `flow` | `flow_test.go` | Config entry flows, domain filter |
| Error paths | `error_test.go` | Invalid input, missing resources |
| Faulty fixture | `faulty_test.go` | Error handling with broken templates and disabled automations |
| Realistic fixture | `realistic_test.go` | Real-world config, WebSocket logs, seeded history |
| HA API contract | `contract_test.go` | Schema compliance for 8 HA REST/WebSocket endpoints |
| Golden snapshots | `golden_capture_test.go` | Output format stability |

Approximately 202 integration tests cover these areas.

### Contract tests

Eight tests in `contract_test.go` verify that the HA API behaves the way hactl expects it to. They check the shape of REST responses and WebSocket messages — field names, types, required presence of certain keys. If a future HA release renames a field or changes a response structure, one of these tests will fail before hactl's own logic breaks. The contract tests are part of the integration suite (same `-tags=integration` build tag) but are worth calling out separately because their purpose is different: they protect against upstream changes, not bugs in hactl itself.

---

## Layer 3: Companion Tests

The companion is an optional sidecar service that gives hactl direct filesystem access to the HA configuration directory, which is needed for write-path operations when hactl is not running on the same host as HA. The companion tests verify that this service works correctly and securely.

Because the companion needs to run alongside HA on a shared volume, these tests use Docker Compose rather than testcontainers. The compose file starts HA stable and the companion image together, mounts a shared `ha-config` volume, seeds it with YAML files, and then runs 16 tests:

- **CRUD operations**: writing, reading, and listing config files through the companion API
- **Security**: attempts to read files outside the config directory (path traversal) and requests for sensitive files (secrets, tokens) are verified to fail
- **Discovery**: the mechanism by which hactl automatically finds a companion on the same HA host
- **OpenAPI contract**: three tests that validate the companion's API responses against its published OpenAPI schema

To run:

```bash
make test-companion
# equivalent: go test -tags=companion -v -count=1 -timeout 300s ./internal/companiontest/...
```

---

## Running Tests Locally

The only hard prerequisite is a running Docker daemon. You can verify this with:

```bash
docker info
```

| Goal | Command | Docker needed | Approximate time |
|---|---|---|---|
| Quick sanity check | `make test` | No | ~5 seconds |
| Full test suite | `make test-int` | Yes | ~2 min first run, ~60s cached |
| Companion tests | `make test-companion` | Yes | ~5 minutes |
| Regenerate golden files | `HACTL_UPDATE_GOLDEN=1 make test-int` | Yes | ~2 min |
| Test against a specific HA version | `HACTL_HA_IMAGE=ghcr.io/home-assistant/home-assistant:2026.3 make test-int` | Yes | ~2 min |

**A common mistake**: running `go test ./...` without `-tags=integration` silently skips all integration tests. The output will show only unit tests passing, which looks like a clean run but leaves most of the test suite untouched. Always use `make test-int` when you want the full picture.

**Troubleshooting**:
- *Container fails to start*: Docker must be running, and the first pull requires a network connection.
- *Tests time out*: If your machine is slow, add a longer timeout: `go test -tags=integration -timeout 600s ./internal/integration/`.
- *Fixture change not picked up*: HA reads its config at startup. If you change a fixture file, the container must be restarted, which happens automatically when you re-run the tests.
- *Orphaned containers*: testcontainers runs a Ryuk sidecar that automatically removes test containers even if the test process crashes.

---

## CI/CD Enforcement

The test suite only works as a quality gate if it runs automatically on every change. hactl uses GitHub Actions for this. The workflow is defined in [`.github/workflows/ci.yml`](.github/workflows/ci.yml) and runs on every push to `main` and every pull request targeting `main`.

The pipeline has five jobs, all running in parallel:

**Lint** runs `golangci-lint` with a strict configuration (version 2 format). It checks for error handling issues, code style, security-sensitive patterns (`gosec`), and several other linters. A linting failure blocks merge.

**Unit Tests** runs `make test` on a fresh checkout. This is fast and provides immediate feedback on basic correctness.

**Integration Tests** is where most of the work happens. It runs `make test-int` three times, in parallel, against three different versions of Home Assistant:

- `stable` — the current stable release
- `prev` — the previous month's release (computed dynamically at runtime as `YYYY.M`)
- `dev` — the HA development build

The `stable` and `prev` runs are required: a failure in either one blocks the pull request. The `dev` run is non-blocking — if HA dev introduces a breaking API change overnight, it shows up as a warning in the CI output rather than blocking a merge. This gives us advance notice of upcoming HA changes without making every PR depend on the stability of a pre-release build.

**Vulnerability Check** runs `govulncheck` against the Go module graph. It checks known CVEs in the Go vulnerability database. A vulnerability finding in a direct dependency blocks merge.

**Companion Tests** runs `make test-companion`. Same rules as unit tests — failure blocks merge.

Beyond `ci.yml`, there are two further automated checks:

**CodeQL** (`.github/workflows/codeql.yml`) runs a static security analysis on every push and pull request, and also on a weekly schedule. It looks for classes of bugs — SQL injection patterns, improper input handling, and similar — that static type checking does not catch. Findings appear as code scanning alerts in the repository.

**Dependabot** (`.github/dependabot.yml`) opens pull requests weekly for Go module updates and GitHub Actions version bumps. This keeps the dependency graph fresh without manual bookkeeping.

**Branch protection** requires that all required CI checks pass before a pull request can be merged, and that at least one reviewer approves it.

---

## What Is Covered

The table below summarizes the current coverage across hactl's features. "Unit" means there are unit tests; "E2E" means the feature is exercised by integration tests against a real HA instance; "Contract" means there are schema-compliance tests for the underlying API.

| Feature area | Unit | E2E | Contract |
|---|---|---|---|
| `health` command | ✓ | ✓ | ✓ |
| `auto ls/show` | ✓ | ✓ | ✓ |
| `auto diff/apply/rollback` | ✓ | ✓ | — |
| `ent ls/show` | ✓ | ✓ | ✓ |
| `ent hist` / `ent anomalies` | ✓ | ✓ | — |
| `tpl` | ✓ | ✓ | — |
| `log` | — | ✓ | ✓ |
| `trace show` | ✓ | ✓ | — |
| `cache status/refresh/clear` | ✓ | ✓ | — |
| `changes` | — | ✓ | — |
| `issues` | — | ✓ | — |
| `svc` | ✓ | ✓ | — |
| `script ls/show` | — | ✓ | — |
| `cc` (custom components) | — | ✓ | — |
| `area/floor/label` | — | ✓ | — |
| `dash` | ✓ | ✓ | — |
| `flow` | ✓ | ✓ | — |
| `version` | ✓ | ✓ | — |
| Output formatting | ✓ | — | — |
| Config loading | ✓ | — | — |
| Filesystem cache | ✓ | — | — |
| Trace analysis algorithms | ✓ | — | — |
| Companion CRUD + security | — | — | ✓ (companion) |
| Companion discovery | — | — | ✓ (companion) |
| Error paths / bad input | ✓ | ✓ | — |
| Write safety (dry-run) | ✓ | ✓ | — |

---

## Honest Gaps

No test suite is complete, and this one is no exception. The following areas are not well covered, and we think it is worth being explicit about them.

**`rtfm` command**: This command simply prints the embedded manual to stdout. It is not currently tested. Because it only reads an embedded file and writes to an output writer, the risk of breakage is low — but it is still an untested code path.

**Cross-platform CI**: All CI jobs run on Ubuntu. hactl ships binaries for Linux, macOS, and Windows, and the Go code is written to be portable, but the test suite itself is not run on macOS or Windows in CI. Platform-specific issues (path separator behaviour, file permission semantics, line-ending handling) would not be caught until a user reports them.

**Network failure resilience**: The HTTP client's retry logic is unit-tested, but there are no integration tests that simulate a HA instance going unreachable mid-operation, returning malformed JSON, or closing a WebSocket connection unexpectedly. These code paths exist and have been written defensively, but they are exercised only by unit tests with a simple in-process stub, not by a real network.

**Auth token expiry and revocation**: The tests always use a freshly minted long-lived token. The behaviour when a token expires, is revoked, or is replaced by a newer one is not tested.

**Concurrent invocations**: Two hactl processes running against the same HA instance at the same time are not tested. The cache uses filesystem operations that are not protected by a file lock, which could cause corruption under concurrent access.

**Large-scale data**: The test fixtures are intentionally small. A real HA installation with hundreds of entities, thousands of history entries, or automations that produce complex nested traces may expose performance or formatting issues that the test suite would not catch.

**Systematic `--dry-run` coverage**: The `auto apply --dry-run` path is tested, but not every write-path command has an explicit test that verifies the dry-run flag prevents any mutation.

---

## Quick Reference

```bash
# Prerequisites
docker info                          # Docker must be running

# Local development
make test                            # Unit tests only (~5s, no Docker)
make test-int                        # Full suite (~2 min, Docker required)
make test-companion                  # Companion tests (~5 min, Docker required)

# Golden file maintenance
HACTL_UPDATE_GOLDEN=1 make test-int  # Regenerate golden snapshots

# Test against a specific HA version
HACTL_HA_IMAGE=ghcr.io/home-assistant/home-assistant:2026.3 make test-int

# Lint
make lint
```

The CI pipeline enforces all of the above on every pull request. If the CI badge at the top of the README is green, all required checks have passed against the current `main` branch.
