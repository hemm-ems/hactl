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
| "Where is <word> used?" | `grep <term>` | substring, config + dashboards |
| "Which entities belong to <concept>?" | `ent ls --pattern '*<term>*'` (ids), `device ls --name <term>`, or the name search under "Filtering & discovery" | `--pattern` matches ids, never display names |
| "Disable / turn on / trigger X" | verify (`auto show` / `ent show`), then `svc call` dry-run | `--confirm` only after the user confirms the plan |
| "Build / change a dashboard" | `ent ls --pattern <topic>`, then `dash create` dry-run | Same confirmation rule |
| "List labels / areas / helpers / scripts" | exactly: `label ls` · `area ls` · `helper ls` · `script ls` | One call, answer. `helper` is not an entity domain — never `ent ls --domain helper` |
| "Am I allowed to …?" | `policy check <kind>:<id> <verb>` | exit 0 allowed, 1 denied; `policy show` lists the rules |

Full command set (family → subcommands):

- `health` · `issues` · `changes` · `log [show <id>]` · `cc ls|show|logs` · `trace show <id>`
- `ent ls|show|hist|anomalies|related|who|set-label|set-area`
- `auto ls|show|cat|diff|apply|create|delete|rollback` · `script ls|show|cat|run|diff|apply|create|delete`
- `helper ls|show|cat|create|delete` · `tpl eval|cat|create|delete` · `svc call`
- `dash ls|show|save|create|delete|resources|grep|replace`
- `device ls|show` · `label|area|floor ls|create|delete`
- `config entries|show|options|delete|files|file|block|flow-start|flow-step|flow-inspect`
- `grep` · `ref scan|replace|validate` · `cache status|refresh|clear` · `companion status|logs|wireguard`
- `policy show|check|lint|review|activate` · `agent add|ls`

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
hactl ent set-label sensor.solar_power --remove solar    # take it back off this one entity
hactl auto ls --label solar
```
Positional labels are always ADDED — `set-label` never replaces an entity's or device's existing labels, so repeated calls accumulate. `--remove <label>` (repeatable) is the other half: it takes one or more labels back off the target named, and may be combined with positional labels to add and remove in the same write, but the same label cannot be named on both sides. This is deliberately narrower than `label delete <id> --confirm`, which removes a label from EVERY entity, device and area that carries it — `--remove` touches only the one target the command names.

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
one hactl prints (`auto ls --json`'s and `auto show --json`'s `config_id`, `auto create`'s result);
`--pattern` matches all forms. `auto ls --json` carries `config_id` on every row — `""` for an
automation without one, a restored ghost included — while the text table keeps its columns; it is
the id a policy's `automation:` rules name (see Policy).

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
hactl ent hist sensor.wp_vl --since 90d --stats                 # long-term statistics (kept forever): mean/min/max per period
hactl ent hist sensor.meter --since 365d --stats --period month  # sum statistics: consumption (change) + meter reading per period
hactl ent hist tibber:energy_consumption_x --since 30d --stats   # external statistic ids (as printed by `energy show`)
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

`ent hist` reads the recorder's **raw state history**, which Home Assistant
purges after `purge_keep_days` (default 10 days). When the history does not
reach back to the start of `--since` and long-term statistics do, the output
ends with a `note:` naming where the history starts, whether the statistics
cover the whole window or where inside it they start, and the `--stats` command
to run (under `--json` the note goes to
stderr; the JSON document is unchanged). No note means the history covers the
window, or no statistics reach further.

`--stats` reads Home Assistant's **long-term statistics** instead: hourly
aggregates kept forever, for entities with a `state_class` (and for external
statistics such as `tibber:…`, which are not entities). Without `--period` the
finest of `hour`/`day`/`week`/`month` giving at most ~50 rows over `--since` is
used; `--period 5minute` (short-term statistics, kept like the raw history) and
`--period year` must be asked for. A **mean** statistic (temperature, power)
renders `mean`/`min`/`max`; a **sum** statistic (energy/gas/water meters)
renders `change` — the consumption within that period — and `state`, the meter
reading at the period's end. The header names the period and unit. Periods
are Home Assistant's calendar buckets (days, Monday weeks, months in HA's time
zone), and `--since` cuts the first one: a `week` row starting before the
window aggregates only the part of that week inside it, so its `change` is not
a whole week's consumption. Under
`--json` each row carries `start`, `end` and every field Home Assistant
returned (`null` where it has none). `--stats` cannot be combined with `--attr`
or `--resample`; `--period` requires `--stats`. Ids are matched lowercase, as
Home Assistant writes them. A missing statistic is an error that says which
case it is — no such entity; an entity without a `state_class` (HA compiles
statistics only for those — run `ent hist` without `--stats`); or an entity
WITH one that still has none (the recorder holds no rows for it, or it is
under an hour old) — and names statistics under a neighbouring id
(`sensor.x` ↔ `sensor.x_2`), where a renamed entity's old rows live. An empty
window on a statistic that exists is an empty list followed by a `note:` with
the newest row's time and a `--since` that reaches it (stderr under `--json`).
Statistics outlive their entity: `ent hist` on a deleted entity, and an
`ent hist` whose history request fails (a long window over a busy sensor can
exceed `--timeout`), end with the `--stats` command that answers instead.
`ent show` says whether an entity has long-term statistics (`statistics: mean
°C`, `sum kWh`, or `none`).

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
hactl device set-area summt_heizung --clear --confirm   # remove the device's own area
hactl device set-label summt_heizung heat_pump --confirm # add label(s) to the device
hactl device set-label summt_heizung --remove heat_pump --confirm # take one back off
```

Placing the **device** in a room is the normal HA pattern: a device's area is
inherited by every one of its entities that has no own `area_id` (H-8), so one
`device set-area` moves all of them at once — `ent set-area` is the per-entity
exception for overrides. `set-label` merges into the device's existing labels.
Both are dry-run by default; the preview resolves the device (ID or name) and
the area/label, so a typo is an error, not a plan.

Pass exactly one of `<area>` or `--clear` to `set-area` — naming both, or
neither, is refused before anything is read. `--clear` removes the device's OWN
area (its entities without an own `area_id` then have none to inherit, unless a
new `set-area` gives it one); it does not touch any other device, which is what
distinguishes it from `area delete`. `set-label --remove <label>` (repeatable)
is the same shape for labels: it takes one label off THIS device alone, and may
combine with positional labels to add and remove in the same write, but the
same label cannot be named on both sides.

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
hactl ent set-label sensor.wp_vl --remove energy --confirm  # take one label back off
hactl ent set-area  sensor.wp_vl living_room            # dry-run
hactl ent set-area  sensor.wp_vl living_room --confirm  # set entity area
hactl ent set-area  sensor.wp_vl --clear --confirm      # remove the entity's own area
```

Labels and areas are applied via the entity registry (dry-run by default; `--confirm` to apply). Multiple labels can be passed to `set-label` at once.

`set-label` and `set-area` can also UNDO what they assign — H-27, "every assignment a command can make, it can also unmake." `set-label --remove <label>` (repeatable) takes one or more labels off the one entity named, leaving its other labels and every other entity's copy of the same label untouched; it may combine with positional labels to add and remove in the same call, but a label cannot be named on both sides — that is refused rather than resolved by picking a winner. `set-area --clear` removes the entity's own area the same way; pass exactly one of `<area>` or `--clear`. Before this, the only way to take a label off one entity or clear one entity's area was `label delete <id> --confirm` or `area delete <id> --confirm` — both instance-wide: they strip the value from every entity, device and area that carries it, not just the one a caller meant.

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

**Each domain has its own required keys, and the `state:` example below is only
correct for `sensor` and `binary_sensor`.** Home Assistant validates a template
entry *asynchronously*, during the reload that follows the write: an entry that
fails its domain schema is logged and skipped while the reload itself still
succeeds. So a write can be reported as done, be byte-clean in the file, and
still never become an entity — `hactl log --component config` is where the
reason appears. Give the domain what it requires:

| domain | required beyond `name`/`unique_id` |
|---|---|
| `sensor`, `binary_sensor` | `state` |
| `number` | `set_value` (an action block) |
| `select` | `options` — **a Jinja template string, never a YAML list** |
| `button` | `press` (an action block) |
| `image` | `url` |
| `update` | `installed_version`, `latest_version` |
| `event` | `event_type`, `event_types` |
| `lock` | `lock`, `unlock` (action blocks) |
| `light`, `fan` | `turn_on`, `turn_off` (action blocks) |
| `vacuum` | `start` (an action block) |
| `weather` | `condition`, `humidity`, `temperature` |
| `switch`, `cover`, `device_tracker`, `alarm_control_panel` | nothing — every key is optional |

`select` is the one the example above actively misleads on: `options` is the
only list-shaped field in the schema that is not a list. Write
`options: "{{ ['alpha','beta'] }}"`, not `options: [alpha, beta]`.

There is no Jinja syntax pre-check either — an unterminated `{{ ...` is written
and reported the same way a valid one is.

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

`helper delete --confirm` names the helper's domain in its request — the domain
the dry run resolved and printed — so a slug that exists in two helper domains
is deleted in exactly that domain, with or without a policy (D-123).

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

**`--data` must be a JSON object**, or left out (which sends `{}`), with or without a policy: `null`, a list, a string, a number or a boolean is refused in the dry run already — exit 1, nothing on stdout, nothing sent — as `invalid --data: want a JSON object ({…}), got null` (D-122). Malformed JSON stays `invalid --data JSON: <parser error>`.

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
`discharge`/`charge`, gas and water `consumption`, and the whole-home meter
(`home`, HA 2026.11 and later) `consumption`. Only a generative source
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
hactl config block template.yaml '[3]'            # the fourth block of a list-rooted file, by position
```

`files`/`file`/`block` read the config directory **through the companion** and
are the only `config` subcommands that need it. `file` without `--raw` returns
the merged document (an `automation: !include automations.yaml` line comes back
as the inlined list); `--raw` returns the file's own bytes. `block` matches
`id:` or `alias:` on the direct items of a top-level list (automations.yaml),
or a top-level mapping key (scripts.yaml), and prints that block verbatim, so
its output may carry a trailing comment line that sits before the next key.
A list-rooted file is also addressable **by position**: a zero-based index,
bracketed (`'[3]'` — quote it, the shell treats brackets as a glob) or bare
(`3`). The bracketed form is exactly the prefix `ref scan` prints in its `path`
column, so a hit pastes back: `ref scan` shows `template.yaml  [3].sensor[0].state`
→ `config block template.yaml '[3]'` prints that whole block. An `id:`/`alias:`
match wins over a bare number (HA's UI mints purely numeric automation ids, and
a printed id must keep resolving), so use the bracketed form when you mean the
position; it never collides. An index past the end is an error naming the valid
range. `template.yaml` blocks carry no `id:`/`alias:`, so position is how
`block` reads them; to read one template entity by its `unique_id`, use
`tpl cat <unique_id>` — `block` says so when the id you gave is a template
unique_id, so a wrong guess costs one command rather than a search. All three are YAML-only — `--json` is
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

To **read back** how an entry is currently configured (e.g. to confirm a value you just set via an options flow), use `config show <entry_id>` — do not infer configuration from behavior. It prints the setup summary (domain, state, source, options/reconfigure support, disabled/failure reason) plus the current configuration, sourced from the integration's diagnostics dump (secrets redacted by the integration). When the integration ships no diagnostics platform, pass `--probe-options-flow` to read current values from a transient options flow (started and immediately aborted); without the flag no options flow is started and the note tells you to add it. Under a policy that denies `edit config_entry:<entry_id>/options` the probe is skipped, nothing is started, and the note says so, naming the rule — `config show` itself still answers (D-79). The `config_source` field (`diagnostics` | `options_flow` | `unavailable`) tells you which. Read-only; needs an admin token.

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
References, below). To find a term *inside* values — `balkony` in
`sensor.balkony_p_ac`, a word in a markdown card — use `hactl grep`, which
substring-searches every dashboard and the config files in one call.

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

### Term search (`grep`)

```bash
hactl grep balkony                          # every config-file key/value and dashboard value containing it
hactl grep küche                            # case-insensitive: finds Küche and KÜCHE (not "kueche")
hactl grep Licht --case-sensitive           # exact case only
hactl grep --regex 'wp_(vl|rl)'             # a pattern instead of a substring
hactl grep balkony --json                   # one document: per-source counts + every hit row
hactl grep balkony --allow-partial --json   # answer from what could be read when a source is unavailable
```

Requires hactl-companion — it reads the config half. `grep` answers "where does
this **term** appear?" in one read-only call: every string value and every
mapping key in the YAML config files (following `!include`, the same files `ref
scan` reads) and every string value in every dashboard. It has no `--confirm`
and never writes; to rename an id everywhere use `ref replace`, on the exact id.

**What it searches.** Config files: string values and mapping keys (a key hit is
where a script or YAML helper is *defined* — `licht_an:` under `script:` — which
no value holds), reported with `kind` `key` or `value`. Dashboards: string
values only (dashboard keys are card schema, never your words). Not searched:
numbers and booleans, `!secret`/`!input` values, files nothing includes
(esphome, blueprints, custom_components), and entity display names, registry
data or states — for display names use the name search under "Filtering &
discovery".

**Matching.** A substring, case-insensitive by default, by the simple Unicode
lowercase on both halves: `küche` finds `Küche` and `KÜCHE`, `istanbul` finds
`İstanbul`, `straße` does **not** find `STRASSE`, and nothing is transliterated.
`--case-sensitive` compares as written. A term must be at least 2 characters
(`ü` is one); an empty or whitespace-only term is refused. Both refusals happen
before anything is contacted.

**`--regex`** reads the term as a pattern, searched anywhere in each string,
case-insensitive unless `--case-sensitive`. The two halves run in two regex
engines (Python on the companion, Go here), so only their common subset is
accepted, and hactl refuses everything else before contacting anything:

- accepted: literals and escaped punctuation (`sensor\.wp`), classes with
  explicit members (`[a-z_]`, `[^0-9]`, `[äöüß]`), `|`, groups `(…)`, `(?:…)`,
  `(?P<name>…)`, `* + ? {n} {n,} {n,m}` (n, m ≤ 1000) and their lazy forms, `.`,
  `^`, `$`, `\A`, leading flags `(?i)` `(?m)` `(?s)`, scoped `(?m:…)` `(?s:…)`
  and `(?i:…)` inside an already case-insensitive pattern,
  `\t \n \r \f \v \a`, `\xHH`;
- refused because the engines read them differently: `\w \W \d \D \s \S \b \B`
  (write `[A-Za-z0-9_]`, `[0-9]`, `[ \t\n]` instead), digit escapes such as
  `\12` (write `\x0a`), `{,n}` (write `{0,n}`), POSIX classes `[[:alpha:]]` and
  any `[`, `--`, `&&`, `~~` or `||` inside a class, and a group that switches
  case-sensitivity for part of the pattern — `(?-i:…)` in the default
  case-insensitive search, `(?i:…)` under `--case-sensitive` (spell the case
  variants out instead: `[Kk]üche`);
- refused because only one engine has them: lookahead/lookbehind,
  backreferences, possessive and atomic groups, `\Z`, `\z`, `\p{…}`, `\Q…\E`,
  `\x{…}`, `\u…`, `(?<name>…)`, `(?U)`, a flag group that is not at the start
  (`a(?i)b`) or that negates (`(?-i)`), repeat counts above 1000.

`$` means what it means in Python on both halves: the end of the value, or just
before one final newline (block scalars keep theirs). Case-insensitive matching
is Python's `re.IGNORECASE` on both halves too, which treats a few characters as
cases of each other where Go alone would not — `I`, `i`, `İ` and `ı` are one
letter, as are `ΐ`/`ΐ`, `ΰ`/`ΰ` and `ﬅ`/`ﬆ` — so `--regex istanbul` finds
`İstanbul` and `ISTANBUL` in config files and dashboards alike. A pattern is not
length-limited; a pattern the companion cannot finish within its deadline
(catastrophic backtracking) is refused with its message.

**Output.** The answer opens with one line naming the term, the mode and the
counts, then a summary with one row per source (`config` file or `dashboard`)
and its exact `keys`/`values` hit counts — always complete, so you can see where
the hits are before reading any. The hit rows follow (`source`, `location`,
`path`, `key` = the enclosing mapping key such as `alias`, `entity` or
`value_template`, `value`), capped by `--top` with `…+N more`; long values are
shortened in the table and whole under `--json`/`--full`. Narrow a noisy term
rather than raising the caps: the per-source counts say where to look next. Each
half returns at most **1,000** hit rows; beyond that the answer says
`truncated` and gives that half's exact total — never a silently short list.
`--json` is one object: `term`, `regex`, `case_sensitive`, `total`,
`truncated`, `config` and `dashboards` (each `{total, truncated}`), `sources`
(`{source, location, key_hits, value_hits}`), `hits` (`{source, location, path,
key, kind, value}`), `partial` and `unread`.

**A miss** reads `no hit for "<term>" in any config file or dashboard` — a
verified negative for that substring (or pattern) over both halves. After a
partial sweep it reads `… in what could be searched` instead.

**Partial sweeps** follow the `ref scan` rule (see References): an unread config
file or dashboard is named in a `partial sweep: …` line above the answer;
`--json` refuses until you pass `--allow-partial`, and then carries `"partial":
true` and the `unread` list. A missing companion is not a partial sweep —
`grep` aborts, as every `ref` command does.

**How it differs from its neighbours.** `ref scan <entity_id>` and `dash grep
<value>` match an *exact* id or a *whole* value and exist to feed `ref replace`
/ `dash replace`; `grep` finds a *term inside* anything, for discovery. `ent ls
--pattern` matches entity identifiers in the registry, which `grep` does not
read.

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
references to an exact id, not free-text terms (a term inside values is
`grep`, above; display names are the name search under "Filtering &
discovery") — but its two halves do not match
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
(`'sensor.x'`, `.turn_on`, `{{`). For term discovery use `grep`, which takes
any substring of 2+ characters.

`replace` changes only the bytes of each matching value in a config file — its
quotes, block style and every other line stay as they were. When the companion
cannot verify that in-place edit for a file, it rewrites that file whole
instead (content correct, formatting elsewhere in it normalized): the row's
status then reads `applied (file re-serialized whole)` — `pending (would
re-serialize the file whole)` in a dry run — and a `note:` line after the table
names the file, so a config kept in git is not surprised by an unrelated diff.

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
hactl rtfm --reference policy             # a family's reference section, which no command delivers on its own
hactl rtfm --skill policy-setup           # a procedure for an agent, shipped in the binary (today: policy-setup)
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

### Policy (permission guardrail)

```bash
hactl policy show                                  # the profile in effect, its rules, the file it came from
hactl policy check entity:lock.front_door control  # exit 0 allowed, 1 denied; names the rule that decided
hactl policy check automation:pg_heating write     # every write verb at once; exit 0 only if every enforced one is allowed
hactl policy lint                                  # validate the file, then check every rule against the live instance
hactl policy lint --allow-partial                  # accept what lint could not check (it is still listed)
hactl rtfm --skill policy-setup                    # asked to propose your own rules: how to draft them
hactl policy lint --file policy.draft.yaml         # check a draft; show and check take --file too
hactl policy review --file policy.draft.yaml       # what the draft would allow here, in counts and names
hactl rtfm --reference policy                      # the full reference: file format, grammar, every --json field
```

**A guardrail against mistakes, not a security boundary.** `<instance>/policy.yaml` tells hactl what may be changed. It stops a misread instruction, a target wider than meant, a typo in an id; it does not stop anything with a shell — the token in `.env` can do everything. **The file is the owner's: never edit, move or delete `policy.yaml`.** Only the owner puts a policy in effect, with `hactl policy activate` in a terminal of their own; you may draft one (`policy.draft.yaml`, see `hactl rtfm --skill policy-setup`), never activate it. A denial is an answer to report, not an obstacle to remove.

**What is enforced.** While a profile is in effect, every command that switches, edits, creates or deletes something works out everything its `--confirm` run would touch and checks all of it in the dry run, and again under `--confirm`, before its first write. All or nothing: if one of 40 files is denied, none is written. Every command that reads is filtered the same way: a listing drops what the profile may not read and ends with `hidden by policy: N (profile <p>)`, a denied attribute reads `<denied>`, and reading one denied thing is refused (see "Enforcement (reads)"). Without `policy.yaml` nothing changes and nothing is checked.

**A refusal** exits 1 with the error class `policy_denied`; the report is on stdout:

```console
$ hactl auto delete prod_heating
policy_denied: would delete automation — refused by policy, nothing was written
  denied: delete automation:prod_heating — deny "automation:prod_*" [delete] (line 14)
  checked: 2 resources, 1 denied
  profile: agent (selected by active)
  hint: the owner would change the rule on line 14; allowed: 1 of 2 checked resources, but the command is refused as a whole — ask the owner; a change goes through a draft and `hactl policy activate`
```

stderr carries one summary line, `policy_denied: 1 of 2 resources denied (profile agent)`. With `--json` it is the preview object with `"ok": false`, `"error": "policy_denied"`, every denial under `details.policy.denied` and the same `hint`.

Read the rule each `denied:` line names:

- `deny "…"` — the owner wrote a rule against this;
- `default: deny` — no rule allows it; an indented `where not met:` line under it says that an allow matched but its area or label did not hold for this resource;
- `fail-closed: …` — hactl could not work out everything the command would touch (Home Assistant did not answer, a file could not be read), so it refused rather than guess.

Report the refusal to the user with the rule and its line. Do not work around it: dropping the denied part is a different, narrower command, which the user confirms like any other. A refused dry run does not open the `--confirm` window. A `--confirm` can still be refused after its preview passed — when the policy, an area or a label, or what a service call reaches changed in between — and the refusal says so.

**Ask before you act.** `policy check <resource> <verb>` answers without changing anything. Verbs: `read`, `control` (switch: `svc call`, `script run`), `edit`, `create`, `delete`, or `write` for every write verb that applies there. A resource is `<kind>:<id>[/<sub>]`: `entity:light.kitchen`, `entity:light.kitchen/registry/labels`, `automation:<config id>` (the `id:` in its config — `config_id` in `auto ls --json` — not the alias), `script:<id>/run`, `helper:<domain>/<id>`, `dashboard:<url_path>/views/<path>`, `service:light.turn_on`, `file:packages/x.yaml`. The answer names the rule that decided it; the exit code is the verdict. The reference lists every kind and sub-resource.

**A broken file** — a typo, an unknown key — refuses every command except `policy show`, `policy lint`, `version`, `help`, `completion` and `rtfm` (and the owner's `policy activate` and `agent add`), with one `policy_broken: <file>:<line>: <message>` line per problem. The owner fixes it; report it, do not edit it.

**Over MCP**, a server started without `--allow-writes` refuses every write first; with it, the policy decides each write exactly as on the command line.

**Writing or reviewing a policy?** Run `policy lint` before relying on a deny: a misspelt deny matches nothing and protects nothing. Everything else — the file format, matching, what each command checks, every `--json` field, worked examples such as protecting one automation — is in `hactl rtfm --reference policy`.

### Policy reference

The policy family's full contract: the file format, the address grammar, how a verdict is reached, what each write command checks, and every `--json` field. Nothing delivers it automatically — `hactl rtfm --reference policy` prints it (D-121); the how-to above is what a caller working under a policy needs.

```bash
hactl policy show --as agent                       # what-if: another declared profile's rules; changes nothing
hactl policy show --json
hactl policy check entity:device_tracker.jan/attr/latitude read --as agent   # what-if verdict
hactl policy check automation:pg_heating edit --json
hactl policy check entity:lock.front_door write --json   # one verdict per write verb that applies
```

**A guardrail against mistakes, not a security boundary: the token in .env can do everything** (D-51, level A). A policy stops a misread instruction, a too-wide target or a typo in an id; it does not stop anything with a shell, which can reach Home Assistant directly or edit the file. hactl reads `<instance>/policy.yaml` and writes it in exactly two places, both the owner's and both behind a terminal: `policy activate` and `agent add`'s read-only starter (D-148). The policy is the owner's: never edit, move or delete `policy.yaml`. A denial is an answer to report, not an obstacle to remove.

**Writes and reads are enforced.** Every command that changes something — `control`, `edit`, `create`, `delete` — checks its whole plan against the policy in its dry run and again under `--confirm`, and refuses before its first write if anything in the plan is denied (see "Enforcement" below). Every command that reads filters what it reads the same way: a listing drops what you may not read and says how many, a denied attribute reads `<denied>`, and reading one denied thing is refused (see "Enforcement (reads)"). `policy show` says `enforced: writes and reads` (`"enforced": true`, `"enforced_verbs": ["read", "control", "edit", "create", "delete"]` in `--json`).

**No file, no change.** Without `policy.yaml` everything is allowed, byte-identical to hactl without a policy (D-58) — in what every command sends, writes and prints as its own answer (D-108): `policy show` says `no policy file`, `policy check` allows everything with rule `no policy file`, `policy lint` reports nothing and exits 0.

#### Policy file format

```yaml
# <instance>/policy.yaml
version: 1
active: agent                     # the profile in effect, for CLI and MCP alike
profiles:
  default:                        # in effect when there is no active:
    deny:
      "entity:lock.*":                  [write]
  agent:
    default: deny
    allow:
      "entity:*":                       [read]
      "entity:light.*":                 [control]
      "automation:pg_*":                [read, edit, create]
      "entity:*/registry/labels":       [edit]
    deny:
      "entity:lock.*":                  [read, write]
      "entity:device_tracker.*/attr/latitude":  [read]
      "file:**":                        [write]
    where:
      "entity:light.*": { area: wohnzimmer }
```

- `version` — required, the integer `1`.
- `active` — optional, the name of a declared profile.
- `profiles` — optional, a mapping of profile name to profile. A profile has up to five keys:
  - `default` — `deny` or `allow`; omitted means `allow`. Only a profile that declares `default: deny` denies what no rule matched (D-52).
  - `allow`, `deny` — a mapping of resource pattern to a non-empty list of verbs.
  - `indirect` — optional; the only value is `unchecked`, which switches off the check of what a written automation, script or template would do (D-135). Omitted means checked.
  - `where` — a mapping of an `allow` pattern of the same profile, written exactly as in `allow`, to a selector `{area: <v>, label: <v>}`. A value is a string or a non-empty list of strings, held to the same Unicode rule as an id (D-69, under Resources).

**An instance that must have a policy** (D-136). `HACTL_POLICY=required` in the instance's `.env` turns a missing `policy.yaml` from "allow-all" (D-58) into a refusal: every command except `version`, `help`, `completion`, `rtfm`, `policy show` and `policy lint` exits 1 with `policy_missing: <path> — this instance requires a policy; the owner restores it` on stderr, followed by a `why:` line (the file does not exist, or selects no profile) and a `hint:` line. A `policy.yaml` that exists but selects no profile (`implicit-allow-all`) is refused the same way. An agent's instance directory should always carry it; `hactl agent add` writes it.

**Which profile applies** (D-61, D-62). Only the file selects it, never the caller: there is no `--profile` flag, no `HACTL_PROFILE` variable and no `hactl mcp --profile`; the owner switches profile by editing `active:`.

| the file has | profile in effect | `selected_by` |
|---|---|---|
| no file at all | none — allow-all | `no-file` |
| `active: <p>` | `<p>` | `active` |
| no `active:`, a profile named `default` | `default` | `default-profile` |
| no `active:`, no `default` profile | none — allow-all, and `policy show` says so loudly: `ALLOW-ALL` | `implicit-allow-all` |
| (`--as <p>` on `show`/`check`) | `<p>`, for this answer only | `what-if` |

`--as <profile>` is a read-only what-if: it evaluates another declared profile and never changes the effective one. Its output says `what-if` (`"what_if": true`). It names a declared profile; any other name, and any name when there is no file, is refused (exit 1).

Nor does the caller choose the instance, and with it the policy file, per call over MCP (D-64): an `hactl mcp --dir <instance>` server refuses a call whose `--dir` names another directory (see "MCP server"). On the CLI, `--dir` picks the instance as always — another instance directory is another instance, with its own `policy.yaml`.

**A broken file fails closed** (D-60). A file hactl cannot read as a policy is refused, never read leniently, because a typo must never silently become allow-all. Broken means any of:

- not valid YAML, the top level is not a mapping, or more than one YAML document (a second `---` would be ignored);
- a YAML anchor, alias or merge key (`&d`, `*d`, `<<:`) anywhere, an anchor nothing refers to included — write every value out;
- any mapping key twice at any level (top level, a profile name, a profile's keys, an `allow`/`deny` pattern, a `where` pattern, a selector key) — both lines are named;
- a key this section does not define, at any level (`profile:` for `profiles:`, `allows:`, a selector key other than `area`/`label`);
- a value of the wrong shape: `profiles`, a profile, `allow`, `deny`, `where` or a selector that is not a mapping — an empty `agent:` included (write `agent: {}`);
- `version` missing or not `1`;
- `active` present but empty (`active:`, `active: ~`, `active: ""`) or not a string, or naming a profile the file does not declare;
- `default` other than `allow` or `deny`;
- a pattern that breaks the grammar below — unknown kind, wrong number of id segments, unknown sub-resource, a wildcard where the grammar wants a literal, a `system` id that names (or, as a glob, matches) none of its set, a character outside the id's charset, a `.`/`..` or ambiguous `block` segment in a `file` id, or an `entity`/`service` id with neither a `.` nor a wildcard (it can never match: `entity:front_door` — write `entity:*.front_door`; D-63);
- a verb list that is empty, not a list, or holds an unknown verb (`wirte`);
- a `where` key that is not an `allow` pattern of the same profile, a `where` on a kind other than `entity` and `device` (or on `*`), an empty selector, or an empty selector value;
- a `policy.yml` in the instance directory — hactl reads only `policy.yaml`, and a policy saved under the wrong name must not silently be no policy;
- a `policy.yaml` (or `policy.yml`) that exists but cannot be read — a dangling symlink, a directory, no permission: the error names the path, and it is never `no policy file`.

Every problem is one line, worded the same everywhere — in `policy show` and `policy lint` (stdout), in `policy check` and every refused command (stderr), and in an MCP refusal: `policy_broken: <file>:<line>: <message>`. An unknown verb, key, kind, sub-resource word or registry field name ends ` — did you mean "<word>"?` when a valid word of that position is within two edits (the nearest; a tie goes to the first in this manual's order): `policy_broken: /home/u/ha/home/policy.yaml:12: verbs for "entity:lock.*": unknown verb "wirte" (want read, control, edit, create, delete or write) — did you mean "write"?`. The output closes with one line, `policy_broken: policy.yaml belongs to the owner; only the owner changes it, through `hactl policy activate` — report this, do not edit it` (D-117). `policy show` and `policy lint` report the problems (exit 1; their stderr line is `policy_broken: <n> problem(s) in <file>`); `policy check` refuses (exit 1, empty stdout). **Every other command refuses the same way** — exit 1, the problem lines and the closing line on stderr, nothing on stdout, nothing sent to Home Assistant — reads included, because a file that cannot be read cannot say which reads it would have allowed. The only commands that still run are `policy show` and `policy lint` (so you can see what to fix) and the ones that read no instance and send nothing: `version` (and `--version`), `help` (and `--help`), `completion` and `rtfm`. `setup` refuses like every other command — fix the file first. `hactl mcp` starts, and refuses each tool call the same way except that same list (D-60); the file is read again on every call, so fixing it takes effect without a restart.

#### Resources

A rule pattern and a `policy check` resource share one address grammar:

```
<kind>:<id>[/<sub>[/<sub>]]
```

The id is Home Assistant's own identifier, written exactly as hactl prints it (an automation's config id, not its alias — D-1).

| kind | id | sub-resources |
|---|---|---|
| `entity` | entity_id (`light.kitchen`) | `state` · `attr/<name>` · `registry/<field>`, field one of `name` `area_id` `labels` `icon` `disabled_by` `entity_id` · `history` · `stats` |
| `device` | device_id | `registry/<field>`, field one of `area_id` `labels` `name_by_user` |
| `area` · `floor` · `label` | registry id | — |
| `automation` | config id | `config` · `trace` |
| `script` | config id (the object id) | `config` · `trace` · `run` |
| `template` | unique_id — of a YAML template entity; a template helper made in the UI is a config entry (`config_entry:<entry_id>`, its entity `entity:<id>`), not a `template:` | `config` |
| `helper` | `<domain>/<id>` — two segments (`input_boolean/guest_mode`); no command edits a helper's config (`helper create`/`delete` only), and switching one is `control` on its entity | `config` |
| `dashboard` | url_path; the default dashboard is the empty id (`dashboard:`, `dashboard:/views/home`) | `views/<path>` · `resources` |
| `config_entry` | entry_id | `options` · `flow` |
| `file` | companion-relative path, one or more segments | `block/<id>` |
| `service` | `<domain>.<service>` | — |
| `system` | one of `logs` `wireguard` `reload` `check_config` `cache` `template` | `<domain>`, below `reload` only (`system:reload/automation`) |

- A sub-resource path may stop early: `entity:x/attr` is every attribute of `x`, `entity:x/registry` every registry field.
- Sub-resource words (`attr`, `registry`, `views`, `block`, …) and the registry field names are literal: a misspelling is a broken file, not a rule that matches nothing.
- For `file`, a trailing `/block/<id>` is always the block sub-resource; everything before it is the path. A segment named `block` (in any case) anywhere else — `file:a/block`, `file:a/block/b/c.yaml`, `file:block/x` with no path before it — is refused as ambiguous with the block sub-resource (D-67).
- A `file` path never holds a `.` or `..` segment (`file:./secrets.yaml`, `file:packages/../secrets.yaml`): refused, never canonicalised (D-67).
- **Id charset** (D-68). `entity` and `service` ids: ASCII letters, digits, `_` and `.`; `helper`: ASCII letters, digits and `_` in each of its two segments; `area`, `floor`, `label`: ASCII letters, digits and `_` — Home Assistant's own charset, letters in any case (D-2). Patterns may add `*` and `?`. Every other kind's id takes any character except these, which no id segment and no free sub-resource segment (`attr/<name>`, `views/<path>`, `block/<id>`, `reload/<domain>`) of any kind may hold: white space at either end (a leading or trailing space, a trailing no-break space), a control character anywhere (a tab, NUL), and an invisible format character anywhere (Unicode category Cf: zero-width space and joiners, word joiner, BOM, soft hyphen, bidi overrides). White space inside is allowed where Home Assistant allows it — in `automation`, `script` and `template` ids, `dashboard` url_paths, `file` paths and every free sub-resource segment (`file:packages/my file.yaml`, `entity:weather.home/attr/Hourly forecast`); `device` and `config_entry` ids, which Home Assistant generates, hold no white space at all. Free sub-resource segments take no other charset.
- **One Unicode rule for every text site** (D-69). In every id, every free sub-resource segment and every `where:` value, anywhere in the text, these are refused as well: any White_Space character other than the ASCII space (a no-break space, an ideographic space, a line or paragraph separator, a tab), any Default_Ignorable_Code_Point (a variation selector, a combining grapheme joiner, a Hangul filler, besides the format characters above), and bytes that are not valid UTF-8. The ASCII space stays refused at either end. The same rule applies to every id hactl reads off an outgoing request (see Enforcement): a request whose id breaks it cannot be mapped, and a write carrying it is refused.
- A `policy check` resource is concrete: it holds no `*` or `?`. A resource that breaks any rule of this section is refused (exit 1, nothing on stdout); a pattern that breaks one is a broken file.
- Kinds, sub-resource words and verbs are lowercase, exactly as written here.

#### Verbs

| verb | means | e.g. |
|---|---|---|
| `read` | see it | `ent show`, `config file`, `dash show` |
| `control` | change runtime state, not config | `svc call`, `script run` |
| `edit` | change an existing config or registry field | `auto apply`, `ent set-label`, `dash save` |
| `create` | bring a new resource into existence | `auto create`, `area create` |
| `delete` | remove it | `auto delete`, `dash delete` |

`write` stands for the write verbs — `control`, `edit`, `create`, `delete` — **that apply at the rule's address** (D-53, D-110). A verb applies at an address only if some command asks for it there; the table below is the complete list, held in lockstep with what hactl's checks emit. A pattern covers its own address and everything below it, so a verb applies at a pattern when the table lists it at any address the pattern covers (`*` covers every kind). **A deny also holds every verb that changes what it covers from above** (D-125) — a part of it is denied, so all of it is: `create` and `delete` reach every part of what they create or delete, `control` on `entity:<id>` changes its `state` and `attr/…`, and `edit` on `file:<path>` rewrites its `block/…`. An allow does not: it grants only the part. So `"entity:*/registry/entity_id": [write]` means `[edit]` in an allow and `[edit, create, delete]` in a deny — never `control`, because no command switches a registry field; the no-renames rule is therefore `deny "entity:*/registry/entity_id": [edit]`, since with `[write]` it refuses deleting (and creating) every entity, and `policy lint` reports each allow it overrides as `blocked-by-part` (D-127) — while `"entity:lock.*": [write]` means all four, `deny "entity:lock.*/state": [write]` means control, create and delete, and `"system:reload/*": [write]` means nothing. `policy show` lists the result as `expanded` (in text, ` → [<verbs>]` after a rule holding `write`). `read` applies at every address.

| address | write verbs that apply there |
|---|---|
| `entity:<id>` | control, create, delete |
| `entity:<id>/registry/<field>` | edit |
| `device:<id>` | delete |
| `device:<id>/registry/<field>` | edit |
| `area:<id>` | create, edit, delete |
| `floor:<id>`, `label:<id>` | create, delete |
| `automation:<id>`, `template:<id>`, `helper:<domain>/<id>`, `script:<id>` | control (checked for denies only — Enforcement, "Service calls: reach"), create, delete |
| `automation:<id>/config`, `template:<id>/config`, `script:<id>/config` | edit |
| `script:<id>/run` | control |
| `dashboard:<url_path>`, `dashboard:<url_path>/views/<path>` | create, edit, delete |
| `config_entry:<id>` | create, edit, delete |
| `config_entry:<id>/options` | edit |
| `file:<path>` | edit |
| `service:<domain>.<service>` | control |
| `system:wireguard` | control, edit |
| `system:cache` | edit, delete |
| every other address: `entity:<id>/state`, `/attr/<name>`, `/history`, `/stats`; `automation:<id>/trace`, `script:<id>/trace`; `helper:<domain>/<id>/config`; `dashboard:<url_path>/resources`; `config_entry:<id>/flow`; `file:<path>/block/<id>`; `system:logs`, `system:check_config`, `system:reload`, `system:template` | none — `read` only |

A verb written by name is kept as written, on any kind — the file is not broken, and a check of exactly that address still matches it — but a write verb that applies nowhere its pattern reaches (for a deny, counting the verbs that change it from above) decides nothing a command asks: `policy lint` reports it as `verb-not-applicable`, and a `write` that applies to nothing the same way. `policy check` accepts `write` and answers each write verb that applies at the resource (see `policy check`).

#### Matching

- The pattern `*` matches every resource of every kind.
- Otherwise the kind must be equal, and the id is matched **segment by segment** with the same glob engine as `--pattern`: case-insensitive (D-2), `*` any run of characters and `?` exactly one character (one Unicode code point, not one byte — `file:k?che.yaml` matches `küche.yaml`), but neither crosses a `/`. A segment that is exactly `**` matches zero or more whole segments, and only in a `file` id (`file:packages/**`).
- **The full id only** (D-63). Unlike `--pattern`, a policy pattern has no second anchor after the domain (D-28 is for filters): `entity:kitchen*` matches no entity, because every entity id starts with its domain, and `entity:sensor.*` does not match `binary_sensor.x`. Across domains write the domain as a wildcard: `entity:*.front_door` matches `lock.front_door` and `cover.front_door`. An `entity` or `service` pattern with neither a `.` nor a wildcard (`entity:front_door`, `service:turn_on`) can never match and is a broken file.
- **Unlike `--pattern`, a pattern without a wildcard is exact, never a substring.** `entity:light.kitchen` matches `light.kitchen` (any case) and not `light.kitchen_2`: a filter may err towards more rows, an allow may not err towards more rights.
- A pattern without a sub-resource covers the resource and everything below it. A pattern with one covers that sub-resource and everything below it, and not the resource itself (each sub segment glob-matched the same way).

#### Evaluation

For one resource and one verb, in the effective profile (D-52):

1. **A deny that matches decides: denied.**
2. Otherwise, **an allow that matches decides: allowed** — if it has a `where:`, only when the selector holds.
3. Otherwise, **the profile default decides:** `default: deny` denies, anything else allows.
4. **Partial** (D-66). If steps 2–3 allowed the resource but a deny of the same profile, for the same verb, covers a strict descendant of it **and that verb applies at the deny's own address** — the table's verbs there, or a verb that changes it from above (Verbs, D-110, D-125) — the verdict is partial. A strict descendant is the same kind and id with one or more further sub-resource segments: `deny "entity:*/registry/entity_id" [edit]` covers a descendant of `entity:light.x` and of `entity:light.x/registry`, and a deny on `entity:device_tracker.*/attr/latitude` one of `entity:device_tracker.jan`. A `file` path is not a descendant of another path — a deny on `file:packages/x.yaml` leaves `file:packages` whole; `file:packages/x.yaml/block/b` is a descendant of `file:packages/x.yaml`.
   - For a write verb (`control`, `edit`, `create`, `delete`) **partial is denied**: the rule named is the first of those descendant denies.
   - For `read` **partial is allowed**: the rule named is still the one that allowed it, and the descendant denies are listed — the parts a phase-3 read will mask (D-54).

   A deny whose verb does not apply where the deny sits never makes anything partial: `deny "entity:*/registry/entity_id" [write]` holds no `control`, so `control entity:light.x` is allowed, and an explicit `deny "entity:*/registry/labels" [control]` refuses nothing — no command's `control` changes a registry field, and `policy lint` says so. A verb that changes the deny's part does make it partial: `deny "automation:x/config" [write]` refuses `delete automation:x` (deleting the automation deletes its config), `deny "entity:lock.*/state" [control]` refuses a service call on a lock, and `deny "file:x.yaml/block/b" [edit]` refuses an edit of `file:x.yaml`. A deny that matches the resource itself, or an ancestor of it, is step 1, never partial. A verdict with no profile in effect (`no policy file`, implicit allow-all) is never partial.

Rule order in the file means nothing; deny always overrides allow, so there are no exceptions to exceptions — narrow the allow instead. A parent covers its children: `read` on `entity:light.kitchen` covers `entity:light.kitchen/attr/brightness`. A deny on a child cuts out only the child — and makes a check on the parent partial (step 4). An allow on a child alone grants only the child (`entity:*/registry/labels` [edit] allows editing labels and nothing else of the entity). When several rules of the deciding kind match, the one named is the first in the file: the lowest line, then the leftmost column (two rules can share a line in a flow mapping, `deny: {"entity:lock.*": [write], "entity:*": [control]}`).

**Selectors** (`where:`, D-55) narrow an allow on an `entity` or `device` pattern and are evaluated live on every check — a relabel takes effect at once. `area` holds when the resource's area matches by id or name: for an entity its effective area, its own or else its device's (H-8); for a device its own. `label` holds when one of the resource's own labels matches by id or name (an entity does not inherit its device's labels). Values glob like patterns; several values of one key are alternatives; `area` and `label` together must both hold. A selector is evaluated only while it can still change the verdict or the rule it names: never when a deny matches the resource (step 1), and the narrowed allows that match are tried in file order (line, then column) — none after an allow that already grants, and none placed after a matching allow without `where:`. If a tried selector cannot be evaluated — Home Assistant unreachable — that narrowed allow does **not** match, the next one is tried, and the verdict says so in a note. A tried selector that evaluated to false is reported when no allow granted, so a denial by the default says which narrowed allow matched the resource and why its selector did not hold (`unmet_where`, text `where not met:`, D-112): the reason names the first key that failed, area before label — `input_boolean.pg_x has no label pg_core_label`, `light.desk is in area office, not wohnzimmer`, `light.desk has no area` (an entity's area is its own, or else its device's). A selector never tried is not evaluated and not noted.

Every verdict names what decided it, as `deny "entity:lock.*" [read write]`, `allow "entity:light.*" [control] where area=wohnzimmer`, `default: deny`, `default: allow`, `no policy file`, or `implicit allow-all: no active: and no default profile`. A rule is shown with its verbs as written.

#### Enforcement (writes)

**When it applies.** Only while a profile is in effect (D-74). With no `policy.yaml`, or with a file that puts no profile in effect (`implicit-allow-all`), every command behaves byte-identically to hactl without a policy (D-58): no check, no extra request to Home Assistant, no audit line. Writes — the verbs `control`, `edit`, `create`, `delete` — are enforced; `read` is not yet (see the top of this section).

**The plan is checked in the dry run.** Every write command first works out everything its confirmed run would touch — each `(resource, verb)`, including the follow-up writes the command makes on its own — and checks all of it **before printing its preview**. `--confirm` checks the same plan again before its first write. So a dry run refuses exactly what `--confirm` would refuse, naming the same rule (preview parity, H-2). A passing dry run means the confirmed run is allowed **as long as nothing it depends on changes**: `--confirm` re-reads `policy.yaml`, re-evaluates `where:` selectors and recomputes a service call's reach, so an edited policy, a moved or relabelled entity, or a grown area can still refuse it — and that refusal says the preview had passed (D-80). A refused dry run does not count as the dry run that authorises `--confirm`: the 30-minute window opens only on a preview that passed, so a `--confirm` right after a refused preview is refused by the confirm guard (its own message, no rule) before the policy is asked. The commands that write without `--confirm` (`cache clear`, `cache refresh`, `config flow-inspect`, `config show`'s probe) check the same way before they write.

**All or nothing** (E-7). If any resource in the plan is denied, the whole command is refused and nothing is written — also when the other 39 of 40 files are allowed. The refusal lists **every** denied resource, not only the first. `--allow-partial` never overrides a denial: it accepts sources that could not be *read*, not resources the policy refuses.

**What a refusal looks like.** Exit 1, the error class `policy_denied`, the report on stdout, one summary line on stderr:

```console
$ hactl auto delete prod_heating
policy_denied: would delete automation — refused by policy, nothing was written
  denied: delete automation:prod_heating — deny "automation:prod_*" [delete] (line 14)
  checked: 2 resources, 1 denied
  profile: agent (selected by active)
  hint: the owner would change the rule on line 14; allowed: 1 of 2 checked resources, but the command is refused as a whole — ask the owner; a change goes through a draft and `hactl policy activate`
$ echo $?
1
```

stderr: `policy_denied: 1 of 2 resources denied (profile agent)` — a count, never a repeat of the report (D-119). Under `--json` the report keeps the preview envelope, with `ok` false and the error class (D-70):

```json
{"dry_run": true, "ok": false, "error": "policy_denied", "action": "delete automation",
 "details": {"policy": {
   "denied": [{"resource": "automation:prod_heating", "verb": "delete",
               "rule": "deny \"automation:prod_*\" [delete]", "line": 14, "partial": false}],
   "checked": 2, "profile": "agent", "selected_by": "active", "level": "A", "written": []}},
 "hint": "the owner would change the rule on line 14; allowed: 1 of 2 checked resources, but the command is refused as a whole — ask the owner; a change goes through a draft and `hactl policy activate`"}
```

`dry_run` is `true` for a refused preview and `false` for a refused `--confirm`. `details.policy.denied` lists every denied `(resource, verb)` of the plan in plan order, each with the rule and line that decided it; `"partial": true` marks a denial by a deny on a part of the resource (below), and `"kind_wide": true` a check on every id of a kind, reported as `<kind>:*` (Ids Home Assistant mints, below). `checked` counts the plan's `(resource, verb)` pairs. `written` lists what had already been written when the refusal came — always `[]` for a refusal of the plan; non-empty only when a request was refused mid-run (a hactl bug to report) or when `ref replace` wrote outside its checked plan (below). A refusal hactl makes without a rule — fail-closed, below — has `rule` `fail-closed: <reason>` and `line` 0. A denial by `default: deny` after a narrowed allow was tried carries `"unmet_where": [{"rule": "allow \"entity:*.pg_*\" [control] where label=pg_core_label", "line": 9, "reason": "input_boolean.pg_x has no label pg_core_label"}]` (only when non-empty; Selectors), and the text adds `    where not met: <rule> (line <n>) — <reason>` under its `denied:` line; a denial by a deny on a part of the resource ends `— a part of it is denied, so all of it is`. An allowed preview, while a profile is in effect, adds `policy: <n> checked, allowed (profile <p>)` to its text and `"policy": {"checked": <n>, "profile": "<p>"}` to its `details`. A broken file is `policy_broken` (D-60), also exit 1.

**The hint** (D-118) is the report's last text line, `  hint: <text>`, and the JSON `hint` holds the same text. It is built from the denials' causes, in this order, joined by `; `: `the preview had passed, and something it depends on changed since (the policy, an area or label, a service call's reach)` when that applies; `the owner would change the rule on line <n>[, <m>…]` for every line a deny decided (partial included), ascending; `nothing allows <resource>[ (and <k> more)]; the owner would add an allow` for denials by `default: deny`; `hactl could not check <resource> (<reason>), so it refused` for the first fail-closed denial; `allowed: <a> of <n> checked resources, but the command is refused as a whole` when only some were denied. It always ends `` — ask the owner; a change goes through a draft and `hactl policy activate` ``. A request-layer refusal whose plan had allowed the request says `hactl's own plan missed this request — a hactl bug to report; do not edit policy.yaml` instead. The hint never suggests `--confirm`. The one stderr line is `policy_denied: <m> of <n> resources denied (profile <p>)`, `<m>` the denials and `<n>` the checked pairs; a request-layer refusal appends ` — refused at the request layer[ although the command's plan allowed it (a hactl bug to report)]; already written: <what, or nothing>`.

**Partly denied means denied** (D-66). A write to a resource checks the resource *and* everything below it: with `deny "entity:*/registry/entity_id" [edit]`, a command that edits `entity:light.x` as a whole is refused, naming that deny. Commands write at the finest address they can: `ent set-label` checks `entity:<id>/registry/labels`, never the whole entity; `dash save` checks each changed view (`dashboard:<u>/views/<path>`), and the dashboard itself only for changes outside views, in a view without a `path`, or when the stored config could not be read; a dashboard Home Assistant holds no config for (`config_not_found`, e.g. right after `dash create`) is compared against an empty config, so each view its first save brings is a `create` (D-126); a first save that brings no view (`{}`, an empty or null `views`) is still `edit dashboard:<u>` — it stores a dashboard where none was (D-126). **A change a command can address only at the parent is refused by any denied part, touched or not:** with `deny "dashboard:main/views/secret" [edit]`, changing `main`'s title (a non-view key) is refused. A view deny `[edit]` does not stop removing or re-adding the view through an allow of `create`/`delete` on the dashboard; `[write]` does, but then also refuses deleting and creating the dashboard itself (D-125, D-127). A deny on a part no command writes on its own still refuses every write that changes the part (Verbs, D-125): `deny "file:<path>/block/<id>" [edit]` refuses `ref replace` editing `file:<path>`, whichever block the rewrite touches, and `deny "automation:<id>/config" [write]` refuses `auto delete <id>`. Only a verb that cannot change the part refuses nothing — `deny "entity:*/registry/labels" [control]` — and `policy lint` reports such a rule as `verb-not-applicable`.

**What each write command checks.** `{x}` is the resolved identifier (an automation's config id, never its alias — D-1). The default dashboard is `dashboard:` however it was named (`""`, `lovelace`).

| command | checks |
|---|---|
| `svc call <d>.<s>` | `control service:<d>.<s>`, always (E-2) + `control` on every entity the call reaches, every entity named in `--data` and every member of a group or scene among them (E-1, below); `homeassistant.turn_on`/`turn_off`/`toggle` + `control service:<domain>.<s>` for each domain they reach; a call that starts scripts (`script.<x>`, `script.turn_on`, `script.toggle`, `homeassistant.turn_on`/`toggle`) + `control script:{x}/run` for each; + `control` on the typed address of every automation, script, helper or template entity it reaches, decided by denies only (below) |
| `script run <x>` | `control script:{x}/run` + `control service:script.turn_on` + `control entity:script.{x}` — `{x}` is the script's config key from the entity registry; when hactl cannot read it, the run of every script (`script:*/run`) is checked instead |
| `script apply <x>` | `edit script:{x}/config` + `control service:script.reload` (hactl reloads scripts itself after the write), checked before the diff is shown; an apply with no changes writes nothing and checks nothing |
| `script create`, `auto create`, `helper create <d>`, `tpl create` | `create` on the id the file declares: `script:{key}`, `automation:{id}`, `helper:<d>/{key}`, `template:{unique_id}` for every unique_id in the file — a template entity without `unique_id` has no address, so `tpl create` refuses it (`fail-closed: template entity without unique_id`) |
| `auto apply <x>` | `edit automation:{x}/config`; + `create automation:{id}` when the file's `id:` differs (a rename, E-5); a file with no `id:` (or a non-string one) is refused |
| `auto rollback [x]`, `rollback [x]` | `edit automation:{x}/config` (the target from the backup's name when no `x`); + `create` on the backup's own `id:` when it differs; a backup with no `id:` is refused |
| `auto delete`, `script delete`, `helper delete`, `tpl delete` | `delete` on the definition + `delete entity:{e}` for each registry entry the delete then removes (the cleanup after a delete is part of the plan); an automation without a config id (a YAML entry found by alias) is refused (`fail-closed: automation has no config id`); `helper delete` names the helper's domain in its request — always, with or without a policy (D-123) — so a slug that exists in two domains is deleted in the one `helper show` resolves rather than refused as ambiguous |
| `area create`, `floor create`, `label create` | `create` on **every** id of the kind (Home Assistant mints the id, below) |
| `area delete <x>` | `delete area:{x}` + `edit entity:{e}/registry/area_id` and `edit device:{d}/registry/area_id` for every entity and device whose own area it is |
| `floor delete <x>` | `delete floor:{x}` + `edit area:{a}` for every area on that floor |
| `label delete <x>` | `delete label:{x}` + `edit entity:{e}/registry/labels` and `edit device:{d}/registry/labels` for every entity and device holding it, `edit area:{a}` for every area holding it |
| `ent set-label`, `ent set-area` | `edit entity:{x}/registry/labels` / `…/registry/area_id` (`--clear` and `--remove` too) |
| `device set-label`, `device set-area` | `edit device:{x}/registry/labels` / `…/registry/area_id` |
| `ent rename <old> <new>` | `edit entity:{old}/registry/entity_id` + `create entity:{new}` (E-5) + everything `ref replace` checks (E-7) |
| `ref replace <old> <new>` | `edit file:<path>` for every config file the rewrite touches + `edit automation:{id}/config` / `script:{key}/config` / `template:{uid}/config` for every automation, script or template entity a change lies in (a changed `id:` or `unique_id:` is a rename: + `create` on the new id) + `edit dashboard:<u>[/views/<v>]` for every dashboard it rewrites — all or nothing, so one denied entry in a file refuses the whole replace |
| `dash save`, `dash replace` | `edit` per changed view, `create`/`delete` per added/removed view, `edit dashboard:<u>` for the rest — against an empty config when Home Assistant holds none (D-126), as the whole dashboard when the stored config cannot be read |
| `dash create`, `dash delete` | `create` / `delete dashboard:<url_path>` |
| `config delete <x>` | `delete config_entry:{x}` + `delete` on every device and entity whose registry entry names that entry — checked as a deliberate superset (a device that also belongs to another entry is checked too) |
| `config options <x>` | `edit config_entry:{x}/options` |
| `config flow-step --options`, `config flow-inspect --options` | `edit config_entry:{x}/options`, the entry the flow belongs to — on every entry's options when hactl cannot tell which (a flow started by an earlier command) |
| `config flow-start`, `config flow-step`, `config flow-inspect` | `create` on every config entry id (and `edit` too for `flow-step` and `flow-inspect`: the flow may be reconfiguring an existing entry). `flow-inspect` is checked like a step because asking Home Assistant for a flow runs its current step — it can finish the flow |
| `companion wireguard config` / `up` / `down` | `edit` / `control` / `control system:wireguard` |
| `cache clear` / `cache refresh` | `delete` / `edit system:cache` (hactl's own bookkeeping — the id cache, the confirm window, backups, the audit log — is never checked) |
| `config show` | its options-flow probe needs `edit config_entry:{x}/options`; when denied, the probe is skipped with a note — `config show` itself is never refused for it |

**Service calls: reach** (E-1, D-59). A service call acts on whatever its target expands to — an area, a floor, a label, a device, a group. hactl asks Home Assistant for that set (`extract_from_target`, with groups expanded) in the dry run and requires `control` on each entity in it. A service counts as one domain's entity service only when its domain is not `homeassistant` and has live entities (`light.turn_on`): then only the available entities of that domain count. Any other service — `homeassistant.turn_on`, an integration's own service such as `zwave_js.clear_lock_usercode` that acts on `lock` entities, anything hactl cannot classify — is checked against the whole set the target names, across domains; this can refuse more than the call would touch, never less. `entity_id: all` (in any case) is never sent to that question: it means every live entity of the service's domain for an entity service, and every live entity of every domain otherwise. `homeassistant.turn_on`, `turn_off` and `toggle` call each domain's own service in turn, so they also need `control service:<domain>.<service>` for every domain in the reach; and a call that can start scripts — `script.<x>`, `script.turn_on`, `script.toggle`, `homeassistant.turn_on`/`toggle` — needs `control script:{x}/run` for every script it starts, as `script run` does. Entity ids passed outside the target are checked too: every mapping key and every string value in `--data`, at any depth, that names a live entity needs `control` (`scene.apply` with `entities: {lock.front_door: unlocked}` needs `control entity:lock.front_door`). An entity that passes the call on — a group, a light (switch, cover, fan, lock, media_player) group, a scene — needs `control` on every member its live state lists in its `entity_id` attribute, members of members included, because Home Assistant's answer names only the group or scene itself (D-106): `scene.turn_on` on a scene that unlocks `lock.front_door` needs `control entity:lock.front_door`. The members' own services are not added. **An entity that is a typed resource is also checked at its typed address, for denies only** (D-107): an automation as `automation:<config id>` (its `id` attribute), a script as `script:<config key>`, a helper as `helper:<domain>/<id>`, a template entity as `template:<unique_id>` (each read from the entity registry). A deny there refuses the call — `deny "automation:prod_*" [write]` refuses `svc call automation.turn_off` on `prod_heating` — but neither an allow nor `default: deny` decides it: the call's grant is the entity's. An automation without a config id has no typed address and is checked as its entity; when the registries cannot be read, a script, helper or template address is checked on every id of its kind (`<kind>:*`). **If the reach cannot be computed — Home Assistant does not answer the question, or answers with an error — the call is refused** (`fail-closed: reach unknown`), never sent unchecked. A service with no target and no entity in its data needs only its `service:` check.

**Service calls: the service itself** (E-2). `control service:<domain>.<service>` is checked on every service call, with or without a target — the ones you type (`svc call homeassistant.restart`) and the ones hactl sends itself (`script.turn_on` for `script run`, `script.reload` for `script apply`). The service check is never skipped because the entity checks passed. It is decided like any resource (D-52, D-73): under `default: deny` a service needs an allow (`"service:light.*": [control]`); a profile without `default: deny` allows a service no deny matches. A profile that uses `script apply` under `default: deny` must allow `service:script.reload`. `<domain>.reload` needs no `system:reload/<domain>`.

**Rename** (E-5). Renaming moves a resource to a new id, so it needs `edit` on the old one and `create` on the new one — a deny on the new id stops the rename, and so does a deny on renaming the old. This covers `ent rename` and the hidden renames: `auto apply` (and `auto rollback`) with a file whose `id:` is not the target's, and a `ref replace` that rewrites an automation's `id:` or a template's `unique_id:`.

**Ids Home Assistant mints.** `area create`, `floor create`, `label create` and a new config entry (`config flow-start`, `config flow-step`) send a name or a domain; Home Assistant chooses the id. Since there is no id to check yet, hactl checks `create` on **every** id of the kind (D-75): allowed only if no deny for `create` matches any id of that kind and an allow covers them all (`"area:*": [create]`) or the default allows. So `deny "area:secret" [create]` refuses every `area create`, and so does a `create` deny on a part of any id of the kind (`deny "config_entry:*/options" [create]` refuses every new config entry — partly denied means denied). A refusal reports the resource as `area:*` with `"kind_wide": true`.

**Unmapped requests fail closed** (E-9, D-74). Beneath the plan, every request hactl sends — Home Assistant websocket and REST, and the companion — is mapped to the resources it touches before it leaves, once per request however often it is retried. A request hactl cannot map is refused — any websocket command, any HTTP method, a read too (`fail-closed: unmapped request <what>`, D-145). So is a request whose path holds a `.` or `..` segment or an encoded slash, or whose id breaks the grammar (D-67, D-68, D-69). You should never see one: it means a hactl bug, and the refusal says what was already written.

**`ref replace` and `ent rename`, confirmed** (E-7, D-77). The confirmed run first asks the companion for the rewrite as a dry run, checks everything in it, and only then sends the real rewrite of the same `old`/`new` (`ent rename` does this before its registry update, and sends the rewrite after it through the plan it checked). To find the automation, script or template entity a change lies in, hactl reads the file the change is in and walks the change's path: an entry of a top-level list or of an `automation`/`automation <label>` key that has an `id:` and triggers, actions or a blueprint is that automation; a key of a `script` mapping, or a top-level key holding a `sequence`, is that script (a file that is one script — `!include_dir_named` — is the script its file name names); an entity item with a `unique_id` under a template entity domain of a template block is that entity, and a change in the block's shared part (its trigger, condition, action, variables) lies in every entity of the block. A path that matches the file two ways gets both. A change in a node the file shares through a YAML anchor — `&x` in one entry, `*x` or `<<: *x` in another — lies in every entry that shares it, since the rewrite changes them all (D-104). A change in a file another file pulls in with `!include` (or `!include_dir_list`/`_named`/`_merge_list`/`_merge_named`) lies in the entry the include sits in, up through includes of includes: hactl reads the instance's config files to find them (D-105). A file hactl cannot read, that no longer holds the change's path, that may include the changed file but does not parse, or whose anchors or includes make the entries too many to work out, refuses the run (`fail-closed: typed owner unknown`). A file that starts matching in the moment between the two is written without a check: the run then exits 1 with `policy_denied`, names each such file with `fail-closed: written outside the checked plan` and lists it in `written` (as `edit file:<path>`). In text the rename's report comes first and the refusal after it; under `--json` the refusal is the run's only document.

**What a written automation, script or template would do** (E-3, D-56 as refined by D-133). Writing an automation that unlocks the door is unlocking the door later, so a candidate is checked for what it would switch and what it would read, in the dry run already:

- **every action** in it — at any depth, inside `choose`, `if`, `parallel`, `repeat`, `sequence`, and a template entity's `turn_on`, `lock`, `press`, … lists — is checked exactly as `svc call` would check it: `control` on the service, on everything its target reaches (areas, labels, devices, groups, scenes, `all`) and on every entity its data names, and `run` on every script it starts. `scene: scene.x` counts as `scene.turn_on`; a device action (`device_id`, `domain`, `type`) needs `control` on the entity it names;
- **every other entity the candidate mentions** — in a trigger, a condition, a state template — needs `read` (D-133): an automation that watches the door sensor needs to see it, not to switch it;
- the **whole candidate** is checked, not only what changed against the stored one (D-134), so an edit of an automation that already switches a denied entity is refused until the owner allows it;
- what hactl cannot read off the candidate refuses it, whatever the template evaluates to: a service name that is a template (`action: "{{ … }}"`) — `fail-closed: action service is a template`; a target, or a target key in `data:`, holding a template (`entity_id: "{{ expand(…) }}"`) — `fail-closed: action target is a template`; a device action whose entity cannot be resolved — `fail-closed: device action target unresolved`. Literal entity ids inside a template anywhere else (a message, a condition) are found as in `ref validate` and need `read`;
- `ref replace` and `ent rename` need `control` on the new id wherever a change lies inside an automation, script or template entry.

These checks run for `auto create`, `auto apply`, `auto rollback` (the backup it restores), `script create`, `script apply` and `tpl create`, in the plan and again on the companion write that carries the candidate. A denial names the resolved entity, never a registry uuid. A profile may switch this off with `indirect: unchecked` (D-135); `policy show` then says so on its own line (`indirect: unchecked (line N) — what written automations, scripts and templates would do is not checked`, `"indirect": "unchecked"` in `--json`), and the guardrail no longer covers what written automations do.

**Not covered** (guardrail, D-51). What Home Assistant does on its own after an allowed write, beyond what hactl can enumerate from its registries, is not checked: the companion reloads a domain after writing it, a script you may run does whatever it does. (Deleting a label, area, floor or config entry *is* checked against its current holders, above.) A typed write is checked as its type (`automation:x/config`), not also as the file it lands in (`file:automations.yaml`); a `ref replace` change hactl cannot place in an automation, script or template entry is checked as its file only. Created automations, scripts, helpers and templates are checked as what they are, not as the entities Home Assistant mints for them. `tpl eval` can read any state. A write command's preview shows what it would write, read unfiltered (D-82): `dash replace` and `ref replace` diff the stored configs they rewrite, `ent set-label` names the entity's current labels, `config options` the entry's current options — a profile that denies reading a thing but allows writing it shows that thing in the write's preview. Cross-checking typed and file writes and the built-in deny on `file:hactl/**` (E-8) come in a later version. What a *written* automation, script or template would do *is* checked (above); what an existing one does when it runs is not — an `event:` it fires, the body of a script it starts, a `shell_command` it calls.

**MCP.** `hactl mcp` checks in this order (D-72): a server started without `--allow-writes` refuses every write command first (its own error, no policy involved); then a broken `policy.yaml` refuses (D-60); then the policy decides, exactly as on the CLI. `--allow-writes` permits writes in general; the policy decides which. The policy is read on every tool call, from the pinned instance (D-64), and nothing one call checked is trusted by the next.

**Audit log.** While a profile is in effect, every write check is appended to `<instance>/cache/policy-audit.log`, one JSON object per line — previews and confirmed runs, allowed and denied:

```json
{"time": "2026-11-01T09:42:07+01:00", "command": "hactl auto delete prod_heating", "dry_run": false,
 "layer": "command", "resource": "automation:prod_heating", "verb": "delete", "allowed": false,
 "rule": "deny \"automation:prod_*\" [delete]", "line": 14, "profile": "agent"}
```

A line also carries `unmet_where`, as in the refusal, when a narrowed allow was tried and did not hold. `layer` is `command` for the plan check and `request` for the check of each request sent — one line per request, not per retry. `command` is the command path and its positional arguments; flag values that carry content (`--data`, `-f`) are not logged. At 10 MiB the file is renamed to `policy-audit.log.1`, replacing the previous one; `cache clear` does not remove it. If the line cannot be written, the write is refused (`fail-closed: audit log not writable`) — a preview too, since a preview is what authorises `--confirm` (D-71). hactl never reads the log back, except `agent ls`, which shows its last line; it is for the owner.

#### Enforcement (reads)

While a profile is in effect, **what a command reads is filtered before the command sees it** (D-138): every answer from Home Assistant and the companion passes the policy at the request layer, before any command decodes it (H-21), so no output path — prose, `--json`, a count, an error message — can carry a denied value. This holds for every command, the wide reads included.

- **A listing drops the rows you may not read** — entities, devices, areas, floors, labels, automations, scripts, dashboards, statistics, traces, logbook rows, search hits — and says how many: a last line `hidden by policy: N (profile <p>)` on stdout. Under `--json` the JSON keeps its shape and the same line goes to stderr (D-139). Counts the command prints describe what it shows; the trailer is what it does not (H-11).
- **A part you may not read is masked, not dropped** (D-140): a denied attribute keeps its key and reads `<denied>`, a denied `state` reads `<denied>`, and the trailer counts them: `hidden by policy: 0, masked: 3 (profile <p>)`.
- **Reading one thing you may not read is refused** like a denied write (exit 1, `policy_denied`, the rule and its line): `ent show lock.front_door`, `auto cat <id>`, `trace show`, `dash show <u>`, `config file <path>`, `ent hist`, `log` without `read` on `system:logs`.
- **What is checked:**

| what | checked as |
|---|---|
| an entity's state and attributes | `read entity:<e>`; `…/state`, `…/attr/<name>` mask |
| registry rows | `read entity:<e>` / `device:<d>` / `area:<a>` / `floor:<f>` / `label:<l>` |
| history, statistics | `read entity:<e>/history`, `…/stats` (an external statistic `domain:name` has no address: hidden) |
| automation, script, template config; traces | `read automation:<id>` (`script:`, `template:`); `…/trace` |
| dashboards, a dashboard's views | `read dashboard:<u>`; a denied view is dropped from the config, counted |
| config files, `grep` and `ref scan` hits | `read file:<path>`, plus the typed owner a hit lies in |
| config entries, flows | `read config_entry:<id>` |
| logs, repairs | `read system:logs` |
| logbook (`changes`) | each row by `read entity:<e>` |
| energy preferences | each source by `read entity:<statistic>`; an external statistic is hidden |
| `tpl eval` | `read system:template` — required while the profile denies any read (D-143), because a template can read any state |

- **A row hactl cannot address is hidden**, never shown unchecked (D-141): a listing row without an id, an external statistic, a trace without an item id.
- **An unmapped read is refused** (D-145): a GET or websocket read no row names fails closed, as writes always did (E-9).
- **Not covered** (guardrail, D-51): what a raw config file *says* about a denied entity (`config file automations.yaml` is checked as the file, not redacted), the text of a log message, and anything outside hactl.

- **The trailer counts things, not rows**: an entity two reads of one command hide (its state and its registry row) counts once, an attribute masked in every history row once; a row without an address counts on its own. A read the command could do without (a best-effort part of its answer) and that was refused counts as hidden. A command that ends in a policy refusal prints the refusal and no trailer. Under `--json` — and for any other document a command prints, such as `dash show --raw` — the trailer goes to stderr; over MCP it is a second text item beside the answer.
- **A refused read** is reported exactly like a denied write (D-155, D-70, D-119): exit 1, `policy_denied`; on stdout the refusal report — `policy_denied: would read entity:lock.front_door — refused by policy, nothing was written`, then `  denied: read entity:lock.front_door — deny "entity:lock.*" [read] (line 7)`, `  checked:`, `  profile:` and `  hint:` — and under `--json` the refusal envelope (`"ok": false`, `"error": "policy_denied"`, `"action": "read entity:lock.front_door"`, `details.policy.{denied, checked, profile, selected_by, level, written}`, `hint`); on stderr the one count line `policy_denied: 1 of 1 resources denied (profile agent) — reading it is refused`. The refusal is the whole answer: nothing the command printed before it is shown, and no trailer. The same holds for `tpl eval` refused on `system:template` (`denied: read system:template — system:template needs an explicit allow while the profile denies a read`) and for `log` without `read system:logs`, on the command line and over MCP. A config entry's diagnostics are one body: a denied part (`config_entry:<id>/options`) refuses them whole. `config file --resolve` inlines every file the path includes, so it needs `read` on every file.
- **A write command's own reads are exempt** (D-82): what a write command — every command with `--confirm`, previewed or confirmed, and `config flow-inspect`, `cache clear`, `cache refresh` — reads before it writes (the entity it resolves, the label or dashboard list it looks the target up in, the stored config it diffs against, `validate_config`, the snapshot before a save) is neither refused nor filtered, and no trailer counts it. Its plan decides the write: checked in the dry run and again under `--confirm`, before the first write. So a profile with `edit` on `entity:light.desk/registry/labels` and no `read` at all still labels the lamp with `ent set-label`, while `ent show light.desk` under it is refused and `ent ls` leaves the lamp out. A request no row maps is still refused in a write command (D-145).
- **A hidden view is never deleted by a save** (D-153): `dash save` of a config made from a filtered `dash show` would delete the view `dash show` left out. Deleting a dashboard view the profile may not read is refused whatever allows it (`fail-closed: deletes a dashboard view this profile may not read`), in the preview and under `--confirm`. A masked list field keeps its shape: `"labels": ["<denied>"]`.

Reads are not written to the audit log (D-146); a refused read is reported like a refused write.

#### `policy show`

Prints the file path (or `no policy file`), the level-A sentence above verbatim, the effective profile and how it was selected, the profile default, its rules — deny rules first, then allow rules, each in file order (line, then column) with its line, and after a rule whose verbs hold `write` what it expands to, ` → [<verbs>]` (D-128: `deny "entity:*/registry/entity_id" [write] → [edit create delete]`, `allow "entity:*/registry/labels" [write] → [edit]`) — the declared profiles, and `enforced: writes and reads`. With no `active:` and no `default` profile it says `ALLOW-ALL` and why. `--as <p>` shows profile `<p>` and marks the output `what-if`. **The whole report is printed whatever `--tokensmax` says** — it is a document, like `--json` (Output conventions). Exit 0, or 1 for a broken file: its text report is the `file:` line, the disclaimer, one `policy_broken: <file>:<line>: <message>` line per problem and the closing owner line (Policy file format).

```json
{"file": "/home/u/ha/home/policy.yaml", "exists": true, "valid": true, "problems": [],
 "level": "A", "disclaimer": "a guardrail against mistakes, not a security boundary: the token in .env can do everything",
 "enforced": true, "enforced_verbs": ["read", "control", "edit", "create", "delete"],
 "profile": "agent", "selected_by": "active", "what_if": false, "allow_all": false,
 "default": "deny", "profiles": ["agent", "default"],
 "rules": [{"effect": "deny", "pattern": "entity:lock.*", "verbs": ["read", "write"],
            "expanded": ["read", "control", "edit", "create", "delete"], "where": null, "line": 16},
           {"effect": "allow", "pattern": "entity:light.*", "verbs": ["control"], "expanded": ["control"],
            "where": {"area": ["wohnzimmer"], "label": []}, "line": 12}]}
```

`enforced` is true and `enforced_verbs` lists the verbs commands enforce, in the order read, control, edit, create, delete — every verb while a profile is in effect. With no profile in effect nothing is checked, and they say so: `enforced` false, `enforced_verbs` `[]`. `verbs` is the list as written, `expanded` the verbs it grants or denies at its address — `write` replaced by the write verbs that apply there (Verbs), a verb written by name kept — in the order read, control, edit, create, delete (`"entity:*/registry/entity_id": [write]` expands to `["edit"]`). `profiles` is sorted. With no file: `exists` false, `profile` "", `selected_by` `no-file`, `allow_all` true, `default` `allow`, `profiles` and `rules` empty. A broken file: `valid` false, `problems` `[{"line": 7, "message": "…"}]` (the message with its "did you mean", without the `<file>:<line>:` prefix) and `"hint": "policy.yaml belongs to the owner; only the owner changes it, through `hactl policy activate` — report this, do not edit it"` — `hint` appears only when `valid` is false; `profile`, `selected_by` and `default` are "", `allow_all` is false, `profiles` and `rules` are empty.

#### `policy check <resource> <verb>`

Answers one question and **its exit code is the verdict: 0 allowed, 1 denied** — a partial verdict exits by what it resolved to (1 for a write verb, 0 for `read`). The text answer is `allowed: <resource> <verb>` or `denied: <resource> <verb>`, then the deciding rule and its line (`rule: <rule> (line <n>)`), then one `where not met: <rule> (line <n>) — <reason>` line per narrowed allow that was tried and did not hold (Selectors), then — for a partial verdict only — one `partial: <rule> (line <n>)` line per descendant deny, then the profile and how it was selected; a what-if starts with a `what-if: --as <p>` line. It contacts Home Assistant only when a `where:` selector can still change the verdict (see Selectors): a check a deny or an earlier plain allow decides never contacts it.

```json
{"resource": "entity:lock.front_door", "verb": "control", "allowed": false,
 "rule": "deny \"entity:lock.*\" [read write]", "line": 16, "profile": "agent",
 "selected_by": "active", "what_if": false, "level": "A", "enforced": true, "notes": [],
 "partial": false, "partial_denies": [], "unmet_where": []}
```

`line` is 0 when no rule decided. `enforced` says whether a command would act on this verdict: true while a profile is in effect — for `read` the text answer then ends `enforced: a read hides, masks or refuses what this verdict denies`, for a write verb `enforced: a write command refuses what this verdict denies` — and false when no profile is in effect. A `read` of `system:template` is answered as `tpl eval` is decided (D-143): while the profile denies some read, the profile default alone does not allow it (`rule` `system:template needs an explicit allow while the profile denies a read`, `line` 0). `control` on an automation, a helper, a template entity or a script itself is checked for denies only (D-107): a verdict `default: deny` decided there is not enforced (`"enforced": false`, text `not enforced: control on <kind>:<id> is checked for its denies only — …`). A script is the exception when the same default also denies its run: `script run` and a service call starting the script check `control script:<id>/run` in full, so that verdict is enforced and carries the note `a run is checked as control script:<id>/run, denied — <rule>: script run and a service call starting the script refuse it`; while the run is allowed, the script's `not enforced: …` line says `a run is decided by control script:<id>/run, which is allowed` (D-128). `notes` lists every narrowed allow whose `where:` was tried and could not be evaluated, and two notes of their own: `control on <kind>:<id> is checked for its denies only — this allow grants nothing; a service call is decided by the entity's own verdict` when an allow decided `control` on an automation, helper or template address (D-111; not a script, whose allow also grants its `run`, D-128), and `<verb> does not apply at <resource>: no command asks for it there, so this verdict decides no command` for a write verb no command asks for at or below the resource (D-110, D-128). Each note is a `note:` line in text. `unmet_where` lists every narrowed allow that was tried and whose selector did not hold, in the order tried, as `{"rule", "line", "reason"}` — `[]` when none (Selectors, D-112). A partial verdict has `"partial": true` and lists every descendant deny of step 4 in `partial_denies`, in file order (line, then column), each as `{"rule": "deny \"entity:device_tracker.*/attr/latitude\" [read]", "line": 17}`; for a write verb `allowed` is false and `rule`/`line` are those of its first entry, for `read` `allowed` is true and `rule`/`line` name what allowed it. Otherwise `partial` is false and `partial_denies` is `[]`. An invalid resource or verb, an unknown `--as` profile, or a broken file is refused: exit 1, the reason on stderr, nothing on stdout — so a denial is exit 1 **with** a verdict on stdout, and a refusal is exit 1 without one.

**`write`** (D-120) answers one verdict per write verb that applies at or below the resource (Verbs), in the order control, edit, create, delete, and **exits 0 only when every one a command enforces is allowed** — a deny-only `control` the default decided (`not enforced`) is shown denied and does not count — on an automation, a helper or a template only: a script's counts whenever the default also denies its run (D-128) — the top line and `allowed` follow the exit code (D-128). Text: `allowed: <resource> write` or `denied: <resource> write`, then per verb `<verb>: allowed|denied — <rule> (line <n>)` with that verb's `partial:`, `where not met:` and `note:` lines indented below it (and, for a deny-only `control` the default decided, its `not enforced: …` line), then `profile:` and the `enforced:` line as for one verb:

```console
$ hactl policy check entity:light.sofa write
denied: entity:light.sofa write
control: allowed — allow "entity:light.*" [control] where area=wohnzimmer (line 12)
edit: denied — default: deny
create: denied — default: deny
delete: denied — default: deny
profile: agent (selected by active)
enforced: a write command refuses what this verdict denies
```

JSON: `resource`, `"verb": "write"`, `allowed` (true only when every enforced verdict is), `profile`, `selected_by`, `what_if`, `level`, `enforced`, and `verdicts` — one object per applicable verb, each exactly the document `policy check <resource> <verb> --json` prints. A one-verb answer has no `verdicts` key. A resource at which no write verb applies is refused — exit 1, nothing on stdout, `write applies to nothing at entity:light.sofa/attr/brightness: no command asks for a write there`.

#### `policy lint`

Validates the file, then checks every rule of **every** profile, not only the effective one — a typo in an inactive profile is a hole the day the owner switches to it. Findings, each with its profile, rule and line:

| code | means |
|---|---|
| `broken` | a broken-file problem, as above; no live checks run |
| `implicit-allow-all` | profiles are declared, but no `active:` and no `default` profile puts any of them in effect |
| `redundant-allow` | an allow in a profile without `default: deny` — that profile already allows what no deny matches, so the allow and its `where:` decide nothing |
| `no-match` | the pattern's id matches nothing of its kind on the live instance, or its `attr/<name>` matches no attribute of any entity the id matches — a typo in a deny is a silent hole otherwise. A rule whose only verb is `create` is never `no-match`: it names ids that do not exist yet (D-171) |
| `unreachable` | for one of its verbs, every live match of the allow is denied by a deny of the same profile, so it can never decide |
| `verb-not-applicable` | a write verb written by name applies nowhere the pattern reaches, or a `write` applies to nothing there (Verbs, D-110): `"control" does not apply at entity:*/registry/entity_id — no command asks for it there, so the rule decides nothing for it (control on entity is checked at: entity:<id>)`, or `"write" applies to nothing at system:reload/* — no command asks for a write there, so the rule decides nothing for writes` — one finding per rule and verb |
| `no-effect` | an allow on an `automation`, `helper` or `template` address naming `control`: control there is checked for denies only, so the allow grants nothing for it (D-111) — `control on automation:<id> is checked for denies only, so this allow grants nothing for control — grant switching on the entity instead: allow "entity:automation.*": [control], and its service` |
| `blocked-by-part` | under `default: deny`, an allow whose write verb a deny of the same profile on a strict-descendant pattern always refuses from above — the verb is in both, applies at the deny's pattern (D-125: `create`/`delete` of the owner, `control` via `state`/`attr`, `edit` via a file `block`) and some command asks it between the two — wherever the patterns overlap (D-127); one finding per allow and deny: `never grants delete where deny "entity:*/registry/entity_id" [write] (line 61) covers a part: a part of it is denied, so all of it is — if the deny should only stop changes to the part itself, narrow it to [edit]` (the last clause only when such a narrower deny exists) |
| `self-grant` | under `default: deny`, a narrowed allow's `where:` that another allow of the profile lets the agent satisfy itself — an allowed edit of the matched entities' `registry/labels` or `registry/area_id`, or of their devices' `registry/area_id` (D-114): `the agent can satisfy this where: itself — allow "entity:*/registry/labels" [edit] (line 9) lets it change labels on 3 of the entities this allow matches (e.g. input_boolean.pg_x); a guardrail does not stop that — to close it, deny the field edit: deny "entity:*.pg_*/registry/labels": [edit]` |
| `half-protected` | a deny protects an automation's, script's, helper's or template's config but not the entity it owns, or the entity but not the config (D-115; see "Worked example: protect one automation") — the message names the counterpart address, resolved live |

Live checks compare the id against: `entity` — the entity_ids in states and the entity registry; `device`, `area`, `floor`, `label` — their registries; `automation` — the config ids (D-1; not the entity object id `auto ls` prints, which differs for every UI-authored automation); `script` — the ids `script ls` prints; `dashboard` — the url_paths `dash ls` prints plus the default; `service` — the services Home Assistant reports; `system` — its fixed set; `helper` — `<domain>/<id>` of every helper `helper ls` lists (the companion's YAML helpers and the storage helpers in the states); `template` — the `unique_id`s of the companion's template list; `config_entry` — the entry ids `config entries` lists (D-116). Without the companion, `helper` is checked against its storage half and listed **unchecked** with the companion's error — a match in the storage half counts as one, but no rule is reported `no-match` or `unreachable` against half a list — and `template` is unchecked with the companion's error. `file` is not enumerated (the companion lists only the YAML files it serves, not the config directory): every rule naming it is listed **unchecked** with reason `not enumerated`, its profile, rule and line (D-128) — as is every kind Home Assistant could not be read for — an unchecked kind is never reported clean. After the first transport failure (refused, timed out) lint stops asking: every Home Assistant-backed kind not yet read is listed unchecked with that same error as its reason, so a stalled host costs one `--timeout`, not one per kind.

Below the id (D-65), closed sub-resource words and registry field names need no live check (a misspelling is a broken file). Of the free segments, `attr/<name>` is checked against the attribute names in the live state of every entity the pattern's id matches — glob and case as in Matching; a pattern that stops at `attr` has nothing to check, and an id that matches no entity is one `no-match`, not two. Every other free segment — `views/<path>`, `block/<id>`, `reload/<domain>` — cannot be checked: each rule holding one is listed in `unchecked` with reason `sub-resource not checked`, whether or not its kind is enumerated, so lint is never clean without `--allow-partial` while such a rule exists. A free segment of only `*` (`*`, `**`) counts as checked — nothing in it can be misspelt — so it is not listed, and `attr/*` is not looked up; a segment with any literal character (a `?` too) is unchecked, or for `attr/<name>` checked live.

**`where:` values** (D-113) are checked too: each `area` value against the area registry and each `label` value against the label registry, by id or by name, glob and case as in Matching. A value that matches nothing is `no-match` with the narrowed allow's profile and rule and the `where:` entry's line — `where: label "pg_core_lable" matches no label on this instance (by id or name)` — since a misspelt selector silently denies everything the allow was meant to grant. A value of only `*` counts as checked. A registry lint could not read lists its kind (`area`, `label`) as **unchecked** once, like any other kind.

**`self-grant`** asks the profile itself, with the live registries, whether the edit that satisfies the selector is allowed and by which allow — so a deny on the field, or a granting allow narrowed by its own `where:` that does not hold, is no finding. Only entities in the entity registry count (nothing else has labels or an area to change). When the registries cannot be read the narrowed allow is listed unchecked with reason `self-grant not resolved`.

**`half-protected`** resolves each typed entry's entity live, as the service-call check does (an automation's entity by its `id` attribute, a script's, helper's or template's by the entity registry): a deny on `automation:<id>`, `script:<id>`, `helper:<domain>/<id>` or `template:<id>` (or its `config`) holding `edit` or `delete` whose entity still allows `edit` on a registry field — the first one still editable is named, in the order `name`, `area_id`, `labels`, `icon`, `disabled_by`, `entity_id` — and a deny on `entity:<e>` holding `edit` whose owner still allows `edit <kind>:<id>/config` or `delete <kind>:<id>` (a helper: `delete` only; a deny on any one form of its id protects it). One finding per rule, naming the first counterpart (by id, then entity) and the others in a closing `; the same holds for <x>[, <y>, <z>][ and <n> more]` (D-128): `deny "automation:pg_v9_auto_1" [write] protects the automation's config, but its entity automation.pg_v9_verify_auto can still be changed (edit entity:automation.pg_v9_verify_auto/registry/name is allowed) — to protect both, also deny "entity:automation.pg_v9_verify_auto": [write]`. When the registry cannot be read the rule is listed unchecked with reason `counterpart not resolved` (unless its kind is listed unchecked already).

Exit 0 when there is no file, or the file is valid, nothing was found and nothing was unchecked; exit 1 otherwise. `--allow-partial` accepts unchecked entries (they are still listed) — it never hides a finding.

For `unreachable`, an allow's sub-resource path is a set, not a literal: the allow is unreachable for a verb only when, for each live match, one deny of the same profile covers its whole sub path — the deny's sub path is no longer than the allow's, each literal allow segment matches the deny's segment, and each wildcard allow segment faces exactly `*` in the deny (or the deny stops above it). A deny on `entity:lock.*/attr/?` leaves `attr/battery` to an allow on `entity:lock.*/attr/*`, so that allow is reachable.

```json
{"file": "/home/u/ha/home/policy.yaml", "exists": true, "valid": true, "level": "A",
 "findings": [{"code": "no-match", "profile": "agent", "rule": "deny \"entity:lokc.*\" [read write]",
               "line": 16, "message": "matches no entity on this instance"}],
 "unchecked": [{"kind": "dashboard", "reason": "sub-resource not checked", "profile": "agent",
                "rule": "deny \"dashboard:main/views/secret\" [read]", "line": 19},
               {"kind": "file", "reason": "not enumerated", "profile": "agent",
                "rule": "deny \"file:**\" [write]", "line": 20}]}
```

Findings are ordered by line, then code, profile and rule. `unchecked` follows the order of the kinds table; within a kind the kind's own entry (`profile`/`rule` "", `line` 0 — a kind Home Assistant could not be read for) comes first, then one entry per rule in file order (line, then column). Both are `[]` when empty. In prose, a broken file's report is its `file:` line, one `policy_broken: <file>:<line>: <message>` line per problem and the closing owner line (Policy file format); under `--json` its findings have code `broken`, and the document adds `"hint": "policy.yaml belongs to the owner; only the owner changes it, through `hactl policy activate` — report this, do not edit it"` (only when `valid` is false).

#### Setting up an agent: draft, review, activate

**An agent may draft a policy; only the owner activates one** (D-148). The flow:

1. **The owner creates the agent's instance**, in a terminal: `hactl agent add <name>` (see "Several agents" below). The new agent starts **read-only**: its `policy.yaml` holds one profile that denies every write (D-150), so it can look at everything it needs to propose rules and change nothing.
2. **The agent drafts.** `hactl rtfm --skill policy-setup` prints the procedure for an agent: inventory the instance, ask the owner in plain language what it may switch, edit and see and what never, write `policy.draft.yaml` beside `policy.yaml` with `default: deny`, check it with `hactl policy lint --file policy.draft.yaml`, and hand over. The agent never writes `policy.yaml`.
3. **The owner reviews**: `hactl policy review --file policy.draft.yaml` says in counts and names what the draft's active profile would let the agent do on *this* instance — which entities it may switch and see, which automations, scripts and dashboards it may edit, create or delete, which services it may call, what it can never touch, whether what written automations do is checked (D-135), and every lint finding.
4. **The owner activates**: `hactl policy activate policy.draft.yaml` — **in a terminal of its own** (D-149). It refuses unless stdin and stdout are a terminal, so neither an agent nor Claude Code's `!` prefix can run it (both have none, M-5); over MCP it is never offered. It lints the draft (a broken draft is refused), prints the review, asks for the profile name as confirmation, then writes `policy.yaml` atomically and keeps the previous one as `policy.yaml.prev`.

`policy show`, `policy check` and `policy lint` take `--file <path>` to read a draft instead of `policy.yaml`; the answer says so (`file: … (draft, not in effect)`).

**`--file <draft>`** (D-161). A relative path is resolved against the instance directory (beside `.env`), never the working directory — so `--file policy.draft.yaml` names the instance's draft from anywhere; an absolute path is read as written. `policy activate <draft>` resolves its argument the same way. The answer is the draft's: its profile, its verdict, its lint. A draft is never in effect, so `policy show --file` prints `enforced: nothing — a draft is not in effect; hactl policy activate puts it in effect`, `policy check --file` starts with the same `file: <path> (draft, not in effect)` line and ends `not enforced: a draft is not in effect` (its exit code is still the verdict; without `--file` it has no `file:` line), and under `--json` every one of the three carries `"draft": true` (`false` without `--file`), `show` with `"enforced": false` and `"enforced_verbs": []`. A draft that does not exist is an error (exit 1, `no draft at <path>`), never `no policy file`. `--as <p>` works on a draft as on the file. `--file` changes only what is read: the command still runs under the instance's own policy, so a broken `policy.yaml` still refuses `policy check --file`.

#### `policy review [--file <draft>]`

What a profile lets the agent do **on this instance**, for the owner, in counts and names (D-162): `policy.yaml`, or the draft `--file` names. It reads Home Assistant (the same inventory `policy lint` uses) and changes nothing. Sections, in this order:

- the `review:` line — the file, and `(draft, not in effect)` for a draft; the `profile:` line — the profile in effect and how it was selected, and its default; the `indirect:` line — `checked` or `unchecked (line <n>)` (D-135);
- `entities (<n>)`: `see`, `switch` (control) and `edit` (rename, area, labels — the registry) — each `<a> of <n>` and what it reaches, by name (`switch: 2 of 212 — light.hall, light.kitchen`; see the naming rule below);
- `automations`, `scripts`, `dashboards` (`<n>` each): `edit`, `delete`, for scripts `run` (control on `script:<id>/run`) — `<a> of <n>` and the ids; and `create`: `any id`, `only what <allow rule> (line <n>)[, …] allows`, or `no`;
- `services (<n>)`: `call` — `<a> of <n>` and the services, by name;
- one `never:` line per deny rule of the profile, with what it matches here: `never: deny "entity:lock.*" [read write] (line 14): 2 entities — lock.back_door, lock.front_door` (`never: no deny rules` without one); a pattern on a part (`entity:*/registry/entity_id`) counts the ids whose part it denies;
- `lint` — the profile's file linted exactly as `policy lint` would, its findings and unchecked kinds, or `clean`.

**Who asks decides what it counts** (D-172). At a terminal — stdin and stdout both one, as for `activate` — the review is the owner's: it runs without the instance's policy session and counts the real instance, whatever the instance's own profile hides. Without one — an agent's shell, an MCP server, a script — it runs under the session like every other read: it counts and names only what the profile in effect lets it read, and the `hidden by policy: N (profile p)` trailer (on stderr: the review is a document) says how much it left out. An agent can review its own draft; it learns nothing its profile hides.

A grant line names the shorter list: every id it reaches, or `all but <ids>` when fewer are left out (`see: 210 of 212 — all but lock.back_door, lock.front_door`), `all` when nothing is, and no names when it reaches nothing. A list shows at most 10 ids, then `…, +<k> more`; `--json` lists every allowed one. A kind Home Assistant could not be read for says `not checked: <reason>` instead of a count. With no profile in effect it says `ALLOW-ALL` (no file, or no `active:` and no `default` profile) and lists nothing. **The whole review is printed whatever `--tokensmax` says** (a document). Exit 0 whatever it found — findings are for the owner to read, not a verdict; 1 for a broken file (reported as `policy show` reports one), a draft that does not exist, or Home Assistant not reachable at all.

```json
{"file": "/home/u/ha/agents/kitchen/policy.draft.yaml", "draft": true, "profile": "agent", "selected_by": "active",
 "allow_all": false, "default": "deny", "indirect": "checked", "enforced_verbs": ["control", "edit", "create", "delete"],
 "grants": [{"kind": "entity", "verb": "control", "sub": "", "label": "switch", "allowed": 34, "total": 212,
             "ids": ["light.kitchen", "…"], "unchecked": ""}],
 "create": [{"kind": "automation", "any": false, "rules": [{"rule": "allow \"automation:pg_*\" [read edit create delete]", "line": 9}]}],
 "never": [{"rule": "deny \"entity:lock.*\" [read write]", "line": 14, "kind": "entity", "matched": 2,
            "ids": ["lock.back_door", "lock.front_door"], "unchecked": ""}],
 "lint": {"findings": [], "unchecked": []}}
```

`grants` holds, in the order above, `entity` `read`/`control`/`edit`, `automation` `edit`/`delete`, `script` `edit`/`delete` and `control` with `"sub": "run"`, `dashboard` `edit`/`delete`, `service` `control`; `label` is the word the text uses; `ids` are the allowed ones, sorted; `allowed`/`total` are 0 and `unchecked` names the reason when the kind could not be read. `never[].kind` is `*` for the bare pattern `*`, which matches everything (`matched` 0, `ids` []). `lint` is the `findings`/`unchecked` pair of `policy lint --json`. With no profile in effect `allow_all` is true and `grants`, `create` and `never` are [].

#### `policy activate <draft>`

The owner's command — the only one that puts a policy in effect (D-148, D-149, D-163). In order:

1. **A terminal, or nothing.** stdin and stdout must both be a terminal; otherwise it exits 1 before reading anything: `policy activate needs a terminal: only the owner activates a policy — run it yourself in a terminal of your own (an agent's shell and Claude Code's ! prefix have none)` and the line ``  hint: ask the owner; a change goes through a draft and `hactl policy activate` ``. Over MCP it is never offered: `command "hactl policy activate" is not available over MCP: only the owner runs it, in a terminal of their own — ask the owner; …`, and the same for `agent add`.
2. **The draft is read once.** Its bytes are what is reviewed and exactly what is written — a draft changed after the review is not what lands. Refused before any prompt, with nothing written: a draft that does not exist; a broken draft (exit 1, its problems on stderr as `policy_broken: <draft>:<line>: <message>` lines and the closing owner line); a draft that puts no profile in effect (no `active:` and no `default` profile — there is no profile name to confirm, and an agent's instance must not be allow-all; to run without a policy, the owner removes `policy.yaml` by hand); the draft path being `policy.yaml` itself; a `policy.yml` in the instance directory (it would break the activated file).
3. **The review** of the draft (as `policy review --file`; when Home Assistant cannot be reached there is no review and nothing is written), and what it replaces: `replaces: <dir>/policy.yaml (profile <p>)`, `(broken)`, or `nothing (no policy.yaml yet)`.
4. **The confirmation**: `type the profile name to activate it (<p>), anything else cancels:`. Anything but the exact name — an empty line, end of input — cancels: `activate: cancelled — nothing was written`, exit 1.
5. **The write**: the draft's bytes, unchanged — never re-serialised, so comments and layout survive. The previous `policy.yaml`, if any, is copied to `policy.yaml.prev` (replacing an older one; with no previous file no `.prev` is made), then the bytes are written to a temporary file in the instance directory, synced to disk and renamed over `policy.yaml` — a reader sees the old file or the new one, never half of either, and no temporary file is left behind, on success or failure. It prints `activated: <dir>/policy.yaml — profile <p> is in effect from the next command` and, when there was one, `previous: <dir>/policy.yaml.prev`. The draft stays where it was. Exit 0.

`policy activate` and `agent add` run without the instance's policy session: they are the owner's, behind their terminal gate, so a broken or missing `policy.yaml` — which refuses every other command — can still be replaced (D-163). With no session there is nothing to filter: they are not write commands in D-82's sense, whose own reads a session exempts — they have no session at all (D-172). `policy review` at a terminal runs the same way.

#### Several agents

**One instance directory per agent** (D-151): its own `.env` with its own token, its own `policy.yaml`, its own audit log. The file still picks the profile (D-62), and an MCP server is pinned to one directory (D-64), so each agent gets exactly its own rules.

```console
$ hactl agent add kitchen-helper        # in a terminal, as the owner
created ~/.hactl/agents/kitchen-helper  (from ~/.hactl/default)
  token:   a long-lived token of its own, "hactl agent kitchen-helper"
  policy:  read-only until you activate one (HACTL_POLICY=required)
  next:    give the agent `hactl rtfm --skill policy-setup`, then review and activate its draft
  mcp:     claude mcp add kitchen-helper -- hactl mcp --dir ~/.hactl/agents/kitchen-helper --allow-writes

$ hactl agent ls
NAME            DIR                              PROFILE    REQUIRED  LAST WRITE CHECK
kitchen-helper  ~/.hactl/agents/kitchen-helper   readonly   yes       never
```

- `agent add <name>` copies `HA_URL` and `COMPANION_URL` from the source instance (`--from <dir>`, default: the instance hactl would use), writes `HACTL_POLICY=required` (D-136), and gives the agent **a token of its own** (M-6), so Home Assistant's own records tell the agents apart. It asks Home Assistant for one; where that is not possible it asks for one to be pasted (`--token -`). `--path` chooses the directory. Like `activate`, it needs a terminal.
- `agent ls` lists the agent directories under `~/.hactl/agents/` (or `--root`).
- **Writes from several agents to one Home Assistant never interleave** (D-152): the write lock is held per Home Assistant (keyed by its URL), not per directory.

**`agent add` in detail** (D-160, D-164). `<name>` is lowercase letters, digits, `-` and `_`, starting with a letter or digit, at most 64 characters. The directory is `--path`, or `~/.hactl/agents/<name>`; one that exists at all — a file, an empty directory, a symlink — is refused (exit 1, nothing changed, no token made). The source instance must load (`HA_URL` and `HA_TOKEN`). The token is a long-lived access token of the source token's own Home Assistant user, created over Home Assistant's WebSocket (`auth/long_lived_access_token`, which Home Assistant accepts on a connection authenticated with a long-lived token — oracle, D-160), named `hactl agent <name>`, valid 3650 days — it appears under that name in the user's Security → Long-lived access tokens, where the owner revokes it. `--token -` reads one from the terminal instead (not echoed); `--token` takes no other value, so a token never lands in shell history (D-170). Home Assistant refuses the minting request to every command run under a policy (D-169). Either token is checked against Home Assistant (`GET /api/`) before anything is written; if Home Assistant refuses to create one, nothing is written and the error says to create one by hand and run again with `--token -`. Then it writes, in the new directory (mode 0700): `.env` (mode 0600) with `HA_URL`, `HA_TOKEN`, `COMPANION_URL` and `TZ` when the source has them, and `HACTL_POLICY=required`; and the starter `policy.yaml`:

```yaml
# Written by `hactl agent add`: read-only until the owner activates
# this agent's own policy with `hactl policy activate`.
version: 1
active: readonly
profiles:
  readonly:
    default: deny
    allow:
      "*": [read]
```

If writing fails after the token was made, the error names the token so the owner can revoke it. `agent add` and `policy activate` are the only commands that write `policy.yaml`.

**`agent ls [--root <dir>]`** (D-167) lists every directory directly under the root that holds a `.env`, sorted by name, and reads only local files. `PROFILE` is the profile in effect, `none` (no `policy.yaml`), `allow-all` (a file that selects no profile) or `broken`; `REQUIRED` is `yes` with `HACTL_POLICY=required`; `LAST WRITE CHECK` is the last line of the agent's audit log — `<time> allowed|denied <verb> <resource>` — or `never`. A root that does not exist lists nothing (`no agents under <root>`), exit 0. `--json`: `[{"name", "dir", "profile", "selected_by", "required", "policy": "ok|missing|broken", "last_write_check": <audit line object or null>}]`.

#### Worked example: protect one automation

An automation has two addresses: its config, `automation:<config id>` (what `auto apply`, `auto delete` and switching it check), and its entity, `entity:automation.<object id>` (what `ent rename`, `ent set-label`, `ent set-area` and a registry delete check). For an automation made in the UI the two ids differ. Find both in one listing — `id` is the entity's object id, `config_id` the config id:

```console
$ hactl auto ls --pattern pg_v9 --json
[{"id": "pg_v9_verify_auto", "config_id": "pg_v9_auto_1", "state": "on", …}]
```

Then deny both:

```yaml
    deny:
      "automation:pg_v9_auto_1":               [write]   # its config: edit, delete, switching it
      "entity:automation.pg_v9_verify_auto":   [write]   # its entity: rename, relabel, move, disable, delete
```

With only one of the two, `policy lint` reports `half-protected` and names the missing address. The same holds for a script (`script:<id>` and `entity:script.<…>`), a helper (`helper:<domain>/<id>` and `entity:<domain>.<…>`) and a template entity (`template:<unique_id>` and its entity). A deny cannot be narrowed with `where:` and an allow cannot exclude: to keep one domain out of a wildcard allow such as `entity:*.pg_*`, list the domains you mean (`entity:light.pg_*`, `entity:switch.pg_*`, …).

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

- **Token cap & estimate:** output is truncated at `--tokensmax` tokens (default 500, `0` = off) with a hint naming filters that shrink it; prefer filters to raising the cap. `--full` removes it too, unless you pass `--tokensmax` yourself. `--tokens` prints a `[~N tok]` estimate (stderr under `--json`). **Documents are never capped** (a cut leaves them unparseable or incomplete): `--json`, `dash show --raw|--yaml|--view`, `<family> cat`, `config file|block`, `policy show`, `completion`, `--help`.
- **Tables:** one header line, one row per item; `…+N more` for overflow, capped by `--top`.
- **Stable IDs:** `trc:a7` (`auto`/`script show`), `log:f2` (`log` incl. `--unique`, `cc logs`) — kept in `cache/ids.json` until `cache clear`; `ent anomalies` mints none.
- **Timestamps:** short form in your zone (`09:42` today, `04-16 09:42` otherwise); `--full` does **not** make them ISO. **`--json` always gives full ISO8601 with your offset**, table listings included. Boolean columns are JSON booleans (`"admin": true`); numeric cells stay strings (`"runs_24h": "0"`) — parse them, never test them for truthiness.
- **No decoration:** no emojis, no color.
- **JSON mode:** `--json` extracts fields; filter first on large datasets. **Commands `--json` does not reach:** `auto|script|helper|tpl cat`, `auto|script diff`, `config file|block`, `rtfm` — each prints one document verbatim; every other command honours it. Previews return `{"dry_run":true,"action","details","hint"}`, a confirmed write `{"dry_run":false,"ok":true,"action","details"}` (+`"warnings"`) — read `dry_run`, not your flags.
- **Bad input is refused, not absorbed** (exit 1, stderr, empty stdout): a blank identifier (an empty string is never a wildcard), an argument a command does not take (`ent ls sensor` → `--domain sensor`), an unknown subcommand in any family, a flag the command does not take (the error names the ones that do), a value outside a flag's range (`--top -1`, `--timeout 0s`), or two inputs naming one thing (`dash show --raw --yaml`). Nothing mistyped ever exits 0 with help.
- **Dry runs resolve their target** and parse the `-f` file before printing a plan: a preview fails exactly where `--confirm` would, so a misspelled id is an error, not a plan. A `--confirm` is refused non-interactively (how-to on stderr, exit 1) unless the **same command and target** ran without it in the last 30 min — dry-run, then repeat.
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
- `hactl rtfm --core` / `--family <name>` / `--families` fetch subsets on demand; `--reference <name>` fetches a family's reference section — today only `policy` has one: its how-to travels with the family, the full contract (`### Policy reference`) only on request; `--skill <name>` prints a skill — a step-by-step procedure for an agent, shipped in the binary like this manual (today only `policy-setup`, D-165); it is printed verbatim and uncapped, alone (it combines with no other `rtfm` flag), and an unknown name is refused with the list
- Humans at a terminal never see it; `rtfm`, `mcp`, `setup`, `version`, `help`, `completion` never trigger it
- **Delivery is decided by who is listening, never by the shape of the answer.** `--json` is a promise about stdout and the manual has never been on stdout, so `health --json` and `device ls --json` deliver on stderr exactly as they do without the flag — an agent that reads only structured output is the caller this manual is written for, and it used to be the one caller that never received it

---

## MCP server

`hactl mcp` serves this CLI over the Model Context Protocol on stdio. MCP clients see a single `hactl` tool that takes a command line (without the binary name), e.g. `{"command": "ent ls --domain light"}`. All commands and flags in this manual work unchanged; this manual is also available as the MCP resource `hactl://manual`. Over MCP the full manual arrives once with the first tool result — the progressive stderr delivery above applies to plain CLI usage only.

```bash
claude mcp add hactl -- hactl mcp --dir ~/.hactl/default
```

- Read-only by default: mutating commands (`svc call`, `auto apply`, `script apply`, create/delete, `script run`, …) are rejected with an error. Start the server with `hactl mcp --allow-writes` to permit them; the dry-run + `--confirm` write path still applies.
- `--allow-writes` comes first, the policy second: a read-only server refuses a write command before `policy.yaml` is consulted; on a server with `--allow-writes`, the instance's policy then decides each write exactly as on the CLI (see "Policy" — Enforcement). The policy is read on every call, so an edit to `policy.yaml` applies from the next call on; a broken file refuses every call except `policy show` and `policy lint`.
- One instance per server process. A `--dir` given at server start pins every call to that instance — and with it to that instance's `policy.yaml` (D-64). A call whose command line carries `--dir` (or `--dir=`) naming another directory — anywhere on the line, after a `--` too, and an empty `--dir=` included — is refused with an error and runs nothing; naming the pinned directory itself is accepted. Directories are compared as absolute, cleaned paths, symlinks not followed. `HACTL_DIR` in the server's environment never moves a pinned server either. A server started without `--dir` is not pinned: each call finds its instance as the CLI does.
- `setup`, `completion`, `policy activate`, `agent add` and `mcp` itself are never available over MCP; unclassified commands fail closed. `policy activate` and `agent add` are the owner's (D-149): the refusal says so and ends with ``ask the owner; a change goes through a draft and `hactl policy activate` ``.
- A malformed message costs that message, never the session. A line that is not a valid JSON-RPC request is answered with a JSON-RPC error when its `id` can be read, and dropped (with a note on stderr) when it cannot — a bad line from a client does not end the server. Lines above 4 MiB are dropped. The session ends when the client closes the stream, and then `hactl mcp` exits 0.
