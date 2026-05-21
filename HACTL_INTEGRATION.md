# hactl ↔ companion: API Contract v2

> Defines the interface between hactl (Go CLI) and hactl-companion (Python add-on).  
> Companion scope: **YAML file access only** — things the HA REST/WS API cannot do.

---

## Design Principle

| Capability | HA API | Companion needed? |
|---|---|---|
| Read YAML definitions (template.yaml, scripts.yaml, automations.yaml) | ✗ No API | **Yes** |
| Write/edit YAML blocks in-place with backup | ✗ No API | **Yes** |
| Resolve `!include` / `!include_dir_named` | ✗ No API | **Yes** |
| Script parameter discovery (`fields:`) | ✗ Only in YAML | **Yes** |
| Template sensor Jinja2 source | ✗ Only in YAML | **Yes** |
| Reload domains | ✓ `POST /api/services/{domain}/reload` | No |
| Check config | ✓ WS `homeassistant/check_config` | No |
| System logs | ✓ WS `system_log/list` + REST `/api/error_log` | No |
| Config entries | ✓ WS `config/entries` | No |
| Supervisor info | ✓ WS `hassio/addon/info` | No |

---

## Overview

1. hactl auto-discovers companion via the Supervisor WS proxy (`supervisor/api` → `/addons` enumeration → `/addons/<slug>/info`), or explicit `COMPANION_URL`
2. Companion mounts `/config` volume and serves YAML content via HTTP
3. hactl calls companion for YAML read/write, then uses HA API directly for reload
4. All writes are dry-run by default; `--confirm` flag to apply
5. Integration tests use Docker (HA + companion on shared volume)

---

## 1. API Endpoints (v2 Contract)

### 1.1 Health

| Method | Path | Auth | Response |
|--------|------|------|----------|
| GET | `/v1/health` | None | `{"status": "ok", "version": "0.2.0"}` |

### 1.2 Generic Config File Operations

| Method | Path | Auth | Response |
|--------|------|------|----------|
| GET | `/v1/config/files` | Bearer | `{"files": ["configuration.yaml", "template.yaml", ...]}` |
| GET | `/v1/config/file?path=<path>&resolve=true` | Bearer | `{"path": "...", "content": "..."}` |
| GET | `/v1/config/block?path=<path>&id=<id>` | Bearer | `{"path": "...", "id": "...", "content": "..."}` |
| PUT | `/v1/config/file?path=<path>&dry_run=true` | Bearer | `{"status": "dry_run\|applied", "diff": "...", "backup": "..."}` |

**Query params**:
- `resolve` (default `true`): Resolve `!include`, `!include_dir_named`, `!include_dir_list`, `!include_dir_merge_named`
- `dry_run` (default `true`): If true, return diff without writing. If false, write + create backup.

**Security**:
- `secrets.yaml` always denied → 403
- Path traversal (`..`, absolute paths, symlinks outside /config) → 400
- All paths relative to `/config`

### 1.3 Template Sensor Endpoints

| Method | Path | Auth | Response |
|--------|------|------|----------|
| GET | `/v1/config/templates` | Bearer | `{"templates": [...]}` |
| GET | `/v1/config/template?id=<unique_id>` | Bearer | `{"unique_id": "...", "content": "<yaml>"}` |
| PUT | `/v1/config/template?id=<unique_id>&dry_run=true` | Bearer | `{"status": "dry_run\|applied", "diff": "...", "backup": "..."}` |
| POST | `/v1/config/template` | Bearer | `{"status": "created", "unique_id": "..."}` |
| DELETE | `/v1/config/template?id=<unique_id>` | Bearer | `{"status": "deleted"}` |

**GET /v1/config/templates response**:
```json
{
  "templates": [
    {
      "unique_id": "uuidTemplatesddfgfdffewesdsfsckl",
      "name": "Energie Zählerstand Flur",
      "domain": "sensor",
      "state": "{{(states('sensor.energieverbrauch_total')|float(0) + ...) | round(1)}}",
      "unit_of_measurement": "kWh",
      "device_class": ""
    },
    {
      "unique_id": "uuidTemplatesdzaktiwozimotiondoubel",
      "name": "Wohnzimmer Bewegung doublechek",
      "domain": "binary_sensor",
      "state": "{% set motion = ... %}",
      "unit_of_measurement": "",
      "device_class": ""
    }
  ]
}
```

**Parsing**: `template.yaml` is a top-level list. Each item has a `sensor:` or `binary_sensor:` key containing a list of definitions. Match by `unique_id`.

### 1.4 Script Definition Endpoints

| Method | Path | Auth | Response |
|--------|------|------|----------|
| GET | `/v1/config/scripts` | Bearer | `{"scripts": [...]}` |
| GET | `/v1/config/script?id=<script_id>` | Bearer | `{"id": "...", "content": "<yaml>"}` |
| PUT | `/v1/config/script?id=<script_id>&dry_run=true` | Bearer | `{"status": "dry_run\|applied", "diff": "...", "backup": "..."}` |
| POST | `/v1/config/script` | Bearer | `{"status": "created", "id": "..."}` |
| DELETE | `/v1/config/script?id=<script_id>` | Bearer | `{"status": "deleted"}` |

**GET /v1/config/scripts response**:
```json
{
  "scripts": [
    {
      "id": "kino_start",
      "alias": "Kino Start",
      "mode": "single",
      "fields": [
        {
          "name": "brightness",
          "description": "Target brightness",
          "required": false,
          "selector": {"number": {"min": 0, "max": 255}}
        }
      ]
    }
  ]
}
```

**Parsing**: `scripts.yaml` is a top-level dict. Keys = script IDs. Each value has `alias`, `mode`, optional `fields`, `sequence`.

### 1.5 Automation Definition Endpoints

| Method | Path | Auth | Response |
|--------|------|------|----------|
| GET | `/v1/config/automations` | Bearer | `{"automations": [...]}` |
| GET | `/v1/config/automation?id=<id>` | Bearer | `{"id": "...", "content": "<yaml>"}` |
| PUT | `/v1/config/automation?id=<id>&dry_run=true` | Bearer | `{"status": "dry_run\|applied", "diff": "...", "backup": "..."}` |
| POST | `/v1/config/automation` | Bearer | `{"status": "created", "id": "..."}` |
| DELETE | `/v1/config/automation?id=<id>` | Bearer | `{"status": "deleted"}` |

**GET /v1/config/automations response**:
```json
{
  "automations": [
    {
      "id": "1234567890",
      "alias": "Turn on lights at sunset",
      "mode": "single",
      "description": ""
    }
  ]
}
```

**Parsing**: `automations.yaml` is a top-level list. Each item is a dict with `id` field.

---

## 2. Authentication

- Token passed via `Authorization: Bearer <token>` header
- In HA OS/Supervised: Companion uses `SUPERVISOR_TOKEN` env (injected by Supervisor)
- `/v1/health` is exempt from auth (liveness check)
- In integration tests: static token `integration-test-token-12345`

---

## 3. Error Responses

| Status | Meaning | Body |
|--------|---------|------|
| 400 | Bad request (path traversal, invalid params) | `{"error": "path traversal detected"}` |
| 401 | Missing/invalid auth token | `{"error": "unauthorized"}` |
| 403 | Forbidden (secrets.yaml) | `{"error": "access denied: secrets.yaml"}` |
| 404 | Not found (file, block, template, script, automation) | `{"error": "not found: <id>"}` |
| 500 | Internal server error | `{"error": "..."}` |

---

## 4. !include Resolution

Companion resolves HA YAML include directives when `resolve=true` (default):

| Directive | Behavior |
|-----------|----------|
| `!include <path>` | Inline file content at reference point |
| `!include_dir_named <dir>` | Merge directory as named dict (filename without ext = key) |
| `!include_dir_list <dir>` | Merge directory contents as list |
| `!include_dir_merge_named <dir>` | Deep merge named dict |

**Security**: Resolved paths must remain within `/config`. Circular includes detected and rejected.

---

## 5. Go Client (`internal/companion/`)

### 5.1 Types (`types.go`)

```go
type HealthResponse struct {
    Status  string `json:"status"`
    Version string `json:"version"`
}

type ConfigFilesResponse struct {
    Files []string `json:"files"`
}

type ConfigFileResponse struct {
    Path    string `json:"path"`
    Content string `json:"content"`
}

type ConfigBlockResponse struct {
    Path    string `json:"path"`
    ID      string `json:"id"`
    Content string `json:"content"`
}

type ConfigWriteResponse struct {
    Status string `json:"status"`
    Diff   string `json:"diff,omitempty"`
    Backup string `json:"backup,omitempty"`
}

type ConfigDeleteResponse struct {
    Status string `json:"status"`
}

type TemplateDefinition struct {
    UniqueID          string `json:"unique_id"`
    Name              string `json:"name"`
    Domain            string `json:"domain"`
    State             string `json:"state"`
    UnitOfMeasurement string `json:"unit_of_measurement,omitempty"`
    DeviceClass       string `json:"device_class,omitempty"`
}

type TemplatesResponse struct {
    Templates []TemplateDefinition `json:"templates"`
}

type TemplateResponse struct {
    UniqueID string `json:"unique_id"`
    Content  string `json:"content"`
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

type ScriptResponse struct {
    ID      string `json:"id"`
    Content string `json:"content"`
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

type AutomationResponse struct {
    ID      string `json:"id"`
    Content string `json:"content"`
}
```

### 5.2 Client Methods (`client.go`)

```go
// Health
func (c *Client) Health(ctx) (*HealthResponse, error)

// Generic config
func (c *Client) ListConfigFiles(ctx) (*ConfigFilesResponse, error)
func (c *Client) ReadConfigFile(ctx, path string, resolve bool) (*ConfigFileResponse, error)
func (c *Client) ReadConfigBlock(ctx, path, id string) (*ConfigBlockResponse, error)
func (c *Client) WriteConfigFile(ctx, path, content string, dryRun bool) (*ConfigWriteResponse, error)

// Templates
func (c *Client) ListTemplates(ctx) (*TemplatesResponse, error)
func (c *Client) GetTemplate(ctx, uniqueID string) (*TemplateResponse, error)
func (c *Client) WriteTemplate(ctx, uniqueID, content string, dryRun bool) (*ConfigWriteResponse, error)
func (c *Client) CreateTemplate(ctx, content string) (*ConfigWriteResponse, error)
func (c *Client) DeleteTemplate(ctx, uniqueID string) (*ConfigDeleteResponse, error)

// Scripts
func (c *Client) ListScriptDefs(ctx) (*ScriptsResponse, error)
func (c *Client) GetScriptDef(ctx, id string) (*ScriptResponse, error)
func (c *Client) WriteScriptDef(ctx, id, content string, dryRun bool) (*ConfigWriteResponse, error)
func (c *Client) CreateScriptDef(ctx, content string) (*ConfigWriteResponse, error)
func (c *Client) DeleteScriptDef(ctx, id string) (*ConfigDeleteResponse, error)

// Automations
func (c *Client) ListAutomationDefs(ctx) (*AutomationsResponse, error)
func (c *Client) GetAutomationDef(ctx, id string) (*AutomationResponse, error)
func (c *Client) WriteAutomationDef(ctx, id, content string, dryRun bool) (*ConfigWriteResponse, error)
func (c *Client) CreateAutomationDef(ctx, content string) (*ConfigWriteResponse, error)
func (c *Client) DeleteAutomationDef(ctx, id string) (*ConfigDeleteResponse, error)
```

### 5.3 Discovery (`discovery.go`)

```go
// Discover finds the companion URL.
// Priority: 1) Config.CompanionURL,
//           2) WS supervisor/api (Supervisor proxy) → /addons enumeration → /addons/<slug>/info → ingress URL
func Discover(ctx context.Context, cfg *config.Config, ws *haapi.WSClient) (string, error)
```

---

## 6. hactl CLI Commands Using Companion

| Command | Companion Call | After Apply |
|---------|---------------|-------------|
| `hactl tpl ls` | `ListTemplates()` | — |
| `hactl tpl show <id>` | `GetTemplate()` + HA `GetState()` | — |
| `hactl tpl edit <id> -f <yaml> [--confirm]` | `WriteTemplate(dry_run)` | `CallService("template", "reload")` |
| `hactl tpl create -f <yaml>` | `CreateTemplate()` | `CallService("template", "reload")` |
| `hactl tpl rm <id> --confirm` | `DeleteTemplate()` | `CallService("template", "reload")` |
| `hactl script def <id>` | `GetScriptDef()` | — |
| `hactl script edit <id> -f <yaml> [--confirm]` | `WriteScriptDef(dry_run)` | `CallService("script", "reload")` |
| `hactl script create -f <yaml>` | `CreateScriptDef()` | `CallService("script", "reload")` |
| `hactl script rm <id> --confirm` | `DeleteScriptDef()` | `CallService("script", "reload")` |
| `hactl auto def <id>` | `GetAutomationDef()` | — |
| `hactl auto edit <id> -f <yaml> [--confirm]` | `WriteAutomationDef(dry_run)` | `CallService("automation", "reload")` |
| `hactl auto create -f <yaml>` | `CreateAutomationDef()` | `CallService("automation", "reload")` |
| `hactl auto rm <id> --confirm` | `DeleteAutomationDef()` | `CallService("automation", "reload")` |

---

## 7. hactl Native Commands (No Companion)

| Command | HA API | Details |
|---------|--------|---------|
| `hactl config entries` | WS `config/entries` | Table: Domain \| Title \| State \| Source |
| `hactl config check` | WS `homeassistant/check_config` | Already implemented in websocket.go |
| `hactl config reload <domain>` | REST `POST /api/services/{domain}/reload` | Already proven in writer.go |

---

## 8. Docker Integration Tests

### 8.1 Stack (`internal/companiontest/docker-compose.yaml`)

```yaml
services:
  homeassistant:
    image: ghcr.io/home-assistant/home-assistant:stable
    ports: ["8123"]
    volumes: [ha-config:/config]
    networks: [ha-net]

  companion:
    image: ghcr.io/hemm-ems/hactl-companion:0.2
    environment:
      SUPERVISOR_TOKEN: integration-test-token-12345
    ports: ["9100"]
    volumes: [ha-config:/config]
    networks: [ha-net]

volumes:
  ha-config:
networks:
  ha-net:
    driver: bridge
```

### 8.2 Test Lifecycle (`main_test.go`)

1. Pull companion image from GHCR (`ghcr.io/hemm-ems/hactl-companion:0.2`)
   - Fallback: clone companion repo → `docker build` locally
2. `docker compose up -d`
3. Poll HA `/api/onboarding` until ready
4. Headless onboarding (5 steps → long-lived token)
5. Poll companion `/v1/health` until ready
6. Wait for config files to be written
7. Run tests
8. `docker compose down -v`

### 8.3 Test Cases (Implemented)

**Config file tests**:
- TestHealth, TestListConfigFiles, TestReadConfigFile
- TestSecretsYamlDenied, TestPathTraversal
- TestDryRun, TestWriteNewFile

**Template tests**:
- TestListTemplates — verify seeded template sensor returned
- TestCreateAndGetTemplate — create new sensor, get by unique_id, write dry-run, delete

**Script tests**:
- TestListScriptDefs — verify seeded script returned
- TestCreateAndGetScript — create with id key, get, write dry-run, delete

**Automation tests**:
- TestListAutomationDefs — verify seeded automation returned
- TestCreateAndGetAutomation — create with id field, get, write dry-run, delete

**Contract tests**:
- TestOpenAPISpecValid — spec parses
- TestAllSpecPathsCovered — 10 paths present
- TestSpecEndpointMethods — correct HTTP methods (20 operations)

---

## 9. OpenAPI Spec (`testdata/companion-v1.yaml`)

**Contract owned by companion repo** (`openapi/companion-v1.yaml`).  
Vendored in hactl at `testdata/companion-v1.yaml`. Updated when companion releases a new version.  
Image: `ghcr.io/hemm-ems/hactl-companion` (tags: `latest`, `0.2`, `0.2.0`)

10 paths, 20 operations:
```
/v1/health                    GET
/v1/config/files              GET
/v1/config/file               GET, PUT
/v1/config/block              GET
/v1/config/templates          GET
/v1/config/template           GET, PUT, POST, DELETE
/v1/config/scripts            GET
/v1/config/script             GET, PUT, POST, DELETE
/v1/config/automations        GET
/v1/config/automation         GET, PUT, POST, DELETE
```

---

## 10. Companion Discovery

```
┌─────────────────────────────────────────────────┐
│  hactl config load                               │
│                                                  │
│  1. COMPANION_URL in .env? → use directly        │
│  2. WS hassio/addon/info(hactl_companion)        │
│     → ingress_url field                          │
│     → construct: {ha_url}/api/hassio_ingress/... │
│  3. Health check companion URL                   │
│  4. Return companion client (or nil if not found)│
└─────────────────────────────────────────────────┘
```

Commands requiring companion gracefully degrade:
- `hactl tpl ls` without companion → "companion not available; showing entity states only"
- `hactl script def` without companion → "companion required for YAML definitions"

---

## 11. Checklist

### v1 (Phase 13 — Done ✓)
- [x] Create `internal/companion/client.go` + `types.go`
- [x] Create `internal/companion/client_test.go` (unit tests with httptest)
- [x] Vendor `openapi/companion-v1.yaml` → `testdata/companion-v1.yaml`
- [x] Create `internal/companiontest/docker-compose.yaml`
- [x] Create `internal/companiontest/main_test.go`
- [x] Create `internal/companiontest/companion_test.go`
- [x] Create `internal/companiontest/contract_test.go`
- [x] Add `make test-companion` to Makefile
- [x] Run test-companion green locally (16 tests pass, ~60s)

### v2 (Phase 14 — In Progress)
- [x] Companion: strip supervisor/logs/ha-cli endpoints (12 removed)
- [x] Companion: add !include resolution
- [x] Companion: add template CRUD endpoints (5)
- [x] Companion: add script CRUD endpoints (5)
- [x] Companion: add automation CRUD endpoints (5)
- [x] Companion: update OpenAPI spec to 10 paths / 20 operations
- [x] Companion: release v0.2.0 on GHCR (`ghcr.io/hemm-ems/hactl-companion:0.2`)
- [x] hactl: update companion client (remove 11 methods, add 15)
- [x] hactl: update contract tests (10 paths, 20 operations)
- [x] hactl: update integration tests (GHCR image, template/script/automation CRUD)
- [x] hactl: vendor new contract from companion repo
- [ ] hactl: add companion auto-discovery
- [ ] hactl: add `config entries/check/reload` commands (no companion)
- [ ] hactl: add `tpl ls/show/edit/create/rm` commands
- [ ] hactl: add `script def/edit/create/rm` commands
- [ ] hactl: add `auto def/edit/create/rm` commands
- [ ] hactl: update documentation
