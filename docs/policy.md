# Permission policy

A `policy.yaml` in your instance directory tells hactl what an agent may read
and change, per entity, automation, dashboard and so on. For example: "read
everything except locks, switch lights only in the living room, edit only the
`pg_*` automations, change labels but never names".

> **A guardrail, not a lock.** The policy catches *mistakes*: a misread
> instruction, a target wider than intended, a typo in an id. It does not stop
> anything that wants to get around it. The token in `.env` can do everything,
> and anything with a shell can call Home Assistant directly or edit the file.

> **Status: writes are enforced, reads are not yet.** Every command that
> changes something refuses a denied change, already in its dry run, before
> anything is written. Reads are not filtered yet: a `read` rule is what
> `policy check` answers, but no listing hides a row and no attribute is masked
> in this version. See [What hactl enforces](#what-hactl-enforces).

## Quick start

Create `policy.yaml` next to your instance's `.env`:

```yaml
version: 1
active: agent                 # the profile hactl applies, for CLI and MCP alike
profiles:
  agent:
    default: deny             # whatever no rule allows is denied
    allow:
      "entity:*":                 [read]
      "entity:light.*":           [control]
      "automation:pg_*":          [read, edit, create]
      "entity:*/registry/labels": [edit]
    deny:
      "entity:lock.*":                         [read, write]
      "entity:device_tracker.*/attr/latitude": [read]
```

Then ask it questions:

```console
$ hactl policy check entity:lock.front_door control
denied: entity:lock.front_door control
rule: deny "entity:lock.*" [read write] (line 12)
profile: agent (selected by active)

$ hactl policy check entity:light.sofa control
allowed: entity:light.sofa control
rule: allow "entity:light.*" [control] (line 8)
profile: agent (selected by active)
```

The exit code is the answer: `0` allowed, `1` denied. Every answer names the
rule that decided it. To ask about every kind of change at once, use `write`:

```console
$ hactl policy check entity:light.sofa write
denied: entity:light.sofa write
control: allowed — allow "entity:light.*" [control] (line 8)
edit: denied — default: deny
create: denied — default: deny
delete: denied — default: deny
profile: agent (selected by active)
enforced: a write command refuses what this verdict denies
```

It answers each write verb that can apply to that resource and exits `0` only
if all of them are allowed. One exception: `control` on an automation, script,
helper or template address is shown, but a denial there by `default: deny`
alone does not count, because no command enforces it (see [Switching typed
entries](#switching-typed-entries)).

Finally, run `hactl policy lint` against your live instance to catch typos (see
[Checking a policy](#checking-a-policy)).

With no `policy.yaml`, nothing changes: everything is allowed, exactly as
without the feature, and hactl makes no extra request to check anything.

## Which profile applies

A file can hold several profiles. **The file alone decides which one applies.**
There is no `--profile` flag and no environment variable, so an agent cannot
pick a friendlier profile for itself. You switch profiles by editing `active:`.

| The file has | In effect |
|---|---|
| `active: agent` | profile `agent` |
| no `active:`, but a profile named `default` | profile `default` |
| no `active:` and no `default` profile | nothing: everything is allowed, and `policy show` says `ALLOW-ALL` |
| no file at all | nothing: everything is allowed |

To see what *another* profile would decide without switching, use
`--as <profile>` on `policy show` or `policy check`. The output is marked
`what-if`, and the effective profile does not change.

An MCP server started with `hactl mcp --dir <instance>` sticks to that
instance. A tool call that tries another `--dir` is refused, so it cannot reach
a different instance's policy, or an instance with none.

## Writing rules

A rule is `"<resource pattern>": [verbs]` under `allow:` or `deny:`.

### Resources

`<kind>:<id>`, optionally followed by a sub-resource path:

| kind | id | examples |
|---|---|---|
| `entity` | entity_id | `entity:light.kitchen`, `entity:light.kitchen/attr/brightness`, `entity:*/registry/labels` |
| `device` | device_id | `device:abc123/registry/area_id` |
| `area`, `floor`, `label` | registry id | `area:wohnzimmer` |
| `automation` | config id (not the alias) | `automation:pg_*` |
| `script` | object id | `script:goodnight/run` |
| `template` | unique_id | `template:solar_total` |
| `helper` | `<domain>/<id>` | `helper:input_boolean/guest_mode` |
| `dashboard` | url_path (`dashboard:` alone is the default dashboard) | `dashboard:main/views/secret` |
| `config_entry` | entry_id | `config_entry:01J…/options` |
| `file` | path in the config directory | `file:packages/**`, `file:automations.yaml/block/alarm` |
| `service` | `<domain>.<service>` | `service:lock.unlock` |
| `system` | `logs`, `wireguard`, `reload`, `check_config`, `cache` | `system:reload/automation` |

Entity sub-resources are `state`, `attr/<name>`, `history`, `stats` and
`registry/<field>`, where field is one of `name`, `area_id`, `labels`, `icon`,
`disabled_by` or `entity_id`. The full table is in the
[manual](manual.md#resources).

The pattern `"*"` on its own matches every resource of every kind:
`"*": [read]` lets the agent read everything, `"*": [write]` under `deny:`
makes a read-only profile.

A `template:` is a template entity from YAML, addressed by its `unique_id`. A
template helper made in the UI (Settings → Helpers) is a config entry, not a
`template:` — address its entity (`entity:sensor.x`) or its config entry
(`config_entry:<entry_id>`).

### Verbs

| verb | means | e.g. |
|---|---|---|
| `read` | see it | `ent show`, `dash show` |
| `control` | change runtime state | `svc call`, `script run` |
| `edit` | change existing config or a registry field | `auto apply`, `ent set-label` |
| `create` | make something new | `auto create`, `area create` |
| `delete` | remove it | `auto delete` |

Helpers have no edit command: `helper create` and `helper delete` are all
hactl does to a helper's config, so `edit` on `helper:` decides nothing.
Switching a helper (`input_boolean.turn_on`) is `control` on its entity
(`entity:input_boolean.guest_mode`) and on the service.

`write` is shorthand for `control, edit, create, delete` — **as far as they
can change what the rule points at.** Nothing ever "switches" a registry
field, so `"entity:*/registry/entity_id": [write]` never covers `control`,
while `"entity:lock.*": [write]` covers everything you can do to a lock.

A deny on a part also stops whatever would change that part from above:
deleting or creating the whole thing, a service call on an entity (it changes
the entity's `state` and `attr/…`), and an edit of a file (it rewrites the
file's `block/…`). So `deny "automation:x/config": [write]` also refuses
deleting automation `x`, and a deny on `entity:lock.*/state` refuses switching
a lock. An allow on a part grants only the part.

**The no-renames rule is `deny "entity:*/registry/entity_id": [edit]`.**
With `[write]` it also refuses deleting and creating every entity, because
deleting an entity deletes its entity_id: `auto delete` and `helper delete`
are then refused, whatever you allow. `policy lint` reports each allow such a
deny overrides as `blocked-by-part`.

`hactl policy show` prints what each rule ends up meaning (after a `write`,
`→ [edit create delete]`; `expanded` in `--json`), and the manual's reference
has the full table of which verbs apply where. If you write a verb that can change nothing where you put it
(`"entity:*/registry/labels": [control]`), the file still loads, and
`policy lint` tells you (`verb-not-applicable`).

### Matching

- `*` matches any run of characters and `?` exactly one character, but neither
  crosses a `/`. Only `file` ids also accept `**`, which matches across folders
  (`file:packages/**`).
- Matching ignores case.
- **A pattern matches the full id.** `entity:sensor.*` does not match
  `binary_sensor.x`. To match a name in every domain, make the domain a
  wildcard: `entity:*.front_door`. A bare `entity:front_door` is rejected,
  because it could never match anything.
- **A pattern without a wildcard is exact.** `entity:light.kitchen` does not
  match `light.kitchen_2`.

### How a decision is made

For one resource and one verb:

1. A matching **deny** wins. Deny always beats allow, wherever the rules are in
   the file.
2. Otherwise a matching **allow** grants.
3. Otherwise the profile's **`default:`** decides. Only `default: deny` denies.
   Without it, everything not explicitly denied is allowed.

A rule covers everything below it: `read` on `entity:light.kitchen` covers its
attributes. An allow on a sub-resource grants only that sub-resource:
`"entity:*/registry/labels": [edit]` lets the agent change labels and nothing
else about the entity.

**Partly denied.** Suppose an entity is allowed, but something *inside* it is
denied, like the latitude attribute in the example above. Then:

- **changing** the whole entity is denied, because it would touch the denied
  part — counting only denies whose verb can change what they protect, so a
  deny on a registry field never blocks switching the entity;
- **reading** it is allowed, and the answer lists the denied part:

```console
$ hactl policy check entity:device_tracker.jan read
allowed: entity:device_tracker.jan read
rule: allow "entity:*" [read] (line 7)
partial: deny "entity:device_tracker.*/attr/latitude" [read] (line 13)
```

### Narrowing an allow by area or label

```yaml
    where:
      "entity:light.*": { area: wohnzimmer }
```

The allow on `entity:light.*` then applies only to lights in the living room.
`where:` works on `entity` and `device` allows. It accepts `area`, `label` or
both, each with one value or a list; a value may be an id or a name and may
contain wildcards. An entity's area is its own, or else its device's. Labels
are not inherited from the device.

The check runs against the live instance every time. If you move a lamp into
the living room, the allow applies to it at once. If Home Assistant cannot be
reached, the narrowed allow does not apply.

When a narrowed allow matched but its area or label did not hold, a denial says
so instead of just `default: deny`:

```console
$ hactl policy check entity:input_boolean.pg_guest control
denied: entity:input_boolean.pg_guest control
rule: default: deny
where not met: allow "entity:*.pg_*" [control] where label=pg_core_label (line 9) — input_boolean.pg_guest has no label pg_core_label
profile: agent (selected by active)
```

Two things to watch:

- **A misspelt area or label denies everything the allow was meant to grant.**
  `policy lint` looks every `where:` value up in your areas and labels and
  reports one that matches nothing.
- **Don't let the agent satisfy its own `where:`.** If the same profile lets it
  edit labels (`"entity:*/registry/labels": [edit]`) on the entities a
  `where: {label: …}` allow covers, it can add the label and then use the
  allow. The same goes for areas, including a device's area. `policy lint`
  reports this as `self-grant` and names both rules. The policy is a guardrail
  and does not stop it; close it by denying that field edit on those entities.

`where:` cannot exclude anything: there is no "everything except". To keep one
domain out of a wildcard allow such as `entity:*.pg_*`, list the domains you
mean (`entity:light.pg_*`, `entity:switch.pg_*`, …).

### Protecting one automation

An automation has two addresses: its config (`automation:<config id>`, what
`auto apply`, `auto delete` and switching it check) and its entity
(`entity:automation.<object id>`, what renaming, relabelling, moving and
disabling it check). For an automation made in the UI the two ids differ.
`hactl auto ls --json` shows both: `id` is the entity's object id, `config_id`
the config id.

```yaml
    deny:
      "automation:pg_v9_auto_1":               [write]   # its config
      "entity:automation.pg_v9_verify_auto":   [write]   # its entity
```

With only one of the two, `policy lint` warns (`half-protected`) and names the
address you are missing. Scripts, helpers and template entities work the same
way.

### Protecting one dashboard view

`dash save` checks each view it changes: editing view `secret` of dashboard
`main` is `edit dashboard:main/views/secret`, adding it `create`, removing it
`delete`. A change outside the views, such as the title, is checked on the
dashboard as a whole, so any view deny refuses it. Choose the verbs by what
you want to keep:

- `deny "dashboard:main/views/secret": [edit]` stops changes to the view, but
  not removing it (or adding it back) when the profile allows `create` or
  `delete` on `dashboard:main` — those grant the views too.
- `deny "dashboard:main/views/secret": [write]` stops that as well, but a deny
  that holds `create` and `delete` on a view also holds them on the dashboard
  (deleting the dashboard deletes the view), so `main` can then be neither
  deleted nor created. `policy lint` says so (`blocked-by-part`).

The first `dash save` on a dashboard nothing is stored for yet (right after
`dash create`) creates the views it brings, so `create` on those views is what
it needs; anything else in it, such as a title, needs `edit` on the dashboard.
A first save that brings no view at all (`{}`, an empty `views` list) still
stores a dashboard where none was — on the auto-generated default dashboard it
replaces what users see — so it needs `edit` on the dashboard too.

### Switching typed entries

**`control` on an automation, script, helper or template address itself is
only ever denied, never granted.** `allow "automation:pg_*": [control]` lets
nothing be switched: switching is allowed on the entity
(`entity:automation.pg_*`) and the service. `policy lint` reports such an allow
as `no-effect` (a `script:<id>` allow is the exception: it also grants the
script's `run`), and `policy check` adds a note when an allow decided `control`
on one of these addresses.

## Checking a policy

| Command | What it tells you |
|---|---|
| `hactl policy show` | the file, the profile in effect and why, its rules in order, and a reminder that this is a guardrail |
| `hactl policy check <resource> <verb>` | allowed or denied, and which rule decided (exit 0 or 1) |
| `hactl policy lint` | the file is valid, **and** every rule matches something real on your instance |

**Lint before you rely on a deny.** A deny with a typo (`entity:lokc.*`)
matches nothing, so it protects nothing. `policy lint` checks the rules of
*every* profile, not only the active one, against your live instance. It
reports:

- `no-match`: the rule matches nothing on the instance, which usually means a
  typo;
- `unreachable`: an allow that a deny always overrides;
- `redundant-allow`: an allow in a profile without `default: deny`, where it
  changes nothing;
- `implicit-allow-all`: profiles exist, but none is in effect;
- `verb-not-applicable`: a verb that cannot apply where the rule points;
- `no-effect`: an allow of `control` on an automation, helper or template
  address, which never grants anything;
- `blocked-by-part`: an allow whose `create` or `delete` (or another verb) a
  deny on a part always refuses — such as `allow "entity:automation.pg_*":
  [delete]` with `deny "entity:*/registry/entity_id": [write]`; the message
  names both rules and, where it helps, the narrower deny (`[edit]`). A
  deliberate exception ("every light except one") is written as a deny on
  the entity itself, `deny "entity:light.x": [control]`, not on its `state`,
  and draws no finding;
- `self-grant`: a `where:` allow the agent could satisfy itself;
- `half-protected`: a deny on an automation's config without its entity, or
  the other way round.

Lint checks ids against your instance: entities, devices, areas, floors,
labels, automations, scripts, dashboards, services, helpers (YAML and the ones
made in the UI), template entities and config entries, and every `where:`
value against your areas and labels. Some things it cannot check: file paths,
dashboard view paths and file blocks. It lists them as **unchecked** and exits
1, so a partial check never passes as clean. `--allow-partial` accepts the
unchecked entries but still lists them.

All three commands support `--json`.

## What hactl enforces

**Every write is checked before it happens, and the dry run already tells
you.** A command that would switch, edit, create or delete something works out
everything its `--confirm` run would touch and checks all of it while
previewing. If anything is denied, the preview is refused — and `--confirm`
would be refused the same way, naming the same rule. Nothing is ever half
written:

```console
$ hactl ref replace light.old light.new
policy_denied: would rename "light.old" to "light.new" — refused by policy, nothing was written
  denied: edit file:packages/locks.yaml — deny "file:packages/lock*" [write] (line 15)
  denied: edit dashboard:security — deny "dashboard:security" [write] (line 16)
  checked: 41 resources, 2 denied
  profile: agent (selected by active)
  hint: the owner would change the rule on line 15, 16; allowed: 39 of 41 checked resources, but the command is refused as a whole — report it; do not edit policy.yaml
```

The exit code is `1`, and stderr gets a one-line count,
`policy_denied: 2 of 41 resources denied (profile agent)`. The `hint:` line
says what kind of refusal it is: a rule you wrote (and which line), nothing
allowing it under `default: deny`, or something hactl could not check. With `--json` you get the same as one object: the usual
preview object with `"ok": false`, `"error": "policy_denied"`, and the denied
list under `details.policy.denied`. If one of 40 files is denied, none of the
40 is written. (One narrow gap: a file that starts containing the old name in
the instant between `ref replace`'s own check and its write is written
unchecked; hactl then exits 1 and names that file.)

A preview that passed is not a promise forever. `--confirm` checks again, with
the policy file as it is then, the areas and labels as they are then, and the
entities a service call reaches then. If you edited the policy, moved a lamp
or relabelled something in between, `--confirm` can still be refused — and it
says the preview had passed.

A few things worth knowing:

- **Service calls are checked by what they reach.** `svc call light.turn_on`
  with an area, a label or a group as target needs `control` on every light it
  would actually switch; hactl asks Home Assistant for that set. Services that
  are not one domain's own — `homeassistant.turn_on`, or an integration's
  service that acts on, say, your locks — are checked against *everything* the
  target names, in every domain, and `entity_id: all` then means every entity
  you have. `homeassistant.turn_on` also needs each domain's own service
  (`light.turn_on`, `script.turn_on`, …), and anything that starts a script
  needs that script's `run`. Entity ids anywhere in `--data` count too, so
  `scene.apply` with a lock in it needs `control` on the lock. A light group
  and a scene pass the call on to what they hold: turning on a light group
  needs `control` on every light in it, and turning on a scene needs `control`
  on every entity the scene sets — hactl reads them from the group's or scene's
  own state, since Home Assistant's answer names the group or scene alone. If
  Home Assistant cannot tell hactl what a call would reach, the call is
  refused.
- **Switching an automation, a script, a helper or a template entity also asks
  its own address.** `deny "automation:prod_*": [write]` refuses turning
  `prod_heating` off, not just editing it; the same goes for
  `script:<id>`, `helper:<domain>/<id>` and `template:<unique_id>`. Only a deny
  decides this: allowing the entity (`entity:automation.*`) and the service is
  enough to switch it, even under `default: deny`.
- **The service itself is always checked**, also for services without any
  target such as `homeassistant.restart` — even when every entity is allowed.
  With `default: deny`, allow the services you want
  (`"service:light.*": [control]`); without it, a service no deny matches is
  allowed.
- **Commands that do more than one thing need every part.** `script apply`
  also reloads scripts (`service:script.reload`). `script run` needs
  `script:<id>/run` and the service `script.turn_on`. Deleting an automation,
  script, helper or template also removes its entity from the registry, so it
  needs `delete` on that entity as well. Deleting a label, an area or a floor
  changes everything that holds it, so it needs `edit` on each holder's label
  or area field (or on the areas of a floor); deleting an integration's config
  entry needs `delete` on every device and entity registered to it. hactl
  lists these from the registries, and may check a little more than Home
  Assistant actually changes — never less.
- **Renaming needs both names.** `ent rename a b` needs `edit` on `a`'s
  `registry/entity_id` and `create` on `b`, plus every file, automation,
  script, template and dashboard the rename rewrites. `auto apply` with a file
  whose `id:` differs from the target renames the automation, so it needs
  `create` on the new id; a file with no `id:` at all is refused. `ref replace`
  that rewrites an automation's `id:` is a rename too.
- **A rewritten file is also checked as what is in it.** `ref replace` (and
  `ent rename`) reads each file it would rewrite to see which automation,
  script or template entity every change lies in, and needs `edit` on that
  entry's config as well as on the file, so one denied entry in the file
  refuses the whole replace. If hactl cannot read the file, or
  cannot find the change in it, the rewrite is refused rather than checked as
  the file alone. A change in a part of the file that other entries share —
  a YAML anchor (`&x`) another automation refers to with `*x` or `<<: *x` —
  lies in every entry sharing it, and a change in a file that an automation,
  script or template pulls in with `!include` lies in that entry too. A change
  hactl cannot place in such an entry — an automation without an `id:`, a
  template entity without a `unique_id` — is checked as its file only.
- **Partly denied means denied.** If an entity is allowed but its
  `registry/entity_id` is denied, a command that would change the whole entity
  is refused. hactl writes at the finest level it can — `ent set-label` checks
  only `registry/labels`, `dash save` checks each changed view. But a change
  hactl can only address as a whole is refused by any denied part, even one it
  does not touch: with one view of a dashboard denied, changing the
  dashboard's title is refused, and with one `block/…` of a file denied,
  `ref replace` editing that file is refused.
- **Looking at a config flow counts as stepping it.** Asking Home Assistant
  for a flow's current state runs its current step, which can finish the flow,
  so `config flow-inspect` is checked like `config flow-step`.
- **New areas, floors, labels and integrations.** Home Assistant picks their id
  itself, so hactl cannot know it in advance. It checks `create` on *every* id
  of that kind: allow `"area:*": [create]`, and a deny on any one area's
  `create` blocks creating areas at all. The refusal names `area:*`.
- **Some side effects are not checked**, because they happen inside Home
  Assistant after an allowed write and hactl cannot list them in advance:
  the companion reloads what it wrote, and running a script lets that script
  do whatever it does.

**Over MCP**, `hactl mcp` without `--allow-writes` still refuses every write
first; with `--allow-writes`, the policy decides each one as on the command
line.

**The audit log.** While a policy is in effect, every write check — allowed or
denied, preview or real — is appended to `cache/policy-audit.log` in your
instance directory, one JSON line each, with the rule that decided it. Without
a policy there is no log. If hactl cannot write the log, it refuses the write
rather than act unrecorded.

## A broken file is refused

hactl never reads a broken file leniently, because a typo must not quietly turn
into "allow everything". Unknown verbs, unknown keys, duplicate keys, YAML
anchors, `active:` naming a profile that does not exist, and ids with invisible
characters or stray spaces at the ends are all refused. Every problem is named
with its line:

```console
$ hactl policy check entity:light.sofa control
policy_broken: /home/me/ha/policy.yaml:12: verbs for "entity:lock.*": unknown verb "wirte" (want read, control, edit, create, delete or write) — did you mean "write"?
policy_broken: policy.yaml belongs to the owner; hactl never writes it — report this, do not edit it
```

The same lines appear in `policy show`, `policy lint` and every refused
command, and the last one tells an agent that the fix is yours.

`policy show` and `policy lint` still run and list every problem. **Every
other command refuses until the file is fixed**, reads and `setup` included,
except `version`, `help`, `completion` and `rtfm`, which read no instance. An
MCP server keeps running and refuses each call the same way, with the same
exceptions; it reads the file again on every call, so fixing it is enough.

The same goes for invisible characters anywhere in an id, a sub-resource or a
`where:` value: any kind of space other than the plain ASCII space (a no-break
space, an ideographic space, a tab), invisible characters such as zero-width
spaces, variation selectors or a byte-order mark, and text that is not valid
UTF-8 are all refused (D-69). An id that looks right but holds one would be a
rule that matches nothing.

hactl never writes `policy.yaml`. There is no command for it. The file is yours.

---

For the exact rules, every `--json` field and edge cases, see the
[manual's reference](manual.md#policy-reference) (`hactl rtfm --reference
policy` prints it). For why each choice was made,
see `docs/decisions.md`, rows D-51 onward.
