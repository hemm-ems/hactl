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
rule that decided it.

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

### Verbs

| verb | means | e.g. |
|---|---|---|
| `read` | see it | `ent show`, `dash show` |
| `control` | change runtime state | `svc call`, `script run` |
| `edit` | change existing config or a registry field | `auto apply`, `ent set-label` |
| `create` | make something new | `auto create`, `area create` |
| `delete` | remove it | `auto delete` |

`write` is shorthand for `control, edit, create, delete`.

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
  part;
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
- `implicit-allow-all`: profiles exist, but none is in effect.

Some things lint cannot check yet: template, helper, config_entry and file ids,
and dashboard view paths. It lists them as **unchecked** and exits 1, so a
partial check never passes as clean. `--allow-partial` accepts the unchecked
entries but still lists them.

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
policy_denied: would replace references — refused by policy, nothing was written
  denied: edit file:packages/locks.yaml — deny "file:packages/lock*" [write] (line 15)
  denied: edit dashboard:security — deny "dashboard:security" [write] (line 16)
  checked: 41 resources, 2 denied
  profile: agent (selected by active)
```

The exit code is `1`. With `--json` you get the same as one object: the usual
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
  `scene.apply` with a lock in it needs `control` on the lock. If Home
  Assistant cannot tell hactl what a call would reach, the call is refused.
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
- **Partly denied means denied.** If an entity is allowed but its
  `registry/entity_id` is denied, a command that would change the whole entity
  is refused. hactl writes at the finest level it can — `ent set-label` checks
  only `registry/labels`, `dash save` checks each changed view. But a change
  hactl can only address as a whole is refused by any denied part, even one it
  does not touch: with one view of a dashboard denied, changing the
  dashboard's title is refused, and with one block of a file denied,
  `ref replace` cannot edit that file at all.
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
policy file is broken — hactl refuses to answer from it (D-60):
/home/me/ha/policy.yaml:12: verbs for "entity:lock.*": unknown verb "wirte" (want read, control, edit, create, delete or write)
```

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
[manual](manual.md#policy-permission-guardrail). For why each choice was made,
see `docs/decisions.md`, rows D-51 to D-82.
