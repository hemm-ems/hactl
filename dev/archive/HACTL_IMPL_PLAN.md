# hactl Phase 14 — Implementation Plan

> Repo: `hemm-ems/hactl` (Go)  
> Depends on: hactl-companion v0.2.0 (COMPANION_IMPL_PLAN.md)  
> Scope: Update companion client for v2, add native HA API commands, add YAML CLI commands, auto-discover companion.

---

## Design Principle

- **Companion** = YAML file access only (things HA API cannot do)
- **hactl directly** = reload, check-config, config entries, logs (HA API is sufficient)
- **Auto-discovery** = find companion via HA Ingress API; fallback to explicit `COMPANION_URL`
- **All writes require `--confirm`** = dry-run by default (consistent with existing writer pattern)

---

## Phase 1: Companion Client v2

**Goal**: Strip dead methods, add typed template/script/automation client methods.

| # | Task | File | Details |
|---|------|------|---------|
| 1.1 | Remove 11 dead methods | `internal/companion/client.go` | SupervisorInfo, SupervisorAddons, SupervisorBackups, CreateBackup, CoreLogs, SupervisorLogs, AddonLogs, Reload, Restart, Resolution, CheckConfig |
| 1.2 | Remove dead types | `internal/companion/types.go` | LogsResponse, HaCliResponse |
| 1.3 | Add template types | `internal/companion/types.go` | TemplateDefinition, TemplatesResponse |
| 1.4 | Add script types | `internal/companion/types.go` | ScriptDefinition, ScriptField, ScriptsResponse |
| 1.5 | Add automation types | `internal/companion/types.go` | AutomationDefinition, AutomationsResponse |
| 1.6 | Add ConfigDeleteResponse | `internal/companion/types.go` | `{Status string}` |
| 1.7 | Add template methods (5) | `internal/companion/client.go` | ListTemplates, GetTemplate, WriteTemplate, CreateTemplate, DeleteTemplate |
| 1.8 | Add script methods (5) | `internal/companion/client.go` | ListScriptDefs, GetScriptDef, WriteScriptDef, CreateScriptDef, DeleteScriptDef |
| 1.9 | Add automation methods (5) | `internal/companion/client.go` | ListAutomationDefs, GetAutomationDef, WriteAutomationDef, CreateAutomationDef, DeleteAutomationDef |
| 1.10 | Rewrite unit tests | `internal/companion/client_test.go` | Remove old supervisor/log/cli tests, add httptest mocks for 15 new methods |

**New types**:
```go
type TemplateDefinition struct {
    UniqueID          string `json:"unique_id"`
    Name              string `json:"name"`
    Domain            string `json:"domain"`              // "sensor" or "binary_sensor"
    State             string `json:"state"`               // Jinja2 template
    UnitOfMeasurement string `json:"unit_of_measurement,omitempty"`
    DeviceClass       string `json:"device_class,omitempty"`
}

type TemplatesResponse struct {
    Templates []TemplateDefinition `json:"templates"`
}

type ScriptDefinition struct {
    ID     string        `json:"id"`
    Alias  string        `json:"alias"`
    Mode   string        `json:"mode"`
    Fields []ScriptField `json:"fields,omitempty"`
}

type ScriptField struct {
    Name        string `json:"name"`
    Description string `json:"description,omitempty"`
    Required    bool   `json:"required"`
    Selector    any    `json:"selector,omitempty"`
}

type ScriptsResponse struct {
    Scripts []ScriptDefinition `json:"scripts"`
}

type AutomationDefinition struct {
    ID          string `json:"id"`
    Alias       string `json:"alias"`
    Mode        string `json:"mode,omitempty"`
    Description string `json:"description,omitempty"`
}

type AutomationsResponse struct {
    Automations []AutomationDefinition `json:"automations"`
}

type ConfigDeleteResponse struct {
    Status string `json:"status"`
}
```

**Verification**: `go test ./internal/companion/... -count=1` green

---

## Phase 2: Companion Auto-Discovery

**Goal**: hactl finds companion automatically via HA Ingress, with explicit URL fallback.

| # | Task | File | Details |
|---|------|------|---------|
| 2.1 | Add `CompanionURL` field to Config | `internal/config/config.go` | Optional, parsed from `COMPANION_URL` in .env |
| 2.2 | Add `HassioIngressSessions` WS method | `internal/haapi/websocket.go` | WS cmd: `supervisor/api` → `/ingress/session` |
| 2.3 | Add `HassioAddonInfo` WS method | `internal/haapi/websocket.go` | WS cmd: `hassio/addon/info` with slug → returns ingress_url |
| 2.4 | Create discovery module | `internal/companion/discovery.go` | `Discover(ctx, wsClient, haURL) (string, error)` |
| 2.5 | Discovery logic | discovery.go | 1) Check config CompanionURL; 2) If empty, WS hassio/addon/info("hactl_companion") → ingress URL; 3) Construct full URL |
| 2.6 | Unit test discovery | `internal/companion/discovery_test.go` | Mock WS responses |

**Discovery flow**:
```
1. Config has COMPANION_URL set? → Use it directly
2. WS hassio/addon/info slug="hactl_companion" → get ingress_url field
3. Construct: {ha_base_url}/api/hassio_ingress/{ingress_token}/
4. Health check the URL → return if ok
5. All fail → return error "companion not found"
```

**Verification**: `go test ./internal/companion/... -count=1` green

---

## Phase 3: Native HA API Commands (no companion)

**Goal**: Add `hactl config` command group using existing HA REST/WS APIs.

| # | Task | File | Details |
|---|------|------|---------|
| 3.1 | Add `ConfigEntryList()` | `internal/haapi/websocket.go` | WS command `config/entries` |
| 3.2 | Add ConfigEntry type | `internal/haapi/websocket.go` | `{EntryID, Domain, Title, State, Source string}` |
| 3.3 | Create `cfg.go` | `internal/cmd/cfg.go` | `config` command group |
| 3.4 | `hactl config entries` | cfg.go | Table: Domain \| Title \| State \| Source. Flags: `--domain` filter |
| 3.5 | `hactl config check` | cfg.go | Calls existing `WSClient.CheckConfig()`, prints pass/fail + errors |
| 3.6 | `hactl config reload <domain>` | cfg.go | Calls `CallService(domain, "reload", nil)`. Allowed: automation, script, template, scene, group, core, input_boolean, input_number, input_select |
| 3.7 | Register `configCmd` in root | `internal/cmd/root.go` | |
| 3.8 | Unit tests | `internal/cmd/cfg_test.go` | Follow existing cmd_test.go pattern |
| 3.9 | WS method test | `internal/haapi/websocket_test.go` | ConfigEntryList mock |

**Verification**: `go test ./internal/cmd/... -count=1` + `go test ./internal/haapi/... -count=1` green

---

## Phase 4: Template CLI Commands

**Goal**: `hactl tpl` gains YAML definition management via companion.

| # | Task | File | Details |
|---|------|------|---------|
| 4.1 | `hactl tpl ls` | `internal/cmd/tpl.go` | Call companion `ListTemplates()` → table: Name \| Domain \| UniqueID \| State (truncated to 60 chars) |
| 4.2 | `hactl tpl show <unique_id>` | tpl.go | Call `GetTemplate()` → render full YAML block. Also show current state from HA API (`GetState`) |
| 4.3 | `hactl tpl edit <id> -f <yaml> [--confirm]` | tpl.go | `WriteTemplate(dry_run=true)` → show diff. With `--confirm`: `WriteTemplate(dry_run=false)` + `CallService("template", "reload")` |
| 4.4 | `hactl tpl create -f <yaml>` | tpl.go | `CreateTemplate()` + `CallService("template", "reload")` |
| 4.5 | `hactl tpl rm <id> [--confirm]` | tpl.go | `DeleteTemplate()` + reload. Requires `--confirm` |
| 4.6 | Flags: `--pattern`, `--domain` on ls | tpl.go | Client-side filter |
| 4.7 | Graceful fallback when companion unavailable | tpl.go | `tpl ls` → print "companion not available, showing states only" + fall back to `ent ls --domain template` behavior |
| 4.8 | Unit tests | `internal/cmd/tpl_test.go` | httptest companion mock |

**Verification**: `go test ./internal/cmd/... -count=1` green

---

## Phase 5: Script Definition CLI Commands

**Goal**: `hactl script` gains YAML definition management via companion.

| # | Task | File | Details |
|---|------|------|---------|
| 5.1 | `hactl script def <id>` | `internal/cmd/script.go` | Call `GetScriptDef()` → full YAML including fields/parameters |
| 5.2 | `hactl script edit <id> -f <yaml> [--confirm]` | script.go | Dry-run + apply + `CallService("script", "reload")` |
| 5.3 | `hactl script create -f <yaml>` | script.go | `CreateScriptDef()` + reload |
| 5.4 | `hactl script rm <id> [--confirm]` | script.go | `DeleteScriptDef()` + reload |
| 5.5 | Show fields in `script show` output | script.go | If companion available, augment state view with fields metadata |
| 5.6 | Unit tests | extend existing script tests | |

**Verification**: `go test ./internal/cmd/... -count=1` green

---

## Phase 6: Automation Definition CLI Commands

**Goal**: `hactl auto` gains YAML definition management via companion.

| # | Task | File | Details |
|---|------|------|---------|
| 6.1 | `hactl auto def <id>` | `internal/cmd/auto.go` | Call `GetAutomationDef()` → full YAML (trigger, condition, action) |
| 6.2 | `hactl auto edit <id> -f <yaml> [--confirm]` | auto.go | Dry-run + apply + `CallService("automation", "reload")` |
| 6.3 | `hactl auto create -f <yaml>` | auto.go | `CreateAutomationDef()` + reload |
| 6.4 | `hactl auto rm <id> [--confirm]` | auto.go | `DeleteAutomationDef()` + reload |
| 6.5 | Unit tests | extend existing auto tests | |

**Verification**: `go test ./internal/cmd/... -count=1` green

---

## Phase 7: Integration Tests

**Goal**: Validate full stack (HA + companion v2) with Docker.

| # | Task | File | Details |
|---|------|------|---------|
| 7.1 | Vendor updated `companion-v1.yaml` | `testdata/companion-v1.yaml` | 20 endpoints |
| 7.2 | Seed test config in compose volume | `companiontest/main_test.go` | Write template.yaml, scripts.yaml, automations.yaml to /config after HA boots |
| 7.3 | Template endpoint tests | `companiontest/companion_test.go` | TestListTemplates, TestGetTemplate, TestWriteTemplate_DryRun, TestCreateTemplate, TestDeleteTemplate |
| 7.4 | Script endpoint tests | companion_test.go | TestListScriptDefs, TestGetScriptDef, TestWriteScriptDef_DryRun, TestCreateScriptDef, TestDeleteScriptDef |
| 7.5 | Automation endpoint tests | companion_test.go | TestListAutomationDefs, TestGetAutomationDef, TestWriteAutomationDef_DryRun, TestCreateAutomationDef, TestDeleteAutomationDef |
| 7.6 | Security tests | companion_test.go | TestSecretsYamlDenied, TestPathTraversal (keep from v1) |
| 7.7 | Include resolution test | companion_test.go | TestIncludeResolution — write config with !include, verify resolved read |
| 7.8 | Rewrite contract tests | `companiontest/contract_test.go` | 20 paths, correct methods |
| 7.9 | Config entries integration test | `internal/integration/cfg_test.go` | TestConfigEntryList, TestConfigCheck, TestConfigReload (testcontainers, no companion) |

**Verification**: 
- `go test -tags=companion ./internal/companiontest/... -v -timeout 300s` green
- `go test -tags=integration ./internal/integration/... -v -timeout 600s` green

---

## Phase 8: Documentation

| # | Task | File |
|---|------|------|
| 8.1 | Rewrite HACTL_INTEGRATION.md | Contract reflects v2 (20 endpoints, no supervisor/logs/ha-cli) |
| 8.2 | Update IMPLEMENTATION.md | Add Phase 14 entry + update metrics |
| 8.3 | Update README.md | Add companion section, new commands reference |
| 8.4 | Update Makefile | Ensure `test-companion` target still works with v2 |

---

## Execution Order & Dependencies

```
                    ┌─────────────────────────────────────┐
                    │ Companion v2 (separate repo)        │
                    │ Must complete Phases 1-6 first      │
                    └───────────────┬─────────────────────┘
                                    │
    ┌───────────────────────────────┼───────────────────────────┐
    │                               │                           │
    ▼                               ▼                           ▼
Phase 1 (client v2)          Phase 2 (discovery)         Phase 3 (native cmds)
    │                               │                           │
    ├───────────────────────────────┤                           │
    ▼                               ▼                           ▼
Phase 4 (tpl cmds)    Phase 5 (script cmds)    Phase 6 (auto cmds)
    │                         │                         │
    └─────────────────────────┼─────────────────────────┘
                              ▼
                       Phase 7 (integration tests)
                              │
                              ▼
                       Phase 8 (documentation)
```

**Parallel tracks**:
- Phase 3 (native commands) is fully independent — no companion dependency
- Phases 4, 5, 6 (YAML CLI commands) are independent of each other but depend on Phase 1
- Phase 2 (discovery) is independent of Phases 4-6

---

## New Commands Summary

| Command | Source | Companion? |
|---------|--------|-----------|
| `hactl config entries` | WS `config/entries` | No |
| `hactl config check` | WS `homeassistant/check_config` | No |
| `hactl config reload <domain>` | REST `POST /api/services/{domain}/reload` | No |
| `hactl tpl ls` | companion `/v1/config/templates` | Yes |
| `hactl tpl show <id>` | companion `/v1/config/template` + HA state | Yes |
| `hactl tpl edit <id> -f ...` | companion PUT `/v1/config/template` + HA reload | Yes |
| `hactl tpl create -f ...` | companion POST `/v1/config/template` + HA reload | Yes |
| `hactl tpl rm <id>` | companion DELETE `/v1/config/template` + HA reload | Yes |
| `hactl script def <id>` | companion `/v1/config/script` | Yes |
| `hactl script edit <id> -f ...` | companion PUT `/v1/config/script` + HA reload | Yes |
| `hactl script create -f ...` | companion POST `/v1/config/script` + HA reload | Yes |
| `hactl script rm <id>` | companion DELETE `/v1/config/script` + HA reload | Yes |
| `hactl auto def <id>` | companion `/v1/config/automation` | Yes |
| `hactl auto edit <id> -f ...` | companion PUT `/v1/config/automation` + HA reload | Yes |
| `hactl auto create -f ...` | companion POST `/v1/config/automation` + HA reload | Yes |
| `hactl auto rm <id>` | companion DELETE `/v1/config/automation` + HA reload | Yes |

---

## Done Criteria

- [ ] Companion client v2: 15 new methods, 0 dead methods, all unit-tested
- [ ] Auto-discovery works via WS hassio/addon/info
- [ ] `hactl config entries/check/reload` work without companion
- [ ] `hactl tpl ls/show/edit/create/rm` work with companion
- [ ] `hactl script def/edit/create/rm` work with companion
- [ ] `hactl auto def/edit/create/rm` work with companion
- [ ] All writes require `--confirm` (dry-run default)
- [ ] All writes trigger domain reload after apply
- [ ] Graceful fallback when companion unavailable
- [ ] Integration tests pass (companion Docker stack)
- [ ] Contract tests validate 20 OpenAPI paths
- [ ] `golangci-lint run ./...` = 0 findings
- [ ] Documentation updated
