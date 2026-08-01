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
| "Why did X turn on?" / "What controls <entity>?" | `ent who <id>`, `ref scan <id>` | `who` = actor history; `scan` = config/dashboard references — inspect hits with `auto show` |
| "Which entities belong to <concept>?" | `ent ls --pattern '*<term>*'` (ids), `device ls --name <term>`, or the name search under "Filtering & discovery" | `--pattern` matches ids, never display names |
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

**Token budget:** output is capped at 500 tokens by default — `--tokensmax=N` raises the cap, `0` removes it (details under Output conventions).

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
hactl ent ls --pattern '*<term>*'
hactl device ls --name <term>
hactl tpl eval "{{ states | selectattr('name','search','(?i)<term>') | map(attribute='entity_id') | list }}"
```
Pick the query that matches the kind of term you hold: `ent ls --pattern` matches entity_ids only, `device ls --name` matches device display names, and the template resolves entity **display names** — the only query that finds `light.ap_gast_v2_led` when the user says "AP6 Flur LED". Search with the shortest distinctive substring — `heat`, not `heat pump`; names are often localized or vendor-specific. Inspect near-miss candidates (`device show`, `ent show`) before asking the user. `label ls` / `area ls` cover concepts that are organized rather than named.

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
hactl auto create -f auto.yaml              # dry-run preview — parses and validates the file
hactl auto create -f auto.yaml --confirm    # create + reload
hactl script create -f script.yaml --confirm
hactl helper create input_boolean -f toggle.yaml --confirm
```
`script create` and `helper create` take a mapping with **exactly one top-level key — the id** (`my_toggle:` with `name:`/`icon:` nested under it); a bare `name:`/`icon:` mapping is rejected. That id becomes the entity's object id, so it must be one HA can use: lowercase letters, digits and single underscores, not starting or ending with one. `helper create` refuses anything else, and refuses a domain outside its eight, before it plans or sends anything. `tpl create` takes an entity item or a full block instead (see Templates). If HA does not confirm the reload, the command says so — a written file HA never read produces no entity.

### "Delete an automation / helper"
```
hactl auto delete <id>                      # dry-run preview — errors if <id> is not real
hactl auto delete <id> --confirm            # delete + reload
hactl helper delete <id> --confirm
```
The preview names what would go (the alias, the domain, the entity_id). Deleting also removes the entity's registry entry, so the id does not linger as an `unavailable` ghost — see "Ghost entities".

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

If hactl cannot connect: `hactl companion status` prints a one-screen connectivity diagnostic and **exits 1 when the companion is not usable**, naming the cause it observed — a token HA rejected, an `HA_URL` that redirects to another origin, or nothing answering at all. Human-facing installation and troubleshooting live in `docs/setup.md`.

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
hactl auto ls --pattern 'ess_*'           # glob/substring on object id, entity_id, config id or alias
hactl auto ls --label victron             # filter by label name (substring)
hactl auto ls --restored                  # only "ghost" automations (restored from registry, no live config)
hactl auto show climate_schedule          # config summary + last 5 traces with stable IDs
hactl auto cat climate_schedule           # the automation's remote YAML, verbatim (no header)
hactl trace show trc:a7                   # condensed trace (trigger → condition → action, pass/fail)
hactl trace show climate_schedule         # the automation's most recent run, by any of its identifiers
hactl trace show trc:a7 --full            # raw trace JSON
```

The run-count column is named for the window it counted: `runs_24h` by default,
`runs_1h` under `--since 1h`. It counts **runs**, not triggers: a trigger blocked by its conditions never
entered the actions and does not count — it appears in `auto show`'s trace table as
`result: failed_conditions`. An errored run still counts, with the failure reported in
`errors`. The two reconcile — `runs_24h` equals the trace rows *not* marked
`failed_conditions` — whenever HA still holds the window's history (it keeps the last 5
traces per automation; `runs_24h` comes from the uncapped logbook). `script ls`'s
`runs_24h` is a plain invocation count: script conditions are steps inside an
already-running sequence, so nothing gates a start from being counted.

**Automation identifiers:** every command that takes an automation — `auto
show|cat|diff|apply|delete|rollback`, `trace show` — accepts any of its
interchangeable names: the config `id:`, the alias, the entity_id, or the
entity_id's object id (the `id` column of `auto ls`). Copy an identifier out of
any listing and it works in the next command. The config `id:` is the canonical
one hactl prints (`auto show --json`'s `config_id`, `auto create`'s result);
`--pattern` matches all forms.

`auto show` summarizes; `auto cat` prints the stored config itself, so it is what
you feed back into `auto diff -f` / `auto apply -f`. It needs the companion.
Output is YAML by design — `--json` does not change it; the full list of
commands `--json` does not reach is under Output conventions.

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
hactl ent ls --pattern 'sensor.wp_*'      # glob/substring on entity_id (automations: also config id + alias)
hactl ent ls --domain sensor              # filter by domain
hactl ent ls --area living                # filter by area name (substring)
hactl ent ls --label energy               # filter by label name (substring)
hactl ent ls --restored                   # only "ghost" entities (restored from registry, no live entity)
hactl ent show sensor.wp_vl               # state + key attributes + area + labels + ownership (platform, unique_id, config_entry_id) + attribute count
hactl ent show sensor.wp_vl --full        # + all attributes
hactl ent hist sensor.wp_vl --since 7d    # ~50 resampled datapoints (time/value)
hactl ent hist sensor.wp_vl --resample 5m # override bucket size
hactl ent hist sensor.wp_vl --attr brightness  # track attribute instead of state
hactl ent anomalies sensor.wp_vl          # gaps (>1h), stuck (>2h/24h), spikes (z>3)
hactl ent related sensor.wp_vl            # related automations, device siblings, every entity in the same area
hactl ent who light.kitchen --since 7d    # who/what changed it: per-event + counts summary
hactl ent rename sensor.old sensor.new            # dry-run: registry rename + reference count
hactl ent rename sensor.old sensor.new --confirm  # rename + rewrite every reference (needs companion)
```

`ent rename` is the one-command form of "rename the entity, then `ref replace`
its references": the registry entry is renamed first (the authoritative
object), then every literal reference in config files and dashboards is
rewritten in the same pass. Only registry entities can be renamed. If the
rewrite half fails after the registry half succeeded, the error names the
completed half and the idempotent remediation (`ref replace <old> <new>
--confirm`). Requires hactl-companion; `--allow-partial` mirrors `ref
replace`. Under `--json`, the dry run is one plan object; a confirmed run
returns the reference report (registry success is the exit code).

The new id is judged before the plan is printed, by HA's rule rather than by a
dot check: `<domain>.<object_id>`, lowercase letters, digits and underscores,
no leading, trailing or doubled underscore — and the **same domain** as the old
id, because HA refuses a cross-domain rename (`switch.x` for an
`input_boolean`). Those are the two answers HA gives at confirm time
("Invalid entity ID", "New entity ID should be same domain"), so the preview
gives them first.

`ent hist` auto-resamples to ~50 points. For binary/non-numeric entities the timeline shows time/state/duration, one row per **state run** — consecutive records reporting the same state are one row lasting until the state actually changes, and an `unavailable` gap splits the run rather than being spanned. Anomaly detection runs client-side on cached history.

`ent show` closes with `attributes: N total; use --full to see all`. `N` is the
entity's **whole** attribute count, not the number withheld — the four it always
shows (`friendly_name`, `unit_of_measurement`, `device_class`, `restored`) are
included in it.

**An empty answer means the entity was quiet, never that it was mistyped.**
`ent hist`, `ent who` and `ent anomalies` fail — like `ent show` — when the
entity has no live state *and* nothing recorded in the window, because those two
cases are indistinguishable from a typo. An entity that was deleted but still
has recorder history reports that history as before. `--resample` must be a
positive duration; `0m` and negative values are refused rather than ignored. A
`--resample` bucket is the width it says: buckets are that wide, there are as
many as it takes to cover the recorded span, and a bucket holding no samples
produces no row — so a gap in the history stays a gap rather than being closed
up.

`ent show`'s `changed_by:` line and `ent who` answer the same question — who or what changed this entity — through **one shared resolution**: the logbook's answer when it has one, the state's own `context` otherwise, and every answer names its source (`source: logbook` | `source: state context`). The sources are not equal: the logbook knows the proximate cause (`Automation: <alias>`, `Script: <id>`, `Device: <name>`, `User <name>`), while the state context carries only the propagated user id — it can name the human who started a causal chain but never the automation that acted.

**Logbook-excluded entities.** HA's logbook permanently excludes "continuous" sensors — any `unit_of_measurement`, `state_class`, or numeric `device_class` — and the `counter`, `image`, and `proximity` domains: for those it answers `[]` forever even though the entity changes. Both commands say so instead of silently disagreeing: `ent show` renders `changed_by: <actor> (source: state context; excluded from logbook — <reason>)`, and `ent who` states the exclusion and gives the same state-context fallback. An excluded entity is **not** a quiet one: `no changes for <id> in the last 24h (source: logbook)` is a verified zero for an entity the logbook covers.

`ent who` aggregates per-event attribution over `--since` with a counts summary (`Jan: 12, Automation 'Sunset lights': 5, ...`). `--json` returns `{events, summary, window, source, logbook_excluded}` — the flag distinguishes an excluded entity from a quiet one by fields, not emptiness, and an excluded entity carries a top-level `changed_by` fallback; `ent show --json` mirrors this in `changed_by`, `changed_by_source`, `logbook_excluded`. Resolving user UUIDs to names needs an admin token; without one raw UUIDs appear while automation/script/device attribution keeps working. The `changes` command's `who` column is the same per-event label as a raw logbook feed, so excluded entities never appear there either.

`ent who` answers who *changed* an entity; to find what is *wired to* change it, run `ref scan <entity_id>` — every automation, script, and dashboard referencing it — then inspect each hit with `auto show` (enabled? recent traces?). Note that listings print entity_ids without display names, and a display name can share no token with its id (`light.ap_gast_v2_led` ↔ "AP6 Flur LED"): `ent show` prints the name, and the name search under "Filtering & discovery" resolves name → entity_id.

### Devices

```bash
hactl device ls                           # device_id, name, area, labels, entity count
hactl device ls --pattern '*heat*'        # glob/substring on device ID or name
hactl device ls --area basement           # filter by area name or ID
hactl device ls --label heat_pump         # filter by label name or ID
hactl device show summt_heizung           # device profile + registered entities
hactl device set-area summt_heizung basement            # dry-run (device by ID or name)
hactl device set-area summt_heizung basement --confirm  # place the device in the area
hactl device set-label summt_heizung heat_pump --confirm # add label(s) to the device
```

Placing the **device** in a room is the normal HA pattern: a device's area is
inherited by every one of its entities that has no own `area_id` (H-8), so one
`device set-area` moves all of them at once — `ent set-area` is the per-entity
exception for overrides. `set-label` merges into the device's existing labels.
Both are dry-run by default; the preview resolves the device (ID or name) and
the area/label, so a typo is an error, not a plan.

LLM workflow for area assignment: discover the device with `device ls`, inspect its entities with `device show`, preview `device set-area <device> <area>`, then repeat the exact command with `--confirm` only after the user confirms the device and target area. Use `ent set-area` only when a single entity must differ from its device.

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

**Names.** `label`/`area`/`floor create` refuse a blank name (empty or whitespace only) before contacting HA, in dry-run and with `--confirm` alike. Home Assistant would accept it: an empty name mints a record with an empty `area_id`/`floor_id`/`label_id`, and every command of that family then fails — `ls`, `create` and `delete` together, because each has to list the registry first — until the record is removed outside hactl. A name that merely carries surrounding spaces (`" Kitchen "`) is a real name and is sent verbatim.

**`floor --level`** is optional and `0` is a value, not an absence: `--level 0` is stored as level 0 (HA's ground floor), a negative level is a basement, and omitting the flag leaves the floor with no level at all.

### Write path (automations)

```bash
hactl auto diff climate_schedule -f new.yaml          # diff local vs remote
hactl auto apply climate_schedule -f new.yaml          # dry-run (default, no write)
hactl auto apply climate_schedule -f new.yaml --confirm  # write + reload
hactl auto rollback                                    # dry-run: preview the backup that would be restored
hactl auto rollback --confirm                          # undo last backup
hactl auto rollback climate_schedule --confirm         # undo specific automation

```

**Safety:** `apply` and `rollback` without `--confirm` are always a dry-run and write nothing (no backup files either). The candidate's trigger/condition/action blocks are validated against HA's real config schema (WS `validate_config`) in both dry-run and confirm mode — an invalid config aborts before anything is written. On `--confirm`, a backup of the current config is saved to `backups/` before the write.

**What the diff means, and what a write changes.** Both sides of the diff are the YAML text: the stored entry as `auto cat` prints it, and the file you pass. A confirmed apply writes **that file's bytes** into `automations.yaml` and leaves every other entry byte-identical — so key order and formatting are part of the diff, because they are part of what lands. Start from `hactl auto cat <id> > new.yaml` and edit; a hand-written file in a different style diffs as changed, and honestly so. `changed_lines` counts the `+`/`-` lines. If the entry cannot be spliced, the response says `reformatted: true` and the run warns that other entries' formatting may have moved.

Both commands need **hactl-companion**: they write through its single-entry route. HA's own automation endpoint re-serializes the entire `automations.yaml` on every write, so there is deliberately no fallback — without a companion these two commands refuse rather than reformat the file.

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
hactl helper ls --pattern guest                      # filter by helper id (glob/substring)
hactl helper ls --name "Guest Mode"                  # filter by display name (substring)
hactl helper show guest_mode                         # id + domain + source header, then the YAML definition
hactl helper show input_boolean.anwesenheit_flur     # storage helpers: address them by entity_id
hactl helper cat guest_mode                          # the same YAML with no header (pipe-friendly)
hactl helper create input_boolean -f toggle.yaml             # dry-run
hactl helper create input_boolean -f toggle.yaml --confirm   # create via companion + reload
hactl helper delete guest_mode                       # dry-run
hactl helper delete guest_mode --confirm             # delete via companion + reload
```

Supported domains: input_boolean, input_number, input_select, input_text, input_datetime, counter, timer, schedule. Requires hactl-companion.

`helper ls` shows a **source** column, and `show`/`cat` report it too:

- `yaml` — defined in a helper file the companion manages. Editable with
  `create` / `delete`.
- `storage` — created in the HA UI, which is how most helpers on a real
  instance exist. `show`, `cat` and `ls` read them (address one by its
  `entity_id`, e.g. `input_boolean.anwesenheit_flur`, or by its bare id);
  `helper delete` refuses them in dry run and with `--confirm` alike — there is
  no YAML definition to delete, so remove it in the UI. `cat` output for a
  storage helper carries a leading `# source: storage` comment; it is still
  valid YAML.

`helper create`'s dry run checks everything `--confirm` does, in the order that
answers cheapest first: the domain is one of the eight it writes (`script`,
`automation` and `template` are HA domains but not helper domains); the id is
one HA can turn into an entity; and `configuration.yaml` has a `<domain>:` key
which `!include`s a file. If the domain is written **inline** in
`configuration.yaml` instead, no create can append to it, and the preview says
so with the same message `--confirm` would — rather than planning a create that
cannot happen. The id check is stricter than the companion, deliberately: HA
validates a helper file as a whole, so one unusable key stops every helper in
that file from loading.

The `-f` file must be a **keyed mapping with exactly one top-level key** — the
helper id — not a bare definition:

```yaml
# toggle.yaml
guest_mode:
  name: Guest Mode
  icon: mdi:toggle-switch
```

A bare `name:`/`icon:` mapping is rejected by the dry run as well as by
`--confirm`: the file must be a mapping with exactly one top-level key, the
helper id, with `name:`/`icon:` nested under it.

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

`tpl eval` is also the discovery escape hatch — set questions in one call instead of a listing sweep:

```bash
hactl tpl eval "{{ states | selectattr('name','search','(?i)<term>') | map(attribute='entity_id') | list }}"
hactl tpl eval '{{ area_entities("living_room") | list }}'
hactl tpl eval '{{ label_entities("energy") | list }}'
```

`area_entities`, `device_entities`, `label_entities`, and `integration_entities` resolve membership; `states | selectattr(...)` filters on any attribute (display name, device_class, …) and is the only way to search entities by display name.

`svc call` is dry-run by default and prints the planned call; `--confirm` executes it (only after the user confirmed). `--return` prints the service response for services that support `return_response` (e.g. `weather.get_forecasts`, `calendar.get_events`). `-d @file.json` reads the payload from a file and avoids shell quoting.

The dry run judges the **payload**, not just the service name, against HA's own service registry — an undeclared field or a malformed `entity_id` ends the command, because HA answers both with 400. Two payload shapes are worth naming:

- **`-d '{"target":{"entity_id":…}}'` is refused.** The `target:` wrapper is automation/script YAML syntax, which HA flattens before calling the service; a service call takes `entity_id`/`device_id`/`area_id`/`label_id`/`floor_id` at the top level.
- **A targeted service with no target reaches nothing.** The preview says so (`targets: none — …`); HA selects no entity when none of the five selector fields is present. `"entity_id":"all"` is the opposite and the preview says that too.

What the preview does *not* refuse: an `entity_id` naming an entity that does not exist (HA accepts it and changes nothing), and any payload for a service whose registry entry documents no fields — `script.<name>` takes arbitrary keys as script variables.

A confirmed call reports what HA attributed to it: `changed: light.kitchen`, or `changed: none reported`. Read that as HA's own answer, not as proof of a miss — a service that acts asynchronously (`automation.trigger`) reports no change either.

### Energy

```bash
hactl energy show                         # what the Energy dashboard measures: sources + tracked devices
hactl energy show --json                  # same, as {configured, sources[], device_consumption[]}
```

One row per statistic feeding the dashboard (`type` grid/solar/battery/gas/water,
`direction`, `statistic`), plus the individually tracked devices — the joins to
run before touching anything (`ent show <statistic>` answers who owns it).
`direction` is what that statistic means for that source type, in HA's own
terms: grid `consumption`/`return`, solar `production`, battery
`discharge`/`charge`, gas and water `consumption`. Only a generative source
reads `production`; a source type this build does not know reads `unknown`
rather than a guess. An
instance whose Energy dashboard was never set up says exactly that (`--json`:
`{"configured": false}`) — HA answers an error for missing preferences, so an
empty dashboard and a missing one are never conflated. Read-only; there is no
write half and no raw WS passthrough (D-12).

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
as the inlined list); `--raw` returns the file's own bytes. `block` matches
`id:` or `alias:` on the direct items of a top-level list (automations.yaml),
or a top-level mapping key (scripts.yaml), and prints that block verbatim, so
its output may carry a trailing comment line that sits before the next key.
`template.yaml` blocks carry neither — read those with `tpl cat <unique_id>`;
`block` says so when the id you gave is a template unique_id, so a wrong guess
costs one command rather than a search. All three are YAML-only — `--json` is
accepted but does not change the output. A missing file or block is an error
with a non-zero exit, not an empty result. `file` without `--raw` re-renders the
document, and HA's own tags survive that rendering as tags (`!input`, `!secret`,
`!env_var` are printed, never resolved and never quoted into strings).

`entries --json` carries values, not the table's renderings: `disabled_by` is
`""` when the entry is enabled (the same shape `config show --json` gives) and
`options` is a boolean.

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

Some steps are **menus** (`"type": "menu"`): a choice of next steps instead of fields. `flow-inspect` lists the options; advance with `hactl config flow-step <id> --data '{"next_step_id": "<option>"}'` (plus `--options` in an options flow). **Select fields** list their submittable values below the field table the same way.

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
hactl dash show                                    # default dashboard: views summary, or an honest no-stored-config report
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
hactl dash grep sensor.wp_vl --allow-partial       # answer from the dashboards that could be read
hactl dash replace sensor.old sensor.new my-dash   # dry-run: rename within one dashboard
hactl dash replace sensor.old sensor.new my-dash --confirm  # apply
```

**LLM round-trip workflow:** `dash show --raw` → modify JSON → `dash save --file`. Config replacement is always full — HA has no partial update API. `--view` scopes inspection output only; do not feed a single-view object to `dash save`.

**`dash show` reports what Home Assistant holds, for any dashboard.** With a
stored config you get the views summary; the same holds when configuration.yaml
pins the default to YAML mode (the config is read from `ui-lovelace.yaml`).
Three answers are not a views summary, and each says which it is:

- **No stored config.** HA holds nothing for this dashboard. For the **default**
  that means HA builds it at view time — the state of every fresh instance —
  and `dash show` says so and points to `dash ls`; it never fabricates a
  rendering of what HA would generate. For a **named** dashboard it is the state
  between `dash create` and its first `dash save`. Under `--json` the answer is
  an object carrying `"state": "auto-generated"` or `"state":
  "no-stored-config"` (a stored answer is the config document itself, which
  carries `views` and no `state` key — check for `state` to tell them apart);
  `--raw`/`--yaml` refuse, because there is no document to round-trip. Store one
  with `dash save <url_path> --confirm`.
- **Strategy-generated.** The stored config carries a `strategy` and no views —
  HA's own `map` dashboard is one — so `dash show` names the strategy and says
  the frontend builds the views at view time. `--raw` shows the stored document.
- **A `--view` that does not exist** is an error at exit 1, whatever the
  dashboard's shape, including a dashboard with no views at all.

A url_path that names no dashboard is an error, never one of the answers above.

**One output format per invocation.** `--raw`, `--yaml` and `--json` each name
a format and `dash show` refuses more than one rather than silently picking:
`--raw` is the stored document verbatim, `--json` the same document indented,
`--yaml` the same document as YAML.

**`grep` and `replace` work on string values, not on entity fields.** Both walk
each dashboard's JSON and match any string **equal to** the argument, wherever it
sits — a card's `entity`, but equally a markdown card whose `content` is exactly
that string, or a view `title`. `dash grep P` finds a view titled `P`. Matching
is whole-value, so a mention *inside* a longer sentence is not a hit, and map
keys are never matched or rewritten. Output is `dashboard` + `path`
(`views[0].cards[1].content`); a miss says "not referenced **as a whole
value**" and routes term discovery to `ent ls --pattern`, exit 0 — it is a
verified negative only for the exact value asked about, and only when every
dashboard could be read: a dashboard whose config does not fetch puts a `partial
sweep: …` line above the answer, makes the miss read "in the dashboards that
could be read", and makes `--json` refuse until you pass `--allow-partial` (see
References, below).

`dash replace` takes one dashboard (omit `url_path` for the default dashboard),
is dry-run until `--confirm`, and rewrites every matching value at once. A
dashboard with no stored config contains no occurrences of anything, so it
reports zero and writes nothing; a url_path that names no dashboard is refused.
Writes to a YAML-mode dashboard are refused up front — preview and `--confirm`
alike — because HA's save API answers "Not supported" for them; the same applies
to `dash save` and `dash delete` (a YAML dashboard is removed from
configuration.yaml, not over the API). Whether the **default** is YAML-mode is
read from its own `mode` in `dash ls`, not from whether it is listed: recent
Home Assistant migrates a stored default into the dashboard list under url_path
`lovelace`, in `storage` mode, and that one is writable. To rename across config files *and*
dashboards in a single pass, use `ref replace`.

> **Skill:** For LLM agents designing dashboards, load the `lovelace-design` skill (`.github/skills/lovelace-design/SKILL.md`). It covers card types, grid sizing, layout patterns, and common pitfalls.

### References (find and rename entity_ids)

```bash
hactl ref scan sensor.wp_vl                    # every reference, config files + dashboards
hactl ref scan sensor.wp_vl --allow-partial    # answer from what could be read when a source is unavailable
hactl ref validate                             # dangling references: pointers to entities that are gone
hactl ref validate --exit-code                 # exit 1 if any dangling reference is found (CI gating)
hactl ref validate --exit-code --allow-partial  # gate on what could be read when a half is unavailable
hactl ref replace sensor.old sensor.new        # dry-run: rename everywhere
hactl ref replace sensor.old sensor.new --confirm   # apply
hactl ref replace sensor.old sensor.new --confirm --allow-partial  # apply even when some dashboards can't be scanned/written
```

Requires hactl-companion — it is what reads the config directory. `ref` is the
whole-instance version of `dash grep`/`dash replace`: YAML config files
(following `!include`) **and** every dashboard in one pass. `scan` finds
references to an exact id, not free-text terms (term/name discovery is the
name search under "Filtering & discovery") — but its two halves do not match
identically: dashboards match whole string values (exactly like `dash grep`),
config files match the id as a word-bounded token, so `scan` **does** find an
id inside a config-file template string (`{{ states('sensor.wp_vl') }}`) while
never finding one inside a dashboard's. Reports `source` (`config` |
`dashboard`), `location` (file name or dashboard), and `path`
(`[1].trigger[0].entity_id`).

**The target must be a whole token**: it has to start and end on a letter, digit
or underscore, because the config half matches it with a word boundary at each
end. `scan`/`replace` refuse anything else before contacting Home Assistant —
`.` would otherwise match the dot inside every entity_id and every service name
(2747 hits on a real instance, and `ref replace . X` a plan to rewrite all of
them). Display names are fine (`Wozi TV`, `Küche`); pasted syntax is not
(`'sensor.x'`, `.turn_on`, `{{`). For term discovery use the name search under
"Filtering & discovery".

To rename a **live** entity and its references in one step, use `ent rename`
(registry rename + this replace pass); `ref replace` alone is the tool when
only the references must move — e.g. onto an already-existing entity.

`validate` is the one to reach for after deleting or renaming an entity: it
sweeps every entity reference and reports the ones that no longer resolve. The
live set is the union of the entity registry and current states, so state-only
entities (`sun.sun`, `zone.home`, `weather.*`, template sensors) are not falsely
flagged. Deliberately conservative — only values in known entity-holding
positions are checked (`entity_id`/`entity` in config;
`entity`/`entities`/`badges`/`camera_image` in dashboards), so `light.turn_on`
is never mistaken for an entity. Two blind spots are the accepted price of zero
false positives: **entities inside templates** (`{{ states('sensor.x') }}`) and
entities under non-standard custom-card keys. `validate` reports; it never
fixes — rename with `ref replace`.

**A partial sweep never passes as a clean tree — and never as a short answer.**
`validate` reads four sources — entity registry, live states, config files,
dashboards — and states its scope when one is unreadable: in plain text it still
answers, with one `partial sweep: …` line per unread source; under `--exit-code`
or `--json` it **refuses** with a non-zero exit, because those modes feed a CI
gate or a parser that cannot see a stderr warning, and certifying a half-read
tree would make the gate vacuous. `--allow-partial` proceeds over whatever could
be read, in any mode, scope stated. One source is stricter: missing **live
states** refuse in every mode (the registry alone holds no state-only entities —
most reports would be false positives); an unread config file or dashboard risks
the opposite, false negatives — references that were never checked. A dashboard
Home Assistant holds **no config for is not a partial sweep** — the
auto-generated default, and equally a dashboard created but not yet saved: HA
holds nothing, so zero references there is the complete truth. `--exit-code`
stays green on a fresh instance, and one unsaved dashboard does not make
`validate` refuse to certify the tree.

**`scan` and `dash grep` take the same rule, one step softer.** They answer
"where is X?", so they still report what they found — with the same `partial
sweep: …` lines above the table, in the body where you can see them. Under
`--json` they **refuse** instead: that document is a bare array of rows with
nowhere to say the search was incomplete, and a short list parses exactly like a
complete one. Both take `--allow-partial` to get the partial array. An empty
result never claims more than it tested either: after a partial sweep the miss
reads `no reference to X in what could be scanned`, not `not referenced`.

The **config half can be short without failing**: the companion reports files
its walk could not read (a renamed `!include` target, a file it may not open),
and every `ref` command treats that as partial — `scan` says so, `validate`
refuses to certify, `replace` refuses in dry run and, if a confirmed run hits
it, names the files that may still hold the old id instead of reporting a
completed rename.

**A missing companion is not a degraded source, and `--allow-partial` does not
cover it** — the companion is the transport for the config half, so every `ref`
command aborts in every mode when its discovery fails. Without the add-on (HA
Container, HA Core) `ref` does not run at all; `dash grep` still works over the
WebSocket API alone, but it answers "where is this value?", not "is the tree
clean?" — like `ref scan`, it prints the hits it found with exit 0 and states any
unread dashboard above them.

`replace` is dry-run until `--confirm` and aborts before writing anything if
the companion cannot be reached — a rename that silently skips config files is
the exact failure this command exists to prevent. The **default dashboard is
included** whenever it has a stored config. Two situations refuse with a
non-zero exit, before any write, unless `--allow-partial` is given: a dashboard
whose config could not be read (the scan would be silently partial), and
references in **YAML-mode dashboards**, which HA's API cannot write — under
`--allow-partial` those are reported `skipped: yaml-mode` and everything else
is renamed. A confirmed run is idempotent, so re-running after a partial
failure is safe.

### Logs & custom components

```bash
hactl log --errors                        # ERROR-level entries only
hactl log --warnings                      # WARNING-level entries only (operational signals)
hactl log --errors --warnings --unique    # both levels, deduplicated, sorted by count
hactl log --component zha                 # filter by component name (substring)
hactl log --errors --since 2h             # only entries from the last 2 hours
hactl log --json                          # whole messages, whole logger names
hactl log --full                          # whole messages in the text table too
hactl log show log:f2                     # full detail: timestamp, component, message

hactl cc ls                               # installed custom components + versions
hactl cc show hacs                        # CC details + entity count
hactl cc logs hacs --unique               # CC-specific errors, deduplicated
```

The message column is shortened **for the text table only** — `--json` carries
the message Home Assistant sent, tracebacks and all, and `--full` shows it in
the table too. A cell always renders as one line; where a message had more, the
table marks the fold with `⏎` and `log show <id>` prints the whole thing.

The `component` column works the same way, in the other direction: `--component`
matches the **full dotted logger name**, the table shows its last segment, and
`--json` carries the full name — so `log --component template --json` reports
`homeassistant.components.template.config`, the value the match was made
against, not `config`. All four views (`log`, `log --unique`, `cc logs`,
`cc logs --unique`) carry `id`, so any row can be drilled into with `log show`.

`cc logs <name>` takes the name of an **installed custom component** — the ones
`cc ls` prints — and refuses anything else, like `cc show`. "no log entries for
X" therefore means X is installed and quiet, never that X was a typo.

`cc show` attributes entities through the entity registry's `platform` field,
which names the integration that created them — never through the entity_id,
whose first segment is the *entity* domain (`powercalc` publishes `sensor.*`).
`entities: N` counts the entities HA holds a state for. When the registry
attributes more than that to the component, the line says so and names the
usual reason — `entities: 159 (registry: 402, of which 243 disabled)` — because
a disabled entity is one the integration owns and somebody turned off, not one
that does not exist. `--json` carries `entity_count`, `disabled_count` and
`registry_count` with the ids behind each; `--full` lists both sets.

Log source: WS `system_log/list` (structured, preferred) with automatic fallback to REST `/api/error_log`.

HA's system log is a fixed-size in-memory buffer, not a time-indexed store, so
`log` and `cc logs` show **the whole buffer by default** — the global `--since`
default of 24h is deliberately not applied, because the buffer routinely holds
older entries and hiding them is the opposite of what these commands are for.
Passing `--since` explicitly narrows the buffer to that window (entries whose
timestamp cannot be parsed are kept rather than silently dropped).

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
same Ingress lifeline as the other companion commands. `--since` sets the time
window; `--top` caps the printed line count only — with `--json` you always get
the whole window. Requires hactl-companion.

### Cache & version

```bash
hactl cache status                        # age + size + item counts per category
hactl cache refresh traces                # pull fresh trace data
hactl cache refresh                       # refresh everything
hactl cache clear                         # wipe all local cache, stable IDs included

hactl version                             # version, commit, build date (same first line as --version)
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

> **Stop at the first miss.** If a pattern or entity ID returns empty or 404, report it and stop — do not chain fallback patterns or broaden the search unless the user asks. An empty listing says what emptied it (`no helpers match --pattern "guest" (220 on this instance)`); the bare `no helpers` means the instance holds none. Report the one you got.

Five commands take `--pattern` (glob or substring, case-insensitive), and it matches **identifiers, not display names**:

```bash
hactl auto ls --pattern victron           # object id, entity_id, config id, alias
hactl script ls --pattern kino            # script id
hactl ent ls --pattern 'sensor.wp_*'      # entity_id (automations: also config id + alias)
hactl helper ls --pattern guest           # helper id
hactl device ls --pattern wozi            # exception: id OR name ("Wozi Tv")
```

`*`/`?` → glob, otherwise substring, never case-sensitive; a glob anchors at the
printed id and at the part after the domain (`'anwesen*'` matches
`input_boolean.anwesenheit_flur`). A display name can share no
token with its entity_id (`light.ap_gast_v2_led` ↔ "AP6 Flur LED") and listings
print no name column, so quoted names need the server-side name search:

```bash
hactl tpl eval "{{ states | selectattr('name','search','(?i)<term>') | map(attribute='entity_id') | list }}"
hactl device ls --name <term>             # display-name filter (devices, helpers)
```

These are the only search flags — `ent ls` has no `--name`, nothing has
`--search`; unknown flags fail without a suggestion. `ent ls` stacks
`--domain`, `--area`, `--label`; `auto ls` and `script ls` take `--label` and
`--failing` (recent trace errors):

```bash
hactl ent ls --domain binary_sensor --area garage
hactl auto ls --label victron --failing
```

With an entity in hand, `hactl ent related <id>` spiders its automations,
device siblings, and area neighbors. `--restored` filters `ent ls`/`auto ls`
down to ghost entities — see the "Ghost entities" how-to.

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

- **Token cap & estimate:** output is truncated at `--tokensmax` tokens (default 500, `0` = off) with a hint naming filters that shrink it; prefer filters to raising the cap. `--full` removes it too, unless you pass `--tokensmax` yourself. `--tokens` prints a `[~N tok]` estimate (stderr under `--json`). **Documents are never capped** (a cut leaves them unparseable): `--json`, `dash show --raw|--yaml|--view`, `<family> cat`, `config file|block`, `completion`, `--help`.
- **Tables:** one header line, one row per item; `…+N more` for overflow, capped by `--top`.
- **Stable IDs:** `trc:a7` (`auto`/`script show`), `log:f2` (`log` incl. `--unique`, `cc logs`) — kept in `cache/ids.json` until `cache clear`; `ent anomalies` mints none.
- **Timestamps:** short form in your zone (`09:42` today, `04-16 09:42` otherwise); `--full` does **not** make them ISO. **`--json` always gives full ISO8601 with your offset**, table listings included. Boolean columns are JSON booleans (`"admin": true`); numeric cells stay strings (`"runs_24h": "0"`) — parse them, never test them for truthiness.
- **No decoration:** no emojis, no color.
- **JSON mode:** `--json` extracts fields; filter first on large datasets. **Commands `--json` does not reach:** `auto|script|helper|tpl cat`, `auto|script diff`, `config file|block`, `rtfm` — each prints one document verbatim; every other command honours it. Previews return `{"dry_run":true,"action","details","hint"}`, a confirmed write `{"dry_run":false,"ok":true,"action","details"}` (+`"warnings"`) — read `dry_run`, not your flags.
- **Bad input is refused, not absorbed** (exit 1, stderr, empty stdout): a blank identifier (an empty string is never a wildcard), an argument a command does not take (`ent ls sensor` → `--domain sensor`), an unknown subcommand in any family, a flag the command does not take (the error names the ones that do), a value outside a flag's range (`--top -1`, `--timeout 0s`), or two inputs naming one thing (`dash show --raw --yaml`). Nothing mistyped ever exits 0 with help.
- **Dry runs resolve their target** and parse the `-f` file before printing a plan: a preview fails exactly where `--confirm` would, so a misspelled id is an error, not a plan. A family's **first `--confirm`** is refused non-interactively (how-to on stderr, exit 1) — dry-run first, then repeat.
- **`--stats`:** response size + token estimate on stderr, after any command including a failing one.

---

## Global flags

| Flag | Default | Effect |
|------|---------|--------|
| `--dir` | auto | Instance directory (overrides `HACTL_DIR` and auto-discovery) |
| `--since` | `24h` | Time range (`1h`, `7d`, …) — **only on** `log`, `cc logs`, `companion logs`, `changes`, `auto ls`, `script ls`, `ent hist`, `ent anomalies`, `ent who` |
| `--top` | `10` | Max rows in tables (CLI only — not a tool kwarg; use filters instead). `0` = every row. `--json` returns the full set regardless |
| `--full` | off | Raw/verbose: all attributes (`ent show`), raw JSON (`trace show`). **Lifts both caps** — `--top` rows and `--tokensmax` tokens — so it can return a lot; an explicit `--tokensmax` still wins |
| `--json` | off | JSON output |
| `--color` | off | No-op — accepted, changes nothing |
| `--stats` | off | Print response size + token estimate to stderr |
| `--tokens` | off | Print compact token estimate |
| `--tokensmax` | `500` | Cap output at N tokens; `0` = no cap. Not applied to documents (see above) |
| `--timeout` | `30s` | Per-request timeout for HA/companion API calls; must be positive |

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

A `--dir` or `$HACTL_DIR` that holds no `.env` is answered with the path you
gave and where it came from (`no .env at <path>/.env (from --dir)`, exit 2);
only discovery — no flag, no variable — falls back to the generic four-step
"no hactl instance configured".

---

## Manual delivery

Parts of this manual may already have reached you automatically: when both stdout and stderr are captured (agent/shell-tool usage), hactl delivers the manual progressively on stderr — the core (routing table, conventions, flags) with the first command of a session, each family's how-to with the first command of that family, ending with a `=== RESULT of hactl … ===` marker before the real output. Sessions are per instance, keyed by `HACTL_SESSION` (default: a shared key with a 30-minute idle timeout).

- `HACTL_MANUAL_MODE`: `progressive` (default) | `full` (whole manual once) | `off`
- `off` also disables the first-`--confirm` guard (the refusal of a family's first write before its how-to arrived) — it is meant for scripts and pipelines that pre-load this manual another way, not a token-saving knob for interactive agents
- `hactl rtfm --core` / `--family <name>` / `--families` fetch subsets on demand
- Humans at a terminal never see it; `rtfm`, `mcp`, `setup`, `version`, `help`, `completion` never trigger it
- **Delivery is decided by who is listening, never by the shape of the answer.** `--json` is a promise about stdout and the manual has never been on stdout, so `health --json` and `device ls --json` deliver on stderr exactly as they do without the flag — an agent that reads only structured output is the caller this manual is written for, and it used to be the one caller that never received it

---

## MCP server

`hactl mcp` serves this CLI over the Model Context Protocol on stdio. MCP clients see a single `hactl` tool that takes a command line (without the binary name), e.g. `{"command": "ent ls --domain light"}`. All commands and flags in this manual work unchanged; this manual is also available as the MCP resource `hactl://manual`. Over MCP the full manual arrives once with the first tool result — the progressive stderr delivery above applies to plain CLI usage only.

```bash
claude mcp add hactl -- hactl mcp --dir ~/.hactl/default
```

- Read-only by default: mutating commands (`svc call`, `auto apply`, `script apply`, create/delete, `script run`, …) are rejected with an error. Start the server with `hactl mcp --allow-writes` to permit them; the dry-run + `--confirm` write path still applies.
- One instance per server process. A `--dir` given at server start pins every call to that instance; a per-call `--dir` overrides it.
- `setup`, `completion`, and `mcp` itself are never available over MCP; unclassified commands fail closed.
- A malformed message costs that message, never the session. A line that is not a valid JSON-RPC request is answered with a JSON-RPC error when its `id` can be read, and dropped (with a note on stderr) when it cannot — a bad line from a client does not end the server. Lines above 4 MiB are dropped. The session ends when the client closes the stream, and then `hactl mcp` exits 0.
