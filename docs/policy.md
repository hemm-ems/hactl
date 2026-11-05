# Permission policy

A `policy.yaml` in your instance directory tells hactl what an agent may read
and change, per entity, automation, dashboard and so on. For example: "read
everything except locks, switch lights only in the living room, edit only the
`pg_*` automations, change labels but never names".

> **A guardrail, not a lock.** The policy catches *mistakes*: a misread
> instruction, a target wider than intended, a typo in an id. It does not stop
> anything that wants to get around it. The token in `.env` can do everything,
> and anything with a shell can call Home Assistant directly or edit the file.

> **Status: not enforced yet.** In this version only `hactl policy show`,
> `policy check` and `policy lint` read the file. Every other command behaves
> exactly as before. Use this version to write and test your policy. Enforcement
> comes in the next release.

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
without the feature.

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

`policy show` and `policy lint` still run and list every problem. Once
enforcement lands, every other command refuses until the file is fixed.

hactl never writes `policy.yaml`. There is no command for it. The file is yours.

---

For the exact rules, every `--json` field and edge cases, see the
[manual](manual.md#policy-permission-guardrail). For why each choice was made,
see `docs/decisions.md`, rows D-51 to D-68.
