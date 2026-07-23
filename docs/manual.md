# hactl Manual — LLM Usage Guide

> For agents using hactl as a tool. Assumes familiarity with Home Assistant concepts.

## Quick routing

Match the user's question here first and run exactly the listed sequence — complete it before drilling into any single finding.

| User asks | Run, in order | Notes |
|---|---|---|
| "What went wrong?" / "What broke?" | `health`, `log --errors --warnings --unique`, `changes --since 24h` | All three first; `log show <id>` only afterwards. Many operational signals ("skipping X", "no fallback") are WARNINGs, not errors |
| "Daily report" / "Morning check" / "Status" | `health`, `issues`, `log --errors --warnings --unique`, `changes --since 24h` | Summarize per section |
| "Which automation failed?" | `auto ls --failing`; if empty `log --errors --warnings --unique` | `trace show` only when a failure appears |
| "Is <sensor> behaving normally?" | `ent anomalies <id>` | `ent hist <id>` if anomalies found |
| "Which entities belong to <concept>?" | `device ls --name <shortest-term>`, `device show <closest match>` | Fallbacks: `label ls`, `ent ls --pattern '*<term>*'` |
| "Disable / turn on / trigger X" | verify (`auto show` / `ent show`), then `svc call` dry-run | `--confirm` only after the user confirms the plan |
| "Build / change a dashboard" | `ent ls --pattern <topic>`, then `dash create` dry-run | Same confirmation rule |
| "List labels / areas / helpers / scripts" | exactly: `label ls` · `area ls` · `helper ls` · `script ls` | One call, answer. `helper` is not an entity domain — never `ent ls --domain helper` |

Full command set (family → subcommands):

- `health` · `issues` · `changes` · `log [show <id>]` · `cc ls|show|logs` · `trace show <id>`
- `ent ls|show|hist|anomalies|related|who|set-label|set-area`
- `auto ls|show|cat|diff|apply|create|delete|rollback` · `script ls|show|cat|run|diff|apply|create|delete`
- `helper ls|show|cat|create|delete` · `tpl eval|cat|create|delete` · `svc call`
- `dash ls|show|save|create|delete|resources|grep|replace`
- `device ls|show` · `label|area|floor ls|create|delete`
- `config entries|show|options|delete|files|file|block|flow-start|flow-step|flow-inspect`
- `ref scan|replace|validate` · `cache status|refresh|clear` · `companion status|logs|wireguard`

No other commands exist — never invent one. Flags unclear: `<command> --help`; full manual: `rtfm`.

## Mental model

hactl is a read-heavy CLI. Most commands query HA via REST/WebSocket, condense the result, and print compact text. One directory = one HA instance. All state lives in `.env` (credentials) + `cache/` (SQLite + JSONL).

**Token budget:** Output is capped at 500 tokens by default (`--tokensmax=500`). Raise the cap (`--tokensmax=2000`) or remove it entirely (`--tokensmax=0`) when you need full output. Add `--tokens` to print a compact `[~N tok]` estimate, or `--stats` to see response size + estimated token count on stderr.

## Agent workflows

> **Rule:** Call `hactl rtfm` as the first tool call in every session. It prints the current manual so subsequent calls use accurate command syntax. `hactl rtfm` is uncapped by default — pass `--tokensmax=N` only when you want to truncate it.

### "Why did my automation fail?"
```
hactl auto ls --failing
# if --failing is empty: check the error log for automation names
hactl log --errors --warnings --unique
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

### "Which entities belong to <concept>?" (find things by concept)
```
hactl device ls --name <term>
hactl device show <closest match>
```
Search with the shortest distinctive substring — `heat`, not `heat pump`; device and entity names are often localized or vendor-specific. When a listing returns near-miss candidates, inspect the closest match with `device show` before asking the user. If devices yield nothing, fall back to `hactl label ls`, then `hactl ent ls --pattern '*<term>*'`.

### "Deploy an automation change"
```
hactl auto diff <id> -f new.yaml
hactl auto apply <id> -f new.yaml --confirm
hactl auto show <id>
```

### "Deploy a script change"
```
hactl script diff <id> -f new.yaml
hactl script apply <id> -f new.yaml --confirm
hactl script show <id>
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
hactl auto ls --label victron
```
`svc call` is dry-run by default: it prints the planned call without executing. Repeat with `--confirm` only after the user confirms the plan; the final `auto ls` verifies the result.

### "What went wrong recently?" / "What broke?"
```
hactl health
hactl log --errors --warnings --unique
hactl changes --since 24h
```
Complete all three before drilling into a single entry with `log show` — breadth first, depth only where the sweep flagged something.

### "Show me the daily report" / "Morning check" / "Status summary"
```
hactl health
hactl issues
hactl log --errors --warnings --unique
hactl changes --since 24h
```
Run all four, then summarize per section: system health, open issues, errors, notable changes.

### "Build a dashboard" / "Design or modify a dashboard"
```
hactl ent ls --pattern <topic>
hactl dash create --url-path <path-with-hyphen> --title "<title>"
hactl dash save <url_path> -f dash.json
hactl dash show <url_path>
```
One discovery call, then stop. `dash create` and `dash save` are dry-run by default: they preview without writing. Present the dry-run plan and wait for the user's explicit confirmation before repeating a command with `--confirm`. The original request ("build me a dashboard") is not that confirmation.

---

## Setup

Your instance is normally configured already — verify with `hactl health`. Instance selection: a directory with a `.env` (`HA_URL`, `HA_TOKEN`) is one instance; select it with `--dir <path>` or `HACTL_DIR`, otherwise hactl walks up from the current directory and falls back to `~/.hactl/default/`.

If hactl cannot connect: `hactl companion status` prints a one-screen connectivity diagnostic. Human-facing installation and troubleshooting live in `docs/setup.md`.

---

## Command Reference

### Setup & health

```bash
hactl setup                   # interactive first-time setup: prompts for HA_URL + HA_TOKEN, writes .env in the current dir (or --dir)
hactl setup --url http://ha:8123 --token <token>   # non-interactive (agents/scripts); --token - reads from stdin; --force overwrites
hactl health                  # HA version, state, recorder, location, timezone, error count
hactl health --json            # same as structured JSON
hactl issues                  # active HA repairs/issues, every severity incl. WARNING (domain, severity, fixable, ignored, breaks_in)
hactl issues --all            # also include ignored (dismissed) issues
hactl changes --since 24h     # logbook: what changed recently (state changes, auto triggers)
```

### Automations

```bash
hactl auto ls                             # table: id, state, area, labels, runs_24h, errors, last_err
hactl auto ls --failing                   # only automations with recent errors
hactl auto ls --pattern 'ess_*'           # glob/substring filter on automation ID
hactl auto ls --label victron             # filter by label name (substring)
hactl auto ls --restored                  # only "ghost" automations (restored from registry, no live config)
hactl auto show climate_schedule          # config summary + last 5 traces with stable IDs
hactl auto cat climate_schedule           # the automation's remote YAML, verbatim (no header)
hactl trace show trc:a7                   # condensed trace (trigger → condition → action, pass/fail)
hactl trace show trc:a7 --full            # raw trace JSON
```

`auto show` summarizes; `auto cat` prints the stored config itself, so it is what
you feed back into `auto diff -f` / `auto apply -f`. It accepts the config `id:`,
the entity_id, or the alias, and needs the companion. Output is YAML by design —
`--json` does not change it (same for `script|helper|tpl cat`, `auto|script diff`,
`tpl eval`, `config file|block`).

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
hactl script cat kino_start        # the script's remote YAML, verbatim (needs companion)
hactl script run kino_start        # dry-run: verify the script exists + preview
hactl script run kino_start --confirm  # execute via script.turn_on
```

`state` column: `off` = idle, `on` = currently running. `script cat` prints the
`scripts.yaml` top-level-key form (`kino_start:` → definition), which is exactly
what `script apply -f` accepts back.

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
hactl ent ls --restored                   # only "ghost" entities (restored from registry, no live entity)
hactl ent show sensor.wp_vl               # state + key attributes + area + labels + attribute count
hactl ent show sensor.wp_vl --full        # + all attributes
hactl ent hist sensor.wp_vl --since 7d    # ~50 resampled datapoints (time/value)
hactl ent hist sensor.wp_vl --resample 5m # override bucket size
hactl ent hist sensor.wp_vl --attr brightness  # track attribute instead of state
hactl ent anomalies sensor.wp_vl          # gaps (>1h), stuck (>2h/24h), spikes (z>3)
hactl ent related sensor.wp_vl            # related automations, device siblings, area neighbors
hactl ent who light.kitchen --since 7d    # who/what changed it: per-event + counts summary
```

`ent hist` auto-resamples to ~50 points. For binary/non-numeric entities the timeline shows time/state/duration. Anomaly detection runs client-side on cached history.

`ent show` closes with `attributes: N total; use --full to see all`. `N` is the
entity's **whole** attribute count, not the number withheld — the four it always
shows (`friendly_name`, `unit_of_measurement`, `device_class`, `restored`) are
included in it.

`ent show` includes a `changed_by:` line attributing the most recent change to a user (e.g. `User Jan`) or to `Home Assistant` when no user_id was on the state's `context`. `ent who` does the deeper attribution — it queries the logbook for the entity, classifies each event as `User <name>`, `Automation: <alias>`, `Script: <id>`, `Device: <name>`, or `Home Assistant`, and aggregates a counts summary (`Jan: 12, Automation 'Sunset lights': 5, ...`). `--json` returns `{events, summary, window}`. Resolving user UUIDs to names requires an admin long-lived access token; with a non-admin token the user list call is admin-denied and the output falls back to raw UUIDs while automation/script/device attribution continues to work.

The `changes` command also gained a `who` column carrying the same per-event label.

### Devices

```bash
hactl device ls                           # device_id, name, area, labels, entity count
hactl device ls --pattern '*heat*'        # glob/substring on device ID or name
hactl device ls --area basement           # filter by area name or ID
hactl device ls --label heat_pump         # filter by label name or ID
hactl device show summt_heizung           # device profile + registered entities
```

LLM workflow for area assignment: discover the device with `device ls`, inspect its entities with `device show`, preview one entity update with `ent set-area <entity_id> <area>`, then repeat the exact command with `--confirm` only after the user confirms the entity and target area.

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

hactl ent set-label sensor.wp_vl energy                # dry-run: preview merged labels
hactl ent set-label sensor.wp_vl energy --confirm      # assign label(s) (by ID or name)
hactl ent set-area  sensor.wp_vl living_room            # dry-run
hactl ent set-area  sensor.wp_vl living_room --confirm  # set entity area
```

Labels and areas are applied via the entity registry (dry-run by default; `--confirm` to apply). Multiple labels can be passed to `set-label` at once.

### Write path (automations)

```bash
hactl auto diff climate_schedule -f new.yaml          # diff local vs remote
hactl auto apply climate_schedule -f new.yaml          # dry-run (default, no write)
hactl auto apply climate_schedule -f new.yaml --confirm  # write + reload
hactl auto rollback                                    # dry-run: preview the backup that would be restored
hactl auto rollback --confirm                          # undo last backup
hactl auto rollback climate_schedule --confirm         # undo specific automation

```

**Safety:** `apply` and `rollback` without `--confirm` are always a dry-run and write nothing (no backup files either). The candidate's trigger/condition/action blocks are validated against HA's real config schema (WS `validate_config`) in both dry-run and confirm mode — an invalid config aborts before anything is written. On `--confirm`, a backup of the current config is saved to `backups/` before the write, and HA's Config API validates again on write.

### Write path (scripts)

```bash
hactl script diff kino_start -f new_script.yaml
hactl script apply kino_start -f new_script.yaml             # dry-run (default, no write)
hactl script apply kino_start -f new_script.yaml --confirm   # write via companion + reload
```

Requires hactl-companion. Input may be UI-style script YAML (`alias`, `sequence`, `mode`, ...) or `scripts.yaml` top-level-key form (`kino_start: ...`). Confirmed applies validate the candidate `sequence` against HA's action schema before writing and save a local backup under `backups/`.

### Templates — create & delete

```bash
hactl tpl create -f sensor_tpl.yaml                  # dry-run
hactl tpl create -f sensor_tpl.yaml --confirm        # create via companion + reload
hactl tpl create -f binary_tpl.yaml --domain binary_sensor --confirm  # non-default domain
hactl tpl create -f trigger_block.yaml --confirm     # trigger-based (full block, see below)
hactl tpl cat my_template_uid                        # the entry's remote YAML, verbatim
hactl tpl delete my_template_uid                     # dry-run
hactl tpl delete my_template_uid --confirm           # delete via companion + reload
```

Requires hactl-companion. Default domain is `sensor`. A full block may declare
any template entity domain (sensor, binary_sensor, number, select, button,
weather, light, switch, cover, fan, lock, vacuum, alarm_control_panel, event,
image, device_tracker, update).

The `-f` file is either a **bare entity item** (state-based; placed into a block
for `--domain`) or a **full block** for trigger-based / multi-domain entries. In
HA's `template:` schema the trigger lives at the *block* level, never inside the
entity — a trigger nested in an entity item is rejected with guidance:

```yaml
# trigger_block.yaml — a full block
triggers:
  - trigger: state
    entity_id: sensor.source
sensor:
  - name: Sampled
    unique_id: sampled
    state: "{{ trigger.to_state.state }}"
```

`--domain` applies only to the bare-item form; it is ignored for a full block
(the block declares its own domains). Deleting the last entity of a trigger
block removes the whole block, so no orphan trigger is left behind.

### Helpers

```bash
hactl helper ls                                      # list all helpers
hactl helper ls --domain input_boolean               # filter by domain
hactl helper show guest_mode                         # id + domain header, then the YAML definition
hactl helper cat guest_mode                          # the same YAML with no header (pipe-friendly)
hactl helper create input_boolean -f toggle.yaml             # dry-run
hactl helper create input_boolean -f toggle.yaml --confirm   # create via companion + reload
hactl helper delete guest_mode                       # dry-run
hactl helper delete guest_mode --confirm             # delete via companion + reload
```

Supported domains: input_boolean, input_number, input_select, input_text, input_datetime, counter, timer, schedule. Requires hactl-companion.

The `-f` file must be a **keyed mapping with exactly one top-level key** — the
helper id — not a bare definition:

```yaml
# toggle.yaml
guest_mode:
  name: Guest Mode
  icon: mdi:toggle-switch
```

A bare `name:`/`icon:` mapping is rejected on `--confirm` (400 from the
companion). The dry-run does not read the file's contents, so it previews an
invalid file as happily as a valid one — validate the shape yourself before
confirming.

### Templates & services

```bash
hactl tpl eval '{{ states("sensor.temperature") | float * 2 }}'
hactl tpl eval -f my_template.j2          # read from file

hactl svc call light.turn_on -d '{"entity_id":"light.kitchen","brightness":200}'
hactl svc call light.turn_on -d '{"entity_id":"light.kitchen","brightness":200}' --confirm
hactl svc call weather.get_forecasts -d '{"entity_id":"weather.home","type":"daily"}' --return --confirm
hactl svc call light.turn_on -d @payload.json --confirm
```

Templates evaluated server-side by HA's Jinja engine — semantically correct, including `states()` and custom filters.

`svc call` is dry-run by default and prints the planned call; `--confirm` executes it (only after the user confirmed). `--return` prints the service response for services that support `return_response` (e.g. `weather.get_forecasts`, `calendar.get_events`). `-d @file.json` reads the payload from a file and avoids shell quoting.

### Config entries & flows

```bash
hactl config entries                              # list config entries (entry_id, domain, title, state, source, options, disabled_by)
hactl config entries --domain zha                 # filter by integration domain
hactl config show <entry_id>                      # what an integration is set up as AND how it's configured (read-only)
hactl config show <entry_id> --probe-options-flow # when no diagnostics platform: read current values via a transient options flow
hactl config delete <entry_id>                    # delete a config entry (dry-run; add --confirm to apply)
hactl config options <entry_id>                   # start options flow for an existing config entry (dry-run; --confirm to start)
hactl config flow-start <domain>                  # start a new config flow for a domain/integration (dry-run; --confirm to start)
hactl config flow-step <flow_id> --data '{...}'   # submit data to advance a flow step (dry-run; --confirm to submit)
hactl config flow-step <flow_id> --data '{...}' --options  # same, but for an options flow
hactl config flow-inspect <flow_id>               # inspect current flow state (step, schema, errors)
hactl config flow-inspect <flow_id> --options     # same, but for an options flow

hactl config files                                # list configuration.yaml and every !include'd file
hactl config file automations.yaml                # print a config file as YAML, !include's resolved
hactl config file configuration.yaml --raw        # leave !include directives unresolved
hactl config block automations.yaml climate_schedule  # one keyed block from a file
```

`files`/`file`/`block` read the config directory **through the companion** and
are the only `config` subcommands that need it. `file` without `--raw` returns
the merged document (an `automation: !include automations.yaml` line comes back
as the inlined list); `--raw` returns the file's own bytes. `block` matches on
`id`, `unique_id`, or the top-level key and prints that block verbatim, so its
output may carry a trailing comment line that sits before the next key. All
three are YAML-only — `--json` is accepted but does not change the output. A
missing file or block is an error with a non-zero exit, not an empty result.

`options`, `flow-start`, and `flow-step` are dry-run by default (they start or advance a stateful flow, and a step can complete the flow and create a config entry) — add `--confirm` to actually start/submit. `entries`, `flow-inspect`, and `--json` reads are always live.

Config flows are multi-step and stateful. An LLM agent driving integration setup uses this pattern:

```bash
# 1. Start a flow (dry-run first to preview, then --confirm to actually start)
hactl config options abc123-entry-id --confirm --json
# → {"flow_id":"xyz","type":"form","step_id":"init","data_schema":[...]}

# 2. Submit data to advance
hactl config flow-step xyz --data '{"action": "add_device"}' --options --confirm --json
# → {"flow_id":"xyz","type":"form","step_id":"select_device","data_schema":[...]}

# 3. Complete the flow
hactl config flow-step xyz --data '{"device_type": "heat_pump"}' --options --confirm --json
# → {"flow_id":"xyz","type":"create_entry","title":"Heat Pump"}
```

Some steps contain **expandable sections** (schema fields of type `expandable`, e.g. the Generic Camera `advanced` section). Their fields must be nested under the section name in `--data`, not passed flat — otherwise HA returns a 400. `flow-inspect` shows the nested fields (as `advanced.framerate`) and prints the exact nesting to use:

```bash
hactl config flow-step xyz --data '{"stream_source": "rtsp://...", "advanced": {"framerate": 2, "verify_ssl": false}}' --confirm
```

When a step fails, the HA error detail (e.g. the offending field) is included in the error message.

When starting a *new* integration (not reconfiguring an existing entry), use `flow-start` + `flow-step` without `--options`.

To **read back** how an entry is currently configured (e.g. to confirm a value you just set via an options flow), use `config show <entry_id>` — do not infer configuration from behavior. It prints the setup summary (domain, state, source, options/reconfigure support, disabled/failure reason) plus the current configuration, sourced from the integration's diagnostics dump (secrets redacted by the integration). When the integration ships no diagnostics platform, pass `--probe-options-flow` to read current values from a transient options flow (started and immediately aborted); without the flag no options flow is started and the note tells you to add it. The `config_source` field (`diagnostics` | `options_flow` | `unavailable`) tells you which. Read-only; needs an admin token.

Every `config` command except `files`/`file`/`block` uses HA's REST API directly — no companion needed. Add `--json` for structured output suitable for LLM consumption.

### Dashboards (Lovelace)

```bash
hactl dash ls                                      # list all dashboards (url_path, title, mode)
hactl dash ls --json                               # structured JSON for all dashboards
hactl dash show                                    # views summary for default dashboard
hactl dash show my-dashboard                       # views summary by url_path (from `dash ls`, NOT a view path)
hactl dash show my-dashboard --json                # pretty-printed full config JSON
hactl dash show my-dashboard --raw                 # raw HA JSON (for round-trip editing)
hactl dash show my-dashboard --view living-room    # single view detail as JSON
hactl dash show my-dashboard --view living-room --raw  # raw JSON for only that view

hactl dash create --url-path my-dash --title "My Dashboard" --icon mdi:home --confirm
hactl dash save my-dash --file config.json --confirm  # write full config (dry-run without --confirm)
hactl dash delete my-dash --confirm

hactl dash resources                               # list custom card/CSS resources

hactl dash grep sensor.wp_vl                       # where is this string used, across all dashboards
hactl dash replace sensor.old sensor.new my-dash   # dry-run: rename within one dashboard
hactl dash replace sensor.old sensor.new my-dash --confirm  # apply
```

**LLM round-trip workflow:** `dash show --raw` → modify JSON → `dash save --file`. Config replacement is always full — HA has no partial update API. `--view` scopes inspection output only; do not feed a single-view object to `dash save`.

**`grep` and `replace` work on string values, not on entity fields.** Both walk
each dashboard's JSON and match any string **equal to** the argument, wherever it
sits — a card's `entity`, but equally a markdown card whose `content` is exactly
that string, or a view `title`. `dash grep P` finds a view titled `P`. Matching
is whole-value, so a mention *inside* a longer sentence is not a hit, and map
keys are never matched or rewritten. Output is `dashboard` + `path`
(`views[0].cards[1].content`); a miss prints "not referenced in any dashboard"
and exits 0.

`dash replace` takes one dashboard (omit `url_path` for the default dashboard,
which fails with `config_not_found` when the default is HA's auto-generated one),
is dry-run until `--confirm`, and rewrites every matching value at once. To
rename across config files *and* dashboards in a single pass, use `ref replace`.

> **Skill:** For LLM agents designing dashboards, load the `lovelace-design` skill (`.github/skills/lovelace-design/SKILL.md`). It covers card types, grid sizing, layout patterns, and common pitfalls.

### References (find and rename entity_ids)

```bash
hactl ref scan sensor.wp_vl                    # every reference, config files + dashboards
hactl ref validate                             # dangling references: pointers to entities that are gone
hactl ref validate --exit-code                 # exit 1 if any dangling reference is found (CI gating)
hactl ref replace sensor.old sensor.new        # dry-run: rename everywhere
hactl ref replace sensor.old sensor.new --confirm   # apply
```

Requires hactl-companion — it is what reads the config directory. `ref` is the
whole-instance version of `dash grep`/`dash replace`: it covers YAML config
files (following `!include`) **and** every dashboard in one pass.

`scan` reports `source` (`config` | `dashboard`), `location` (file name or
dashboard) and `path` (`[1].trigger[0].entity_id`). Same whole-value matching as
`dash grep`.

`validate` is the one to reach for after deleting or renaming an entity: it
sweeps for entity references that no longer resolve. The live set is the union of
the entity registry and current states, so state-only entities (`sun.sun`,
`zone.home`, `weather.*`, template sensors) are not falsely flagged. It is
deliberately conservative — only values in known entity-holding positions are
checked (`entity_id`/`entity` in config; `entity`/`entities`/`badges`/
`camera_image` in dashboards), so `light.turn_on` is never mistaken for an
entity. Two blind spots are the accepted price of zero false positives:
**entities inside templates** (`{{ states('sensor.x') }}`) and entities under
non-standard custom-card keys. `validate` reports; it never fixes.

`replace` is dry-run until `--confirm`. It aborts before writing anything if the
companion cannot be reached, because a rename that silently skips config files is
the exact failure this command exists to prevent. References in YAML-mode
dashboards are reported but not rewritten (hactl cannot write those). A confirmed
run is idempotent, so re-running after a partial failure is safe.

### Logs & custom components

```bash
hactl log --errors                        # ERROR-level entries only
hactl log --warnings                      # WARNING-level entries only (operational signals)
hactl log --errors --warnings --unique    # both levels, deduplicated, sorted by count
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

> **Verify before answering "none".** An empty listing only proves the filter you used. If a flag value was guessed (a domain, label, or area name), confirm it exists (`--help`, the matching registry `ls`, or `rtfm`) before reporting a negative result — that one verification call is exempt from the stop-at-first-miss rule.

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

`--restored` filters both `ent ls` and `auto ls` down to ghost entities — see
below.

### Ghost entities (`--restored`)

HA marks a state `restored: true` when it was
resurrected from the entity registry/recorder with no live platform entity behind
its `unique_id` — the automation/helper/script was deleted or re-authored under a
new `id`, so there is **no config left to repair** (nothing for `ref scan`/`ref
replace` to find). These show as `state: unavailable` and are easy to confuse with
a genuinely broken config. `ent ls --restored` / `auto ls --restored` list only
these ghosts (and a `restored` column appears automatically whenever any listed
row is one); `ent show` / `auto show` flag it on the single-item view. Use this to
triage `unavailable` entities into "ghost, purge in the HA UI" vs. "broken
reference, fix with `ref replace`" before spending repair effort:

```bash
hactl ent ls --restored --domain automation   # ghost automations to clean up
hactl auto ls --restored                       # same, automation-scoped table
```

---

## Output conventions

- **Token estimate:** Add `--tokens` to print a compact `[~N tok]` estimate (`stderr` in JSON mode).
- **Token cap:** Output is truncated at `--tokensmax` tokens (default 500). A command-specific hint is appended when truncation occurs (e.g. `log` suggests `--component`, `ent ls` suggests `--domain`). Use `--tokensmax=0` to disable. Use filters to reduce output rather than raising the cap.
- **Tables:** one header line, one row per item. `…+N more` for overflow. Control with `--top`.
- **Stable IDs:** `trc:a7`, `anom:g3`, `log:f2` — short, persistent in `cache/ids.json`. Safe to reference in follow-up calls.
- **Timestamps:** tables print short form (`09:42` today, `04-16 09:42` otherwise); `--full` does **not** make them ISO. `--json` gives ISO for item/event views (`ent show`, `changes`, `ent who`); table listings serialize the rendered row, so there the short string survives and numbers come back as strings (`"runs_24h": "0"`).
- **No decoration:** no emojis, no color. `--color` is accepted and does nothing; it is kept so existing callers do not break.
- **JSON mode:** `--json` returns structured JSON. Use when extracting specific fields. Never truncated by `--tokensmax` (`--tokens` prints the estimate to stderr) — on large datasets filter first. Verbatim commands ignore it (`auto|script|helper|tpl cat`, `auto|script diff`, `tpl eval`, `config file|block`), as do dry-run previews: those always print text.
- **`--stats`:** prints raw response size + estimated token count to stderr after any command.

---

## Global flags

| Flag | Default | Effect |
|------|---------|--------|
| `--dir` | auto | Instance directory (overrides `HACTL_DIR` and auto-discovery) |
| `--since` | `24h` | Time range (`1h`, `7d`, `30d`, …) |
| `--top` | `10` | Max rows in tables (CLI only — not a tool kwarg; use filters instead). `--json` returns the full set regardless |
| `--full` | off | Raw/verbose output, per command: all attributes for `ent show`, raw JSON for `trace show`. Changes nothing on tables |
| `--json` | off | JSON output |
| `--color` | off | No-op — accepted, changes nothing |
| `--stats` | off | Print response size + token estimate to stderr |
| `--tokens` | off | Print compact token estimate |
| `--tokensmax` | `500` | Cap output at N tokens; `0` = no cap |
| `--timeout` | `30s` | Per-request timeout for HA/companion API calls |

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

## Manual delivery

Parts of this manual may already have reached you automatically: when both stdout and stderr are captured (agent/shell-tool usage), hactl delivers the manual progressively on stderr — the core (routing table, conventions, flags) with the first command of a session, each family's how-to with the first command of that family, ending with a `=== RESULT of hactl … ===` marker before the real output. Sessions are per instance, keyed by `HACTL_SESSION` (default: a shared key with a 30-minute idle timeout).

- `HACTL_MANUAL_MODE`: `progressive` (default) | `full` (whole manual once) | `off`
- `hactl rtfm --core` / `--family <name>` / `--families` fetch subsets on demand
- Humans at a terminal never see it; stdout (incl. `--json`) stays untouched; `rtfm`, `mcp`, `setup`, `version`, `help`, `completion` never trigger it

---

## MCP server

`hactl mcp` serves this CLI over the Model Context Protocol on stdio. MCP clients see a single `hactl` tool that takes a command line (without the binary name), e.g. `{"command": "ent ls --domain light"}`. All commands and flags in this manual work unchanged; this manual is also available as the MCP resource `hactl://manual`. Over MCP the full manual arrives once with the first tool result — the progressive stderr delivery above applies to plain CLI usage only.

```bash
claude mcp add hactl -- hactl mcp --dir ~/.hactl/default
```

- Read-only by default: mutating commands (`svc call`, `auto apply`, `script apply`, create/delete, `script run`, …) are rejected with an error. Start the server with `hactl mcp --allow-writes` to permit them; the dry-run + `--confirm` write path still applies.
- One instance per server process. A `--dir` given at server start pins every call to that instance; a per-call `--dir` overrides it.
- `setup`, `completion`, and `mcp` itself are never available over MCP; unclassified commands fail closed.
