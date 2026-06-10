# hactl Manual — LLM Usage Guide

> For agents using hactl as a tool. Assumes familiarity with Home Assistant concepts.

## Mental model

hactl is a read-heavy CLI. Most commands query HA via REST/WebSocket, condense the result, and print compact text. One directory = one HA instance. All state lives in `.env` (credentials) + `cache/` (SQLite + JSONL).

**Token budget:** Every response starts with a `[~N tok]` header so agents know the cost before reading further. Default cap is 500 tokens (`--tokensmax=500`). Raise the cap (`--tokensmax=2000`) or remove it entirely (`--tokensmax=0`) when you need full output. Use `--full` or `--json` only when you need raw data. Add `--stats` to any command to see response size + estimated token count on stderr.

## Setup

```
HA_URL=http://homeassistant.local:8123
HA_TOKEN=<long_lived_access_token>
```

> **Windows users:** Use `HA_URL=http://127.0.0.1:8123` instead of `localhost`.
> Windows may resolve `localhost` to `::1` (IPv6), but HA typically listens on `0.0.0.0` (IPv4 only), causing connection failures.

Point hactl at the directory containing `.env`:
```bash
export HACTL_DIR=/path/to/instance   # or
hactl --dir /path/to/instance <cmd>  # or cd into it
```
Without `--dir`/`HACTL_DIR`, hactl uses the `.env` in the current directory, then walks parent directories (git-style; parent `.env` files without `HA_URL` are skipped), then falls back to `~/.hactl/default/`.

For debugging, set `HACTL_LOG_LEVEL=debug` to surface discovery, WS, and HTTP details on stderr (accepts `debug`, `info`, `warn`, `error`; defaults to `info`).

Companion connectivity issues? Run `hactl companion status` for a one-screen diagnostic showing which discovery path succeeded or failed and why. Typical failure reasons:

- `auth_denied` — your long-lived token lacks admin scope. Re-issue from an HA owner account.
- `addon_missing` — the add-on isn't installed. HA → Settings → Add-ons → install `hactl-companion`.
- `protocol_mismatch` — HA Container without Supervisor. Set `COMPANION_URL` in `.env` directly.
- `unreachable` — Supervisor is there but the add-on URL isn't responding. Check Ingress / network.

Discovery requires HA OS or Supervised (`supervisor/api` WS proxy must be available). External access works automatically via Supervisor-issued `ingress_session` cookies — no manual port-forwarding or signed-URL setup needed.

---

## Command Reference

### Setup & health

```bash
hactl setup                   # interactive first-time setup: prompts for HA_URL + HA_TOKEN, writes .env in the current dir (or --dir)
hactl setup --url http://ha:8123 --token <token>   # non-interactive (agents/scripts); --token - reads from stdin; --force overwrites
hactl health                  # HA version, state, recorder, location, timezone, error count
hactl health --json            # same as structured JSON
hactl issues                  # active HA repairs/issues (domain, severity, fixable)
hactl changes --since 24h     # logbook: what changed recently (state changes, auto triggers)
```

### Automations

```bash
hactl auto ls                             # table: id, state, area, labels, runs_24h, errors, last_err
hactl auto ls --failing                   # only automations with recent errors
hactl auto ls --pattern 'ess_*'           # glob/substring filter on automation ID
hactl auto ls --label victron             # filter by label name (substring)
hactl auto show climate_schedule          # config summary + last 5 traces with stable IDs
hactl trace show trc:a7                   # condensed trace (trigger → condition → action, pass/fail)
hactl trace show trc:a7 --full            # raw trace JSON
```

Condensed trace format:
```
trc:a7  automation.climate_schedule  2026-04-16 09:42  FAIL
 1 trigger  time         09:42:00
 2 cond     state==home  true
 3 cond     tmpl         FAIL  → 'unknown' not float
 X action   service_call skipped
```
`X` = skipped. Stable trace IDs persist in `cache/ids.json` for follow-up calls.

### Automations — create & delete

```bash
hactl auto create -f new_auto.yaml              # dry-run (default, no write)
hactl auto create -f new_auto.yaml --confirm    # create via companion + reload
hactl auto delete climate_schedule              # dry-run
hactl auto delete climate_schedule --confirm    # delete via companion + reload
```

Requires hactl-companion. YAML file format matches HA automation config (id, alias, trigger, condition, action).

### Scripts

```bash
hactl script ls                    # table: id, state, area, labels, runs_24h, errors, last_err
hactl script ls --pattern kino     # glob/substring filter
hactl script ls --label energy     # filter by label name (substring)
hactl script ls --failing          # only scripts with recent errors
hactl script show kino_start       # config summary + last 5 traces
hactl script run kino_start        # execute script via script.turn_on
```

`state` column: `off` = idle, `on` = currently running.

### Scripts — create & delete

```bash
hactl script create -f new_script.yaml             # dry-run
hactl script create -f new_script.yaml --confirm   # create via companion + reload
hactl script delete kino_start                     # dry-run
hactl script delete kino_start --confirm           # delete via companion + reload
```

Requires hactl-companion. YAML file format matches HA scripts.yaml (top-level key = script ID).

### Entities & history

```bash
hactl ent ls                              # all entities
hactl ent ls --pattern 'sensor.wp_*'      # glob/substring on entity_id
hactl ent ls --domain sensor              # filter by domain
hactl ent ls --area living                # filter by area name (substring)
hactl ent ls --label energy               # filter by label name (substring)
hactl ent show sensor.wp_vl               # state + key attributes + area + labels (+ hidden count)
hactl ent show sensor.wp_vl --full        # + all attributes
hactl ent hist sensor.wp_vl --since 7d    # ~50 resampled datapoints (time/value)
hactl ent hist sensor.wp_vl --resample 5m # override bucket size
hactl ent hist sensor.wp_vl --attr brightness  # track attribute instead of state
hactl ent anomalies sensor.wp_vl          # gaps (>1h), stuck (>2h/24h), spikes (z>3)
hactl ent related sensor.wp_vl            # related automations, device siblings, area neighbors
hactl ent who light.kitchen --since 7d    # who/what changed it: per-event + counts summary
```

`ent hist` auto-resamples to ~50 points. For binary/non-numeric entities the timeline shows time/state/duration. Anomaly detection runs client-side on cached history.

`ent show` includes a `changed_by:` line attributing the most recent change to a user (e.g. `User Jan`) or to `Home Assistant` when no user_id was on the state's `context`. `ent who` does the deeper attribution — it queries the logbook for the entity, classifies each event as `User <name>`, `Automation: <alias>`, `Script: <id>`, `Device: <name>`, or `Home Assistant`, and aggregates a counts summary (`Jan: 12, Automation 'Sunset lights': 5, ...`). `--json` returns `{events, summary, window}`. Resolving user UUIDs to names requires an admin long-lived access token; with a non-admin token the user list call is admin-denied and the output falls back to raw UUIDs while automation/script/device attribution continues to work.

The `changes` command also gained a `who` column carrying the same per-event label.

### Registry: labels, areas, floors

```bash
hactl label ls                            # label_id, name, color, description
hactl label create "Energy" --color red --icon mdi:flash --description "Power consumers"  # dry-run
hactl label create "Energy" --color red --icon mdi:flash --confirm                          # actually create

hactl area ls                             # area_id, name, floor (name), labels
hactl area create "Kitchen" --icon mdi:silverware-fork           # dry-run
hactl area create "Kitchen" --icon mdi:silverware-fork --confirm  # actually create
hactl area delete kitchen --confirm       # delete (dry-run without --confirm)

hactl floor ls                            # floor_id, name, level, icon
hactl floor create "Ground Floor" --icon mdi:home-floor-0 --level 0           # dry-run
hactl floor create "Ground Floor" --icon mdi:home-floor-0 --level 0 --confirm # actually create
hactl floor delete ground_floor --confirm # delete (dry-run without --confirm)

hactl label delete old-label --confirm    # delete a label (dry-run without --confirm)

hactl ent set-label sensor.wp_vl energy   # assign label(s) to entity (by ID or name)
hactl ent set-area  sensor.wp_vl living_room  # set entity area (area_id)
```

Labels and areas are applied via the entity registry. Multiple labels can be passed to `set-label` at once.

### Write path (automations)

```bash
hactl auto diff climate_schedule -f new.yaml          # diff local vs remote
hactl auto apply climate_schedule -f new.yaml          # dry-run (default, no write)
hactl auto apply climate_schedule -f new.yaml --confirm  # write + reload
hactl auto rollback                                    # undo last backup
hactl auto rollback climate_schedule                   # undo specific automation

```

**Safety:** `apply` without `--confirm` is always a dry-run and writes nothing (no backup files either). The candidate's trigger/condition/action blocks are validated against HA's real config schema (WS `validate_config`) in both dry-run and confirm mode — an invalid config aborts before anything is written. On `--confirm`, a backup of the current config is saved to `backups/` before the write, and HA's Config API validates again on write.

### Templates — create & delete

```bash
hactl tpl create -f sensor_tpl.yaml                  # dry-run
hactl tpl create -f sensor_tpl.yaml --confirm        # create via companion + reload
hactl tpl create -f binary_tpl.yaml --domain binary_sensor --confirm  # non-default domain
hactl tpl delete my_template_uid                     # dry-run
hactl tpl delete my_template_uid --confirm           # delete via companion + reload
```

Requires hactl-companion. Default domain is `sensor`. Supported: sensor, binary_sensor.

### Helpers

```bash
hactl helper ls                                      # list all helpers
hactl helper ls --domain input_boolean               # filter by domain
hactl helper show guest_mode                         # show helper YAML definition
hactl helper create input_boolean -f toggle.yaml             # dry-run
hactl helper create input_boolean -f toggle.yaml --confirm   # create via companion + reload
hactl helper delete guest_mode                       # dry-run
hactl helper delete guest_mode --confirm             # delete via companion + reload
```

Supported domains: input_boolean, input_number, input_select, input_text, input_datetime, counter, timer, schedule. Requires hactl-companion.

### Templates & services

```bash
hactl tpl eval '{{ states("sensor.temperature") | float * 2 }}'
hactl tpl eval -f my_template.j2          # read from file

hactl svc call weather.get_forecasts -d '{"entity_id":"weather.home","type":"daily"}' --return   # prints service response (use --return for services that support return_response, e.g. weather.get_forecasts, calendar.get_events)
hactl svc call light.turn_on -d '{"entity_id":"light.kitchen","brightness":200}'
hactl svc call light.turn_on -d @payload.json   # read JSON from file (avoids quoting)
```

Templates evaluated server-side by HA's Jinja engine — semantically correct, including `states()` and custom filters.

### Config entries & flows

```bash
hactl config entries                              # list all config entries (entry_id, domain, title, state, version)
hactl config entries --domain zha                 # filter by integration domain
hactl config delete <entry_id>                    # delete a config entry (dry-run; add --confirm to apply)
hactl config options <entry_id>                   # start options flow for an existing config entry
hactl config flow-start <domain>                  # start a new config flow for a domain/integration
hactl config flow-step <flow_id> --data '{...}'   # submit data to advance a flow step
hactl config flow-step <flow_id> --data '{...}' --options  # same, but for an options flow
hactl config flow-inspect <flow_id>               # inspect current flow state (step, schema, errors)
hactl config flow-inspect <flow_id> --options     # same, but for an options flow
```

Config flows are multi-step and stateful. An LLM agent driving integration setup uses this pattern:

```bash
# 1. Start a flow
hactl config options abc123-entry-id --json
# → {"flow_id":"xyz","type":"form","step_id":"init","data_schema":[...]}

# 2. Submit data to advance
hactl config flow-step xyz --data '{"action": "add_device"}' --options --json
# → {"flow_id":"xyz","type":"form","step_id":"select_device","data_schema":[...]}

# 3. Complete the flow
hactl config flow-step xyz --data '{"device_type": "heat_pump"}' --options --json
# → {"flow_id":"xyz","type":"create_entry","title":"Heat Pump"}
```

Some steps contain **expandable sections** (schema fields of type `expandable`, e.g. the Generic Camera `advanced` section). Their fields must be nested under the section name in `--data`, not passed flat — otherwise HA returns a 400. `flow-inspect` shows the nested fields (as `advanced.framerate`) and prints the exact nesting to use:

```bash
hactl config flow-step xyz --data '{"stream_source": "rtsp://...", "advanced": {"framerate": 2, "verify_ssl": false}}'
```

When a step fails, the HA error detail (e.g. the offending field) is included in the error message.

When starting a *new* integration (not reconfiguring an existing entry), use `flow-start` + `flow-step` without `--options`.

All `config` commands use HA's REST API directly — no companion needed. Add `--json` for structured output suitable for LLM consumption.

### Dashboards (Lovelace)

```bash
hactl dash ls                                      # list all dashboards (url_path, title, mode)
hactl dash ls --json                               # structured JSON for all dashboards
hactl dash show                                    # views summary for default dashboard
hactl dash show my-dashboard                       # views summary by url_path (from `dash ls`, NOT a view path)
hactl dash show my-dashboard --json                # pretty-printed full config JSON
hactl dash show my-dashboard --raw                 # raw HA JSON (for round-trip editing)
hactl dash show my-dashboard --view living-room    # single view detail as JSON

hactl dash create --url-path my-dash --title "My Dashboard" --icon mdi:home --confirm
hactl dash save my-dash --file config.json --confirm  # write full config (dry-run without --confirm)
hactl dash delete my-dash --confirm

hactl dash resources                               # list custom card/CSS resources
```

**LLM round-trip workflow:** `dash show --raw` → modify JSON → `dash save --file`. Config replacement is always full — HA has no partial update API.

> **Skill:** For LLM agents designing dashboards, load the `lovelace-design` skill (`.github/skills/lovelace-design/SKILL.md`). It covers card types, grid sizing, layout patterns, and common pitfalls.

### Logs & custom components

```bash
hactl log --errors                        # error-level entries only
hactl log --errors --unique               # deduplicated, sorted by count
hactl log --component zha                 # filter by component name (substring)
hactl log show log:f2                     # full detail: timestamp, component, message

hactl cc ls                               # installed custom components + versions
hactl cc show hacs                        # CC details + entity count
hactl cc logs hacs --unique               # CC-specific errors, deduplicated
```

Log source: WS `system_log/list` (structured, preferred) with automatic fallback to REST `/api/error_log`.

`hactl log` shows **Home Assistant core** logs only. Add-on logs (including the
companion's own WireGuard/dyndns monitor output) run in a separate Supervisor
container and never reach the core logger — they will **not** appear here. To read
the companion's own logs, use `hactl companion logs` (see below).

```bash
hactl companion logs                           # recent companion add-on logs
hactl companion logs --component wireguard      # just the WG tunnel + dyndns monitor
hactl companion logs --component wireguard --since 1h --level warning
```

Companion logs come from an in-memory ring buffer on the add-on, fetched over the
same Ingress lifeline as the other companion commands. `--since`/`--top` set the
time window and max line count. Requires hactl-companion.

### Cache & version

```bash
hactl cache status                        # age + size + item counts per category
hactl cache refresh traces                # pull fresh trace data
hactl cache refresh                       # refresh everything
hactl cache clear                         # wipe all local cache

hactl version                             # version, commit, build date
hactl rtfm                                # print this manual (for LLM self-teaching)
```

### WireGuard (companion lifeline)

```bash
hactl companion wireguard status                       # tunnel state, handshake, rx/tx, monitor
hactl companion wireguard config -f peer.conf --confirm # push a .conf (persisted on /data)
hactl companion wireguard up --confirm                 # bring the tunnel up now
hactl companion wireguard down --confirm               # bring the tunnel down now
```

Manages the companion's WireGuard tunnel — the remote lifeline hactl rides over. The
endpoints are Ingress-only (a bare bearer token gets 401); this command handles the
Supervisor Ingress session auth automatically. Configs persist on the add-on's `/data`
volume; `up`/`down` only affect the live interface. To have the tunnel return after a
reboot, set the add-on's `vpn.enabled` option (it reconciles on every add-on (re)start).
Mutations are dry-run by default — pass `--confirm` to apply. Use `--tunnel <name>` for a
non-default tunnel (default `wg0`). Requires hactl-companion.

---

## Filtering & discovery

> **Stop at the first miss.** If a pattern or entity ID returns empty or 404, report it and stop. Do not chain fallback patterns or broaden the search unless the user explicitly asks.

Three commands support `--pattern` (glob or substring on the item ID):

```bash
hactl auto ls --pattern victron           # substring: matches "victron" anywhere in ID
hactl auto ls --pattern 'victron_*'       # glob: IDs starting with victron_
hactl script ls --pattern kino
hactl ent ls --pattern 'sensor.wp_*'
```

Pattern with `*` or `?` → glob. Otherwise → case-sensitive substring.

`ent ls` also accepts three additional independent filters — combine freely:

```bash
hactl ent ls --domain binary_sensor --area garage
hactl ent ls --label energy --pattern 'sensor.*'
```

`auto ls` and `script ls` support `--label` to filter by label name (uses HA entity registry labels),
and `--failing` to show only items with recent errors:

```bash
hactl auto ls --label victron             # automations with label "victron"
hactl auto ls --failing                   # automations with recent trace errors
hactl script ls --label energy            # scripts with label "energy"
hactl script ls --failing                 # scripts with recent trace errors
```

For broader entity discovery when you have an entity but want context:

```bash
hactl ent related sensor.wp_vl            # spiders automations, device siblings, area neighbors
```

---

## Output conventions

- **Token header:** Every response starts with `[~N tok]` so you know the cost at a glance.
- **Token cap:** Output is truncated at `--tokensmax` tokens (default 500). A command-specific hint is appended when truncation occurs (e.g. `log` suggests `--component`, `ent ls` suggests `--domain`). Use `--tokensmax=0` to disable. Use filters to reduce output rather than raising the cap.
- **Tables:** one header line, one row per item. `…+N more` for overflow. Control with `--top`.
- **Stable IDs:** `trc:a7`, `anom:g3`, `log:f2` — short, persistent in `cache/ids.json`. Safe to reference in follow-up calls.
- **Timestamps:** short form (`09:42`, `04-16 09:42`). ISO only with `--full`.
- **No decoration:** no emojis, no color (unless `--color`). Clean for parsing.
- **JSON mode:** `--json` returns structured JSON. Use when extracting specific fields. JSON output is never truncated by `--tokensmax` (the token estimate goes to stderr instead) — on large datasets apply filters first.
- **`--stats`:** prints raw response size + estimated token count to stderr after any command.

---

## Global flags

| Flag | Default | Effect |
|------|---------|--------|
| `--dir` | auto | Instance directory (overrides `HACTL_DIR` and auto-discovery) |
| `--since` | `24h` | Time range (`1h`, `7d`, `30d`, …) |
| `--top` | `10` | Max rows in tables (CLI only — not a tool kwarg; use filters instead) |
| `--full` | off | Raw/verbose output |
| `--json` | off | JSON output |
| `--color` | off | ANSI colors |
| `--stats` | off | Print response size + token estimate to stderr |
| `--tokensmax` | `500` | Cap output at N tokens; `0` = no cap |
| `--timeout` | `30s` | Per-request timeout for HA/companion API calls |

---

## Agent workflows

> **Rule:** Call `hactl rtfm` as the first tool call in every session. It prints the current manual so subsequent calls use accurate command syntax. `hactl rtfm` is uncapped by default — pass `--tokensmax=N` only when you want to truncate it.

### "Why did my automation fail?"
```
hactl auto ls --failing
# if --failing is empty: check the error log for automation names
hactl log --errors --unique
hactl auto show <id>
hactl trace show <trc:XX>
```

### "Is this sensor behaving normally?"
```
hactl ent hist <id> --since 7d
hactl ent anomalies <id>
```

### "What else is related to this entity?"
```
hactl ent related <entity_id>
hactl ent ls --area <area> --domain sensor
```

### "Deploy an automation change"
```
hactl auto diff <id> -f new.yaml
hactl auto apply <id> -f new.yaml --confirm
hactl auto show <id>
```

### "Create a new automation / script / helper"
```
hactl auto create -f auto.yaml              # dry-run preview
hactl auto create -f auto.yaml --confirm    # create + reload
hactl script create -f script.yaml --confirm
hactl helper create input_boolean -f toggle.yaml --confirm
```

### "Delete an automation / helper"
```
hactl auto delete <id>                      # dry-run preview
hactl auto delete <id> --confirm            # delete + reload
hactl helper delete <id> --confirm
```

### "Organize entities with labels"
```
hactl label ls
hactl label create "Solar" --icon mdi:solar-power
hactl ent ls --pattern 'sensor.solar_*'
hactl ent set-label sensor.solar_power solar
hactl auto ls --label solar
```

### "Find and act on a group of automations"
```
hactl auto ls --pattern victron
hactl svc call automation.turn_off -d '{"entity_id":"automation.victron_charge"}'
hactl auto ls --label victron            # verify
```

### "What went wrong recently?" / "What broke?"
```
hactl health
hactl log --errors --unique
hactl changes --since 24h
```

### "Build a dashboard" / "Design or modify a dashboard"
```
hactl ent ls --pattern <topic>             # discover entities (one call, stop here)
# --- confirm with user before writing ---
hactl dash create --url-path <path> --title "<title>" --icon mdi:home --confirm
hactl dash save <url_path> --file dash.json --confirm
hactl dash show <url_path>                 # verify (url_path from `dash ls`)
```

---

## Multiple instances

```
~/ha/
  home/     .env  cache/
  cabin/    .env  cache/
  testbed/  .env  cache/
```

```bash
hactl --dir ~/ha/home health
hactl --dir ~/ha/cabin auto ls --failing
```

No global config, no profiles. Directory = instance.

---

## MCP server

`hactl mcp` serves this CLI over the Model Context Protocol on stdio. MCP clients see a single `hactl` tool that takes a command line (without the binary name), e.g. `{"command": "ent ls --domain light"}`. All commands and flags in this manual work unchanged; this manual is also available as the MCP resource `hactl://manual`.

```bash
claude mcp add hactl -- hactl mcp --dir ~/.hactl/default
```

- Read-only by default: mutating commands (`svc call`, `auto apply`, create/delete, `script run`, …) are rejected with an error. Start the server with `hactl mcp --allow-writes` to permit them; the dry-run + `--confirm` write path still applies.
- One instance per server process. A `--dir` given at server start pins every call to that instance; a per-call `--dir` overrides it.
- `setup`, `completion`, and `mcp` itself are never available over MCP; unclassified commands fail closed.
