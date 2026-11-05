# Skill: draft a permission policy for yourself

You are an agent that works on a Home Assistant instance through hactl. The
owner wants you to propose the rules you will work under. You **draft**; the
owner **reviews and activates**. You never write `policy.yaml` and never run
`hactl policy activate` — it needs the owner's terminal and refuses yours.

## 1. Know where you stand

```bash
hactl policy show            # your profile now (a new agent starts read-only)
hactl rtfm --reference policy # the grammar: kinds, verbs, matching, where:
```

## 2. Inventory what is there

Read before you ask, so your questions name real things:

```bash
hactl ent ls --domain light       # and lock, switch, cover, climate, alarm_control_panel, …
hactl area ls ; hactl label ls
hactl auto ls ; hactl script ls ; hactl dash ls
```

## 3. Ask the owner, in plain language

Ask about outcomes, not syntax. One short message, for example:

- Which rooms or devices may I **switch** (lights, covers, climate)? Anything I must **never** switch (locks, alarm, garage)?
- May I **create or edit** automations, scripts, dashboards — all, or only ones with a prefix such as `agent_`? May I **delete**?
- Is there anything I should **not see** at all (a camera, a tracker's location)?
- May I change names, areas or labels?

Never assume an answer. If the owner says "everything except the locks", write exactly that.

## 4. Write the draft

Write `policy.draft.yaml` in your instance directory (beside `.env`). Start
from deny and allow what the owner said:

```yaml
version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*":                 [read]
      "entity:light.*":           [control]
      "service:light.*":          [control]
      "automation:agent_*":       [read, edit, create, delete]
      "entity:automation.agent_*": [control, create, delete]
    deny:
      "entity:lock.*":            [read, write]
```

Rules worth knowing:

- Switching needs `control` on the **entity and the service** (`service:light.turn_on`).
- What an automation you write would switch is checked as if you switched it yourself, and what it only watches needs `read` — allow what your automations will touch.
- Prefer `[edit]` over `[write]` for "no renames" (`entity:*/registry/entity_id`): `[write]` also blocks deleting.
- Do not let a `where: {label: …}` allow be satisfiable by a label edit you are also allowed.

## 5. Check it

```bash
hactl policy lint --file policy.draft.yaml
hactl policy check --file policy.draft.yaml entity:lock.front_door control
```

Fix every `no-match` (a typo protects nothing), `self-grant` and `blocked-by-part` finding, or tell the owner why one stays.

## 6. Hand over

Tell the owner, in one message:

1. what the draft allows and denies, in their words;
2. to run, **in a terminal of their own**:

```bash
hactl policy review --file policy.draft.yaml    # what you would be allowed, in counts and names
hactl policy activate policy.draft.yaml         # only the owner can; it asks for the profile name
```

Then stop. Until the owner activates it, your old profile applies. If a later
command is refused, report the rule it names; do not edit `policy.yaml`, and
do not work around the refusal.
