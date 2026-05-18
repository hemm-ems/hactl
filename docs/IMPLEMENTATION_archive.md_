# hactl – Implementation Status

> Live document updated per session. Phases 0–13 complete.  
> **E2E VERIFIED**: All integration tests pass against real HA containers (Docker).  
> Companion v2 client verified against GHCR image (`ghcr.io/swifty99/hactl_companion:0.2`).

## Core Principles

- Plan → Code → Test. No merging without passing lint + tests.
- `golangci-lint run ./...` = 0 findings always.
- slog logging from start. Security first: no secrets in repo, write-path always dry-run capable.

---

## ✓ Completed Phases 0–8

| Phase | What | Status |
|-------|------|--------|
| 0 | Skeleton, cobra root, lint, CI | ✓ |
| 1 | Config discovery, REST client, `health` | ✓ |
| 2 | Formatters, stable IDs, WebSocket, `auto ls/show` | ✓ |
| 3 | Trace condenser, log deduper, SQLite cache | ✓ |
| 4 | Entity browsing, history resampling, anomalies, templates | ✓ |
| 5 | Write-path (diff/apply/rollback), integration tests | ✓ |
| 6 | Full E2E validation, golden files, faulty fixture | ✓ |
| 7 | WS system_log fallback, health --json, realistic fixture | ✓ |
| 8 | script run, label filter, domain filter, compact tables, script cache, stats | ✓ |
| 9 | Registry API, attribute history, relationship crawler, discovery commands, write ops | ✓ |
| 10 | Lovelace dashboard CLI (`dash`), LLM frontend design skill | ✓ |
| 11 | LLM-safe output: `[~N tok]` header, `--tokensmax` cap, `log show` field parsing | ✓ |
| 12 | Token policy polish, CLI consistency streamlining | ✓ |
| 13 | Companion v2 client (YAML CRUD) + Docker integration tests | ✓ |
| 14 | Config CLI + auto-discovery | ☐ |

---

## Current Metrics (2026-04-28)

| Metric | Value |
|--------|-------|
| Unit tests | 224 ✓ (200 existing + 24 companion client) |
| Integration tests | 202 ✓ (0 failures) |
| Companion integration tests | 16 ✓ (13 endpoint + 3 contract) |
| Lint findings | 0 ✓ |
| Build | `go build ./...` ✓ |
| Commands | 19 groups (health, auto, script, trace, log, cache, ent, cc, tpl, issues, changes, rollback, version, label, area, floor, svc, dash, rtfm) |
| Fixtures | basic, faulty, realistic (3 HA containers) |
| Docker | Docker Desktop, testcontainers-go v0.42.0 |
| Companion | v0.2.0 (`ghcr.io/swifty99/hactl_companion:0.2`) |
| Last verified | HA 2026.4.3, full E2E with Docker |

---

## E2E Testing Notes

### Docker Setup (Windows)
- Docker Desktop must be running: `Start-Process "C:\Program Files\Docker\Docker\Docker Desktop.exe"`
- Wait for daemon: poll `docker info` until "Server Version" appears
- Pipe: `npipe:////./pipe/docker_engine`

### Bugs Fixed During E2E Verification

1. **Cobra --help flag leak** (`internal/cmd/root.go`): When any test called `--help`, cobra's internal bool flag stayed `true` on that subcommand's FlagSet across subsequent `RunWithOutput()` calls. Fixed with recursive `resetCobraFlags()` using `pflag.VisitAll` to reset every flag including cobra's built-in `--help`.

2. **Trace trigger array unmarshal** (`internal/analyze/trace.go`): HA 2026.x returns trace trigger as JSON array (`["state"]`) not string. Changed `RawTraceMeta.Trigger` from `string` to `json.RawMessage` with `parseTrigger()` helper that handles string, array, or raw fallback.

3. **Script run nonexistent entity** (`internal/cmd/script.go`): HA service API returns 200 even for nonexistent script entities. Added `GetState()` pre-check before `CallService`.

4. **Realistic fixture timing** (`internal/integration/realistic_test.go`): HA container reports ready when `/api/onboarding` responds, but may still be in `NOT_RUNNING` state. Added `waitForRunning()` that polls `/api/config` until `state=RUNNING` before seeding history.

5. **Deprecated group.set service** (`internal/integration/svc_test.go`): `group.set` removed in newer HA. Replaced with `persistent_notification.create`.

6. **Condensed trace step type** (`internal/integration/realistic_test.go`): Test checked for `"condition"` but the step type constant is `"cond"`. Also accept PASS/FAIL result header as valid output.

---

## Known Bugs

*(none)*

---

## ✓ Phase 12: Token Policy Polish & CLI Consistency

### 12.1 ✓ Hint specificity in `applyTokenPolicy`
Command-specific truncation hints via `truncationHint(cmdPath)` in `root.go`. Maps:
- `hactl log` → `--component`, `--errors`, `--unique`
- `hactl ent ls` → `--domain`, `--area`, `--label`, `--pattern`
- `hactl auto ls` / `script ls` → `--pattern`, `--label`, `--failing`
- `hactl ent show --full` → remove `--full`
- fallback → generic `--tokensmax=0` hint

### 12.2 ✓ UTF-8 boundary safety in truncation
`applyTokenPolicy` now walks backward with `utf8.Valid()` after computing byte limit, preventing mid-rune splits. Unit test with multi-byte `€` character.

### 12.3 ✓ `ent show` default attribute summary
When `!flagFull` and hidden attributes exist, prints `attributes: N total; use --full to see all`.

### 12.4 ✓ CLI Consistency Streamlining

**Bug fix:**
- `resetSubcommandFlags()` now resets `flagLogErrors`, `flagLogUnique`, `flagLogComponent` (were missing, causing test pollution)

**Flag unification:**
- `auto ls --tag` renamed to `--label` (matches `ent ls --label`; both filter by HA registry labels)
- `script ls` gained `--label` and `--failing` flags (feature parity with `auto ls`)
- `--pattern` flag descriptions normalized: `"filter by name (substring or glob, e.g. <example>)"`

**Compact rendering:**
- `Compact: true` applied to all table renders: `cc ls`, `cc logs`, `changes`, `dash ls`, `dash resources`, `issues`, `ent hist`, `ent anomalies`, `log` (both deduped and regular)

**Cosmetic fixes:**
- `trace` Long description: "automation runs" → "automation and script runs"
- `health` Long description: "Displays" → "Display" (imperative tense)
- `cc ls` empty-state: "no custom components found" → "no custom components"

### 12.5 Close GitHub issue #4
**Checklist before PR:**
- [x] `golangci-lint run ./...` = 0 findings
- [x] Integration tests green (202 pass)
- [x] Golden files updated (`testdata/golden/*.txt`)
- [x] IMPLEMENTATION.md updated
- [x] `docs/manual.md` updated (--tag → --label, new script flags)
- [x] PR created & issue #4 closed

---

## ✓ Phase 13: Companion API Client v2 + Docker Integration Tests

### 13.1 ✓ Companion Go Client (`internal/companion/`)
- `types.go` — Response structs: `HealthResponse`, `ConfigFilesResponse`, `ConfigFileResponse`, `ConfigBlockResponse`, `ConfigWriteResponse`, `TemplateDefinition`, `TemplatesResponse`, `TemplateResponse`, `TemplateCreateResponse`, `ScriptDefinition`, `ScriptsResponse`, `ScriptResponse`, `ScriptCreateResponse`, `AutomationDefinition`, `AutomationsResponse`, `AutomationResponse`, `AutomationCreateResponse`, `ConfigDeleteResponse`
- `client.go` — HTTP client: Bearer token auth, 3 retries with backoff on 5xx, 30s timeout. 15 typed CRUD methods + config file methods + health.
- `client_test.go` — 24 unit tests with `httptest.NewServer` mocks

### 13.2 ✓ Docker Integration Tests (`internal/companiontest/`)
- `docker-compose.yaml` — HA stable + companion (`ghcr.io/swifty99/hactl_companion:0.2`) on shared network + named volume
- `main_test.go` — TestMain lifecycle: pull GHCR image (fallback: local build) → compose up → poll HA → headless onboarding → poll companion → seed config files → run tests → compose down
- `companion_test.go` — 13 live endpoint tests: Health, ListConfigFiles, ReadConfigFile, SecretsYamlDenied, PathTraversal, DryRun, WriteNewFile, ListTemplates, CreateAndGetTemplate, ListScriptDefs, CreateAndGetScript, ListAutomationDefs, CreateAndGetAutomation
- `contract_test.go` — 3 OpenAPI contract tests: spec validation, all 10 paths covered, correct HTTP methods (20 operations)

### 13.3 ✓ OpenAPI Contract (v2)
- Vendored from companion repo → `testdata/companion-v1.yaml`
- 10 paths / 20 operations (health + config CRUD + template/script/automation CRUD)
- `!include` resolution on all reads (`resolve=true` param)

### 13.4 ✓ Makefile
- `test-companion` target: `go test -tags=companion -v -count=1 -timeout 300s ./internal/companiontest/...`

### 13.5 Run Notes
- Build tag `companion` (separate from `integration`) — different Docker lifecycle
- Companion pulled from GHCR (`ghcr.io/swifty99/hactl_companion:0.2`); local build fallback if pull fails
- HA onboarding duplicated from `hatest.go` (keeps packages independent, no testcontainers dep)
- Config files seeded before tests (template.yaml, scripts.yaml, automations.yaml)
- Full run: ~25s (pull cached + compose up + HA boot 10s + onboard 3s + seed 5s + tests <1s + teardown 3s)

---

## Phase 14: Config CLI + Auto-Discovery (Next)

> Companion v2 client + integration tests completed in Phase 13 rewrite.  
> Remaining: wire CLI commands to companion client, add auto-discovery.

### 14.1 ✓ Companion Client v2
Completed. 15 typed methods (template/script/automation CRUD), 24 unit tests, 16 Docker integration tests.

### 14.2 Companion Auto-Discovery
- Priority: explicit `COMPANION_URL` → WS `hassio/addon/info` → ingress URL
- Graceful fallback when companion unavailable

### 14.3 New hactl Commands (no companion)
- `hactl config entries` — WS `config/entries`
- `hactl config check` — existing `WSClient.CheckConfig()`
- `hactl config reload <domain>` — `CallService(domain, "reload")`

### 14.4 New hactl Commands (companion)
- `hactl tpl ls/show/edit/create/rm` — template sensor YAML management
- `hactl script def/edit/create/rm` — script YAML management
- `hactl auto def/edit/create/rm` — automation YAML management
- All writes: dry-run default, `--confirm` to apply, auto-reload after apply

---

## Phase 8 Details — Improvements & Usability

### 8.1 `script run <id>` command
Execute a Home Assistant script by ID. Accepts bare name or `script.` prefix.  
`hactl script run morning_routine` → calls `script/turn_on` service.

### 8.2 `auto ls --tag` label filtering
Filter automations by label with `--tag <substring>` (case-insensitive match).  
Labels column added to automation table output.

### 8.3 `ent ls --domain` domain filtering
Filter entities by exact domain: `hactl ent ls --domain sun`.  
Combinable with existing positional pattern filter.

### 8.4 Table column compression (compact mode)
All `ls` commands use compact rendering: 1-space column separator, no trailing padding, skips empty trailing columns. Reduces token count in LLM contexts.

### 8.5 Faulty & realistic script fixtures
- `testdata/fixtures/faulty/scripts.yaml`: 3 scripts (broken_delay, error_service, working_toggle)
- `testdata/fixtures/realistic/scripts.yaml`: 4 scripts (morning_routine, night_mode, guest_welcome, system_restart)

### 8.6 Cache script traces
`cache refresh` now fetches both automation and script traces. Script trace fetch failure is non-fatal (logged as warning).

### 8.7 Token counter `--stats` flag
`hactl --stats <any command>` appends a stats footer: bytes written + estimated token count (~4 chars/token). Stats go to stderr in binary, to writer in test mode.

### 8.8 Integration tests
~30 new integration tests covering all Phase 8 features across basic, faulty, and realistic fixtures. Golden capture tests for new domain filtering.

---

## Phase 9 Details — Registry API, Discovery & Relationships

### 9.1 HA Registry WebSocket Commands
New `internal/haapi/registry.go` with types for all 5 registries (entity, area, label, floor, device). New WS methods: `EntityRegistryList`, `AreaRegistryList`, `LabelRegistryList`, `FloorRegistryList`, `DeviceRegistryList`, `EntityRegistryUpdate`, `LabelRegistryCreate`. Private `sendCommand` helper for generic WS request/response. All methods link to HA source (`homeassistant/components/config/*_registry.py`).

### 9.2 API Source URL Comments
All HA API-touching files (`client.go`, `websocket.go`, `history.go`, `registry.go`) link to official HA GitHub sources for easy upgradability when HA changes its API.

### 9.3 `ent hist --attr <attribute>`
Attribute history from HA `/api/history/period/` with `minimal_response=false`. Parses `historyEntryFull` (includes `attributes` map), extracts named attribute via `toFloat64` (handles float64, json.Number, string). Resampled + rendered like numeric sensor history.

### 9.4 Discovery Commands
- `label ls` / `label create` — list and create HA labels (with --color, --icon, --description)
- `area ls` — list areas with floor and label resolution
- `floor ls` — list floors with level display

### 9.5 Entity Enrichment
`ent ls` and `ent show` now display area + labels columns from registry. New `--area` and `--label` flags for filtering. `registryContext` struct with `fetchRegistryContext()`, `areaName()`, `labelNames()` helpers (in `label.go`).

### 9.6 `auto ls` / `script ls` Enrichment
Both `auto ls` and `script ls` now show area + labels columns. Registry context fetched via WS alongside trace data.

### 9.7 Write Operations
- `ent set-label <entity> <label>...` — validates labels exist, merges with current entity labels, calls `EntityRegistryUpdate`
- `ent set-area <entity> <area>` — resolves area name to area_id, calls `EntityRegistryUpdate`

### 9.8 `ent related <entity_id>` — Relationship Crawler
Discovers 4 relationship types: automation triggers/controls, device siblings, area neighbors, group memberships. Renders grouped table. Extracted helper functions for gocognit compliance.

### 9.9 Testing
- 28 new unit tests: WS registry tests (6), attribute history parsing (4), toFloat64 (1 table-driven), filterEntitiesByArea/Label (2), registryContext methods (2), plus existing patterns
- 19 new integration tests: label ls/create, area ls, floor ls, ent related, WS registry list, WS label create+list, auto/script ls area/labels columns, golden file updates

---

## Next Steps (by Priority)

### 1. Device Registry CLI (`device ls/show`)
Device registry WS command already implemented (`DeviceRegistryList`). Add CLI commands to browse devices and their child entities.

### 2. `ent set-label`/`ent set-area` for batch operations
Current write ops work for single entities. Add `--pattern`/`--domain` support to apply labels/areas to multiple entities at once.

### 3. Container Parallel Boot (Quick Win, ~15s saved)
Start basic + faulty containers in parallel in TestMain instead of lazy-loading.

### 4. CI Integration Tests (Infra)
Add integration-tests job to `.github/workflows/ci.yml` with Docker-service.

### 5. Supervisor API (Future)
`hactl addon ls`, `hactl system info` via HA Supervisor API (HA OS only). Needs `X-Hassio-Token` instead of long-lived token.

### 6. goreleaser (Optional)
Multi-platform binaries, Homebrew tap if external usage planned.

---

## Real-World Investigation Learnings (2026-04-23)

### Case: light.flur_lichtbalken_oben not dimming at night

**Root cause found:** `automation.flur_licht_master` was disabled (state=`off`) since 2026-02-24. The brightness schedule (`licht_flur_helligkeit_im_tagesverlauf_anpassen`) correctly computed and wrote target brightness to `input_number.licht_flur_oben_helligkeit`, but the master automation that translates helper values into actual `light.turn_on` service calls was off — so the physical light stayed at its last-set brightness.

**hactl workflow that found it:**
1. `health` → no errors (1 call)
2. `auto ls --failing` → nothing (1 call)  
3. `auto ls --pattern flur --top 50` → `flur_licht_master state=off` immediately visible (1 call)
4. `ent hist input_number.licht_flur_oben_helligkeit --since 24h` → confirmed schedule was running fine (1 call)

Total: ~4 calls, ~300 tokens to root cause.

**Gaps exposed:**
- No attribute history for lights (brightness not tracked, only on/off)
- Pattern engine doesn't support regex; `light.*flur` returned empty
- `changes --since 24h` timed out (HA logbook API slow)
- No "related entities" crawler — manual chaining required



---

## Reference

- **modernc.org/sqlite** (CGO-free) → static binary, no C compiler
- **Jinja eval via HA /api/template** → semantically correct, no Go port
- **WS system_log/list + REST fallback** → structured + reliable
- **Writes via Config-API only** → HA validates, no filesystem access
- **slog** → stdlib, structured, no extra deps
- **golangci-lint v2** → current version, v1 config incompatible

---

## Scope

**In:** All read-commands, automations write-path, unit+integration+contract tests, golden files, faulty+realistic fixtures, CI-matrix, docs.

**Out:** HA filesystem access, configuration.yaml editing, daemon-mode, goreleaser/Homebrew, regression corpus, heavy-fixture scaling.