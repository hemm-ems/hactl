# hactl – Testing Guide

This document explains how hactl is tested, what the tests actually verify, how you can run them yourself, and where we know the coverage is thin. It is written for someone new to the project — or to software testing in general — so it tries to explain the *why* at each step, not just the *what*.

Testing a command-line tool that talks to Home Assistant is harder than testing a pure library. The tool's job is to translate real HA state into useful output, so testing against a mock would only confirm that the code calls the mock correctly — not that it actually works. This is why hactl's test suite goes to some lengths to run against a real, live HA instance for the tests that matter most.

---

## The Four Layers

hactl's tests are organized into four layers, each with a different scope and a different cost.

**Unit tests** are the fastest and cheapest. They cover individual functions: parsing logic, formatting, anomaly detection algorithms, cache storage, config file loading. No network, no Docker, no HA. They run in a few seconds and serve as a quick sanity check.

**Integration tests** are the main event. They start a real Home Assistant instance in a Docker container, run hactl commands against it, and check the output. These tests are slower (a couple of minutes the first time, faster once Docker has cached the image), but they are the ones that tell us whether the tool actually works with real HA.

**Companion tests** cover the optional companion service — a small sidecar that gives hactl filesystem write access to the HA config directory. They use Docker Compose to stand up both HA and the companion together, then exercise the companion's API for CRUD operations, security boundaries, and service discovery.

**Discovery tests** cover companion discovery and Ingress authentication — the production path the companion tests deliberately bypass by pre-populating `COMPANION_URL`. That bypass is what let two production bugs ship undetected, which is why this layer exists separately.

Each layer has its own `make` target, and each layer is also enforced independently in CI. You can think of the layers as a pyramid: many small unit tests at the base, a broad set of integration tests in the middle, and the two focused companion suites at the top.

A layer only gates what its tests actually assert, which is a separate question from how many there are — see [Writing a Test That Actually Gates Something](#writing-a-test-that-actually-gates-something).

### This document states no test counts

It used to state four, and all four had drifted — in both directions, twice. Correcting them turned out to be the harder half of the problem: three plausible ways of counting by hand gave three different answers, because each silently answers a different question. Grepping the files that carry `//go:build integration` misses a tagged file that lives outside `internal/integration/`; counting the directory instead includes its `TestMain`, which is not a test, and its untagged files, which are not in the tier. There was no stale number to simply refresh — there was no agreed number to refresh it to.

The counts are therefore derived on demand rather than written down:

```bash
make testcount
```

It prints one `<tier> <count>` line per tier — `unit`, `integration`, `companion`, `discovery` — so the output reads the same to a person and to a script, and it needs no Docker. The numbers come from the same scan the assertion floor uses (see [Honest Gaps](#honest-gaps)), which counts exactly the functions `go test` would run and nothing else. `dev/testcount.sh` explains why that scan is the oracle, and why `go test -tags=<tier> -list` — the obvious candidate, since it reports what would run — is not one.

Wherever this document would otherwise have quoted a number about itself, it names the gate that derives it instead. A number nobody can regenerate is indistinguishable from a number nobody has checked.

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

Fixtures are directories in `testdata/fixtures/` that are mounted as the HA config directory when a container starts. That directory is the list — a test selects one with `hatest.WithFixture("<name>")` — and each one below exists for a reason worth knowing:

**`basic/`** is the default. It contains a minimal `configuration.yaml` that enables the standard integrations (recorder, REST API, automation engine) and an `automations.yaml` with a few simple automations. Most integration tests run against this fixture.

**`faulty/`** exists specifically for testing error handling. Its `automations.yaml` contains a Jinja template that references a non-existent sensor, a disabled automation, and one working automation for comparison. Tests using the faulty fixture call `getFaultyHA(t)` to get a lazily-initialized container for that fixture — it is only started if a faulty test actually runs.

**`realistic/`** is modelled after a real HA installation. It includes template sensors, input helpers, a configured `system_log`, and a spread of diverse automations (door lights, climate schedules, humidity-based ventilation, morning and night routines, a power spike alert, guest and vacation mode automations, and one deliberately disabled legacy automation). Before the realistic tests run, entity history is seeded by calling HA's service API directly — this allows the `ent hist` and `ent anomalies` commands to be tested against data that has meaningful variance.

**`oracle/`** exists for invariant H-8, distinguishability: every identifier that *can* differ *does* differ, and every automation is reachable by a real state trigger, so it actually produces traces. The older fixtures already had divergent automation ids, but nothing ever fired them, so the divergence was inert and a real defect — traces keyed by `object_id` instead of the config id — stayed invisible through the entire integration tier. It also enables `demo:`, which is the only supported way to get real *devices*: HA has no "create device" command, and an entity's effective area falls back to its device's area, so that path cannot be tested without them.

**`lovelace-yaml/`** pins the default dashboard to YAML mode (`lovelace: {mode: yaml}` plus a `ui-lovelace.yaml`). It is an oracle fixture: `lovelace_oracle_test.go` asks a live HA how a YAML-mode default actually answers — retrievable, not writable, and listed under the reserved slug `lovelace` — rather than letting hactl assume. That assumption is where a real defect came from, so the fixture exists to keep the answer coming from HA on every image the tier runs.

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
| HA API contract | `contract_test.go` | Field-level schema compliance for the HA REST and WebSocket endpoints hactl decodes |
| Golden snapshots | `golden_capture_test.go` | Output format stability |

The table is a map of where to look, not a census: `internal/integration/` holds more files than it lists, including the oracle suites described under [Fixtures](#fixtures). `make testcount` reports the tier's size.

### Contract tests

The tests in `contract_test.go` verify that the HA API behaves the way hactl expects it to. Each calls the endpoint it names and asserts the shape of the response it decodes — field names, types, required presence of certain keys — against HA's own answer, not merely that a command exited zero. If a future HA release renames a field or changes a response structure, one of these tests fails before hactl's own logic breaks. The contract tests are part of the integration suite (same `-tags=integration` build tag) but are worth calling out separately because their purpose is different: they protect against upstream changes, not bugs in hactl itself. The companion seam has its own field-level contract in the unit tier (`internal/companion/contract_conformance_test.go`, invariant H-13).

---

## Layer 3: Companion Tests

The companion is an optional sidecar service that gives hactl direct filesystem access to the HA configuration directory, which is needed for write-path operations when hactl is not running on the same host as HA. The companion tests verify that this service works correctly and securely.

Because the companion needs to run alongside HA on a shared volume, these tests use Docker Compose rather than testcontainers. The compose file starts HA stable and the companion image together, mounts a shared `ha-config` volume, seeds it with YAML files, and then runs the tier, which covers:

- **CRUD operations**: writing, reading, and listing config files through the companion API
- **Security**: attempts to read files outside the config directory (path traversal) and requests for sensitive files (secrets, tokens) are verified to fail
- **OpenAPI contract**: validation of the companion's API responses against its published OpenAPI schema, including a check that the spec and the client's endpoint set have not drifted apart

To run:

```bash
make test-companion
# equivalent: go test -tags=companion -v -count=1 -timeout 300s ./internal/companiontest/...
```

This layer intentionally bypasses companion **discovery** — the test writes the resolved `COMPANION_URL` directly into the test `.env` so the contract tests can focus on the HTTP API. The production discovery path (Supervisor WS proxy + Ingress session) is covered by a separate harness, below.

---

## Layer 4: Discovery + Ingress Auth

A second companion-related harness lives in `internal/companiontest_discovery/` (build tag `companion_discovery`). It exists because the original companion test harness above hides discovery and ingress auth behind a pre-populated `COMPANION_URL` — exactly the bypass that let two production bugs ship undetected (a wrong WS namespace and the wrong Ingress auth mechanism; see [companion-discovery-fix-plan.md](../../companion-discovery-fix-plan.md)).

The harness combines:

- A **real Companion container** started via Docker Compose (no shared volume — discovery does not need one).
- An **in-process Fake Supervisor** (`fake_supervisor.go`) running as a Go HTTP+WS server on a free 127.0.0.1 port. It speaks the subset of the HA WS API that hactl actually uses:
  - `supervisor/api` proxy for `/addons`, `/addons/<slug>/info`, `/info`, and `/ingress/session` (POST).
  - Legacy names (`hassio/api`, `hassio/addon/info`) explicitly return `Unknown command.` so a regression to the wrong namespace fails loudly.
- An **HTTP reverse-proxy** on the fake at the deterministic Ingress prefix (`/api/hassio_ingress/fakeid/`) that strips the prefix, adds the `X-Ingress-Path` header the Companion's auth middleware needs, and forwards to the real Companion. This mimics HA Core's `HassIOIngress` view, which is `requires_auth = False` and proxies straight through.

Tests assert:

- Discovery enumerates `/addons` via the Supervisor proxy and matches the companion by slug (bare, repo-prefixed, or name fallback).
- The resolved URL actually serves the Companion's `/v1/health` end-to-end (full Discovery → HTTP round-trip).
- Ingress auth: with the cookie wired up via `WithIngressAuth(wsClient)`, calls succeed; without it, the fake's `requireSession` enforcement returns 401 (proves the cookie is the only thing authenticating).
- The cached `ingress_session` is reused across requests and refreshed on 401 (simulated by the fake's `InvalidateSessions`).

To run:

```bash
make test-int-discovery
# equivalent: go test -tags=companion_discovery -v -count=1 -timeout 300s ./internal/companiontest_discovery/...
```

The harness boots in roughly 15 seconds (mostly the one-time Companion image build), then runs its tests in under three.

---

## Writing a Test That Actually Gates Something

This section exists because of a specific failure. `hactl trace show` rendered
every automation run — including failures — as a bare `  .    PASS` with no
steps, against real Home Assistant, for months. During that time the suite had
over a thousand unit tests and more than two hundred integration tests, and all
of them were green. A separate audit then found that the entire automation write
path could be replaced with a no-op and both tiers would still pass.

Neither was a gap in *how much* was tested. Both were failures in *what the tests
asserted*. The rules below are the ones that would have caught them. Each is
written as a rule rather than a suggestion because each has already been violated
in this repository, in code review, by people who were paying attention.

### Watch every test fail before you trust it

Write the test first. Run it against the unfixed code. **Observe the failure, and
put the failure output in the pull request.** Only then write the fix.

A test that has never failed is not evidence. It might assert the wrong thing, it
might assert nothing, or it might be passing for a reason unrelated to the
behaviour it claims to cover. There is no way to tell from reading it — the only
way to know a test constrains something is to see it react when that thing is
wrong.

The same applies when you change a test's subject: if you touch what a test
covers, break the code deliberately once and confirm the test notices.

Applied to the write-path fix, this took one minute per test and produced three
quotable failures, one of which revealed that Home Assistant rewrites an
automation's schema on write — something none of us knew and the previous test
could never have surfaced.

### Assert on what the system did, never on what hactl said it did

`applied: <id>` is printed unconditionally once the write call returns `nil`. So
is `called <domain>.<service>`, and `traces refreshed`, and `cache cleared`.
Asserting that one of these strings appears in stdout proves that hactl reached
the end of a function. It says nothing about Home Assistant.

For anything that mutates state, read the state back from HA and compare it. See
`internal/integration/write_roundtrip_test.go`. This is invariant H-4, and it is
the reason a stubbed `UpdateAutomationConfig` is now caught.

### "It did not crash" is not an assertion

These shapes have all shipped in this repository and none of them can fail for
the reason the test exists:

```go
out := runHactl(t, "ent", "ls", "--pattern", "person.*")
_ = out                                    // asserts nothing at all

assertNotContains(t, out, "panic")         // the whole body of TestEntRelated

if len(traceOut) == 0 { t.Error(...) }     // "" is the only failing value
```

Assert something that is true of a correct result and false of a plausible wrong
one. If you cannot think of such a value, that is a signal the test is not worth
writing yet — say so in the PR rather than committing a placeholder.

### Beware `||` between two assertions

The condensed-trace test asserted `hasStep || hasResult`. The broken output was
`  .    PASS`, which contains `PASS`, so the disjunction held and the test stayed
green through an entire release. The bug lived in the conjunction.

If a correct result must have *both* properties, assert both. A disjunction is
only right when the alternatives are genuinely interchangeable.

### Fixtures are recordings, not drawings

All four trace fixtures were hand-written in the shape the parser expected. The
parser's shape was wrong. The fixtures and the code agreed with each other and
disagreed with Home Assistant, and the tests confirmed the agreement.

Capture fixtures from a live instance. If you must write one by hand, compare it
against a real payload first and say in the PR that you did. Values invented to
look plausible — `run_id: "run-condfail-003"` where HA emits 32 hex characters —
are how a fixture drifts from reality without anyone noticing.

The same trap exists on the companion side, where a matcher was tested against
YAML authored to the matcher's own model rather than against a real automation.

### Check that the suite can reach the state you are asserting on

Every trace in the integration suite is produced by `automation.trigger`, whose
`skip_condition` parameter defaults to `true`. The consequence: no integration
test had ever produced a condition step, or any outcome other than `finished`.
The failure-handling code was not weakly tested, it was **unreachable by
construction**, and no amount of stronger assertions would have helped.

When you add a test for an error path, verify the harness can actually produce
that error. If it cannot, fixing the harness is the task.

### Ask what a stub would do

Before finishing, ask: *if I replaced this function with `return nil`, or
`return ""`, or `return true`, would any test go red?*

If the answer is no, the behaviour is unprotected no matter what the coverage
report says. This question found forty-seven unprotected functions in one pass,
including the entire automation write path.

Two caveats, both learned here. The check is cheap enough to do by hand on the
function you are changing, and not worth automating across the tree. And passing
it is necessary rather than sufficient: `containsAutoID` had tests that killed
every stub of it, and was still wrong in a way that could overwrite one
automation with another's config. Surviving mutants prove tests are weak; dying
mutants do not prove code is right.

### Coverage percentages are not evidence here

Measured on this repository at the time of the audit:

| | Coverage | Reality |
|---|---|---|
| `overallResult` | **100.0%** | returned `PASS` for every run, for months |
| `findAutomationRelations` | **86.4%** | unreachable from `main`; only tests called it |

Coverage measures which lines executed. Every defect above executed. The CI
threshold is deliberately low (35%) and is a smoke check that the suite ran at
all — treat it as such, and never as a quality target. Raising it buys tests
written to move a number, which is how the assertion-free tests got here.

### Delete code the binary cannot reach

`findAutomationRelations` worked, had two passing tests, and had no callers
outside them — the command that used it had migrated to a weaker replacement, and
because its tests stayed green, nothing recorded that a capability had been lost.

`make deadcode` reports this class in about two seconds, and it is part of
`make gates` and of CI — it is a gate, not advice. It fails when a function is
unreachable from the binary and not listed in `dev/deadcode-allow.txt`, and it
fails just as loudly when a listed function *becomes* reachable again, so the
allowlist cannot rot into a rubber stamp.

Every allowlist line carries a class and a reason. `harness` means the function
exists to serve a test or a gate (`RunWithOutput` is how every CLI test drives
the cobra tree). `orphan` means product code with no path from `main` — a
standing defect, and the tests under it are green while proving nothing about
what a user can do. Read `dev/deadcode-allow.txt` for the current list rather
than trusting a count written here; today it is dominated by the whole
`internal/cache.Store` trace/log cache and by `companion.Client` methods that
no subcommand routes to.

When the gate flags something new, decide deliberately: wire it, or delete it
with its tests. Leaving it is how a test suite comes to certify behaviour the
product does not have.

---

## Running Tests Locally

**The Docker roundtrip is mandatory.** `make gates` is the only definition of
done: it runs lint in every build configuration, the reachability, assertion-floor
and surface-closure gates, the unit tier, and all three Docker tiers, and refuses
to start when Docker is not running rather than silently narrowing what was
verified. `make test` is the unit tier alone and is never acceptance — it starts
no Home Assistant, so it cannot see a wrong lookup key or a missing registry
fallback. Install the pre-push hook with `make hooks`.

The only hard prerequisite is a running Docker daemon. You can verify this with:

```bash
docker info
```

| Goal | Command | Docker needed | Approximate time |
|---|---|---|---|
| Quick sanity check | `make test` | No | ~5 seconds |
| **Everything (the only "done")** | **`make gates`** | **Yes** | **~4 min** |
| Full integration suite | `make test-int` | Yes | ~2 min first run, ~60s cached |
| Companion tests | `make test-companion` | Yes | ~5 minutes |
| Discovery + Ingress auth tests | `make test-int-discovery` | Yes | ~15 seconds (cached image) |
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

The jobs run in parallel; `ci.yml` is the list of them, and each corresponds to a `make` target a developer runs locally:

**Lint** runs `golangci-lint` with a strict configuration (version 2 format), once per build configuration — untagged and then each of the three test tags — so that no file behind a build tag is invisible to the linters. It checks for error handling issues, code style, security-sensitive patterns (`gosec`), and several other linters. A linting failure blocks merge.

**Reachability** runs `make deadcode`. It fails when a function is unreachable from the `hactl` binary and not on the recorded allowlist — see [Delete code the binary cannot reach](#delete-code-the-binary-cannot-reach).

**Unit Tests** runs `make test-assert-floor` as its own step and then `make test`, followed by the coverage threshold check. The assertion floor gets its own step because it judges the tagged Docker tiers too, from source, so it is worth its own red X rather than being buried in the unit tier's output. The surface closure gates are ordinary packages, so `make test` reaches them here; `make test-surface` differs only in printing each ledger.

**Integration Tests** is where most of the work happens. It runs `make test-int` three times, in parallel, against three different versions of Home Assistant:

- `stable` — the current stable release
- `prev` — the previous month's release (computed dynamically at runtime as `YYYY.M`)
- `dev` — the HA development build

The `stable` and `prev` runs are required: a failure in either one blocks the pull request. The `dev` run is non-blocking — if HA dev introduces a breaking API change overnight, it shows up as a warning in the CI output rather than blocking a merge. This gives us advance notice of upcoming HA changes without making every PR depend on the stability of a pre-release build.

**Vulnerability Check** runs `govulncheck` against the Go module graph. It checks known CVEs in the Go vulnerability database. A vulnerability finding in a direct dependency blocks merge.

**Companion Tests** runs `make test-companion` twice: once against the companion version the vendored OpenAPI spec pins, and once against the companion's `main`. The pinned leg is required; the `main` leg is advisory, for the same reason the HA `dev` leg is — it gives advance notice of an unreleased sidecar without making every PR depend on it.

**Discovery Tests** runs `make test-int-discovery` on the same two-leg matrix, with the same required/advisory split.

**All Gates Green** is an aggregating job that runs after the others and fails unless every required one succeeded. It exists so that "the Docker tiers were skipped" cannot look like a green run: a single required check on the branch protection rule cannot be satisfied by a subset.

The repository has further automation outside `ci.yml`:

**CodeQL** (`.github/workflows/codeql.yml`) runs a static security analysis on every push and pull request, and also on a weekly schedule. It looks for classes of bugs — SQL injection patterns, improper input handling, and similar — that static type checking does not catch. Findings appear as code scanning alerts in the repository.

**Dependabot** (`.github/dependabot.yml`) opens pull requests weekly for Go module updates and GitHub Actions version bumps. This keeps the dependency graph fresh without manual bookkeeping.

**Branch protection** requires that all required CI checks pass before a pull request can be merged, and that at least one reviewer approves it.

---

## What Is Covered

The table below summarizes the current coverage across hactl's features. "Unit" means there are unit tests; "E2E" means the feature is exercised by integration tests against a real HA instance; "Contract" means there are schema-compliance tests for the underlying API.

This table is maintained by hand, so it drifts — it has been wrong before, and a ✓ records only that *some* test touches the area, never that the test would fail if the feature broke. Both bugs that prompted the [testing rules above](#writing-a-test-that-actually-gates-something) sat in rows marked ✓ across all three columns. Read it as a map of where to look, not as an assurance.

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

**Assertion strength**: an audit in July 2026 found integration tests that contained no positive assertion at all — the strongest example being a test whose entire body was `out := runHactl(...)` followed by `_ = out`. It ran a real command against a real Home Assistant and threw the answer away.

That is now a gate rather than a finding, so this document does not quote how many such tests remain — the gate does, and it holds the number at zero. `make test-assert-floor` (invariant H-19, `internal/testaudit`) parses every test function in the repository from disk, including the build-tag-gated Docker tiers, and fails any whose body cannot reach a `t.Error`/`t.Fatal` guarded by something other than an error check. `if err != nil { t.Fatalf(...) }` does not count: every command that runs at all passes it. Neither does a `t.Skip`. A test that genuinely has nothing to observe must say so in its doc comment as `//test:no-assert <reason>`; the reason is mandatory, it is printed, and the directive itself fails the gate once the test starts asserting, so an exemption cannot outlive its cause. Run the target with `-v` for the per-tier tally.

What the floor does not measure is assertion *strength* — one `assertContains` on a header row passes it. A test count therefore remains an upper bound on what is gated, and a poor proxy for it, which is the other half of why this document states none.

**Script write path**: closed. `auto apply`/`rollback` (H-4), the dashboard and entity-registry families, and now the companion-backed families — `script create|apply|delete`, `tpl create|delete`, `helper create|delete` — all have round-trip gates that read back from HA (`internal/companiontest/write_config_test.go`, `make test-companion`). Deleting the backup, or the write, or the ghost cleanup from any of them fails a named test.

Closing it turned up three defects the client-level tests could not see, because they drove the companion's API and read back through the companion: `script delete`/`tpl delete` left the entity registered as an `unavailable` ghost (`auto delete` had always cleaned it up), and `tpl create`/`script create` discarded HA's reload confirmation, so both reported success for a definition HA might never have read. The rig had the same blind spot: HA's onboarding config !include's automations, scripts and scenes and nothing else, so the seeded `template.yaml` was never loaded at all.

**Dry-run previews resolve their target**: fixed. Write commands accepted a fabricated id and printed a confident "would do X" plan at exit 0, and `script create`/`helper create` never read the file they were about to send. Every preview now resolves first and fails where `--confirm` would (H-2). `--json` on a preview is no longer a no-op either: previews share one shape that renders as text or as an object stating `"dry_run": true`.

The scope of that fix was originally a hand-written list of the affected commands, and the list missed one: `auto apply`, whose preview prints `dry-run: no changes written` rather than the `dry-run: would …` the list had been grepped for. Which commands are affected is now derived instead of listed — `make test-surface` enumerates every `--confirm`-gated entrypoint and fails any that assembles a preview some way other than through the one renderer that consults `--json` (`dev/surfaces/preview.manifest`, ceiling 0).

Seven unit tests and one integration test asserted the old behaviour as correct and were inverted; `TestDashDeleteAgreesOnUnknownDashboard` replaces a pair that documented the disagreement without naming it — one asserted the confirmed run fails on an unknown dashboard, its neighbour asserted the dry run succeeds for the same argument.

**The timeseries cache is write-only**: `hactl ent hist` writes samples on every call and nothing ever reads them back — `TSStore.GetSamples`, `LatestSample` and `ClearEntity` have no production callers. `cache clear` and `cache status` now cover the file, but the read path it exists to serve does not exist.

**`ref replace` and the default dashboard**: `ref replace` reports `skipped: not storage-mode` for the default dashboard in every case, because it gates on a `lovelace/info` field HA does not emit. The correct gate is an open design question — HA exposes no read-only call that reports the default dashboard's config mode — so the current behaviour asserts a fact hactl never established. Tracked, not fixed.

**Wire-shape coverage**: the `contract_test.go` cases used to be *proxies* — they asserted a command succeeded, not that the payload had the expected shape, and a mismatched shape decodes to a zero value rather than an error, so a proxy could not detect one. Two of them named an endpoint they never called (`TestContract_AutomationConfigAPI` ran `auto ls`; `TestContract_WebSocket_TraceList` ran `auto ls --json`), and two asserted nothing at all. Those now call the endpoint they name and assert the decoded fields against HA's own answer, and the field shapes behind the previously unguarded surfaces — `trace/get` internals (a real errored run must decode to a fail outcome, a finished run to pass), `/api/logbook` context attribution fields, and the `/api/diagnostics/config_entry/{id}` envelope — are pinned too. The remaining gap is the HA surfaces no contract or oracle test drives at all. How large that surface is, this document does not say — two gates derive it and share one list of packages so that nothing can sit between them. H-14's sweep takes every `json.Unmarshal` inside `degeneracy.WirePackages` and forces each site to call `degeneracy.Check` or carry a written reason; the decode surface takes every decode the sweep cannot see — yaml unmarshals, decoder constructions, WebSocket `ReadJSON`, and any json decode outside those packages — and requires each to be dispositioned as proven, exempt or debt in `dev/surfaces/decode.manifest`. There is no third place for a decode to sit, so a new one fails a gate the day it appears rather than needing to be noticed and added to a tally here. On the companion seam the field-level contract is now complete and enforced in both directions (invariant H-13, `internal/companion/contract_conformance_test.go`); H-7 still backstops the trace decode with the UNPARSED poison.

---

## Quick Reference

```bash
# Prerequisites
docker info                          # Docker must be running

# Local development
make test                            # Unit tests only (~5s, no Docker)
make test-int                        # Full suite (~2 min, Docker required)
make test-companion                  # Companion tests (~5 min, Docker required)

# How many tests each tier has (derived, no Docker)
make testcount

# Golden file maintenance
HACTL_UPDATE_GOLDEN=1 make test-int  # Regenerate golden snapshots

# Test against a specific HA version
HACTL_HA_IMAGE=ghcr.io/home-assistant/home-assistant:2026.3 make test-int

# Lint
make lint
```

The CI pipeline enforces all of the above on every pull request. If the CI badge at the top of the README is green, all required checks have passed against the current `main` branch.
