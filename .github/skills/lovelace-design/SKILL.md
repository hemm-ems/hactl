---
name: lovelace-design
description: 'Home Assistant Lovelace dashboard design expertise. Use when asked to create, modify, inspect, or design HA dashboards. Covers dashboard architecture, card types, layout best practices, and the hactl dash CLI workflow.'
---

# Lovelace Dashboard Design Skill

## Architecture Overview

Lovelace is Home Assistant's UI framework. The hierarchy is:

```
Dashboard
└── Views (tabs at the top)
    └── Sections (groups within a view, for sections view type)
        └── Cards (individual UI elements)
```

**View types:**
- **sections** (default, recommended) — grid-based layout with named sections. 12-column grid, each row 56px tall, 8px gap. Use `grid_options` on cards for sizing.
- **masonry** (legacy) — auto-arranged cards in columns by size.
- **panel** — single card fills the entire view.
- **sidebar** — two-column layout (main + sidebar).

**Storage mode vs YAML mode:**
- **Storage mode** (standard) — config stored in HA database, editable via UI and WebSocket API.
- **YAML mode** (deprecated, removal planned 2026.8) — read-only via API. Cannot save via `dash save`.

## CLI Workflow

### Reading dashboards

```bash
# List all dashboards
hactl dash ls
hactl dash ls --json

# Show view summary for default dashboard
hactl dash show

# Show view summary for a named dashboard
hactl dash show my-dashboard

# Get raw JSON config (for LLM round-trip editing)
hactl dash show my-dashboard --raw

# Get pretty-printed JSON
hactl dash show my-dashboard --json

# Show a single view's full config
hactl dash show my-dashboard --view living-room
```

### Modifying dashboards

The workflow is **read → modify → write**. HA has no partial update API — you must save the entire config.

```bash
# 1. Get current config
hactl dash show my-dashboard --raw > /tmp/dash.json

# 2. Modify the JSON (add views, cards, change layout)
# ... edit /tmp/dash.json ...

# 3. Dry-run to check
hactl dash save my-dashboard --file /tmp/dash.json

# 4. Apply
hactl dash save my-dashboard --file /tmp/dash.json --confirm
```

### Creating and deleting dashboards

```bash
# Create (url-path MUST contain a hyphen)
hactl dash create --url-path my-dashboard --title "My Dashboard" --icon mdi:view-dashboard --confirm

# Delete
hactl dash delete my-dashboard --confirm
```

### Gathering context

Before designing a dashboard, gather entity and area context:

```bash
hactl ent ls --json                    # all entities with states
hactl ent ls --domain light --json     # lights only
hactl area ls --json                   # rooms/areas
hactl label ls --json                  # labels for grouping
hactl floor ls --json                  # floors
```

## Card Reference

### Essential cards (use first)

| Card | Use for | Key properties |
|------|---------|----------------|
| `tile` | Primary entity display + control | `entity`, `name`, `icon`, `color`, `features[]` |
| `heading` | Section titles | `heading`, `heading_style`, `icon` |
| `entities` | Grouped entity list | `entities[]`, `title`, `show_header_toggle` |
| `markdown` | Rich text, templates | `content` (supports Jinja2) |

### Monitoring cards

| Card | Use for | Key properties |
|------|---------|----------------|
| `sensor` | Single sensor value | `entity`, `name`, `graph` (`line`/`none`) |
| `gauge` | Value in a range | `entity`, `min`, `max`, `severity` |
| `history-graph` | Historical data | `entities[]`, `hours_to_show` |
| `statistics-graph` | Long-term stats | `entities[]`, `period` (`5minute`/`hour`/`day`/`week`/`month`) |
| `logbook` | Activity log | `entities[]`, `hours_to_show` |
| `calendar` | Calendar events | `entities[]` (calendar entities) |
| `weather-forecast` | Weather display | `entity`, `show_forecast`, `forecast_type` |
| `map` | Entity locations | `entities[]`, `geo_location_sources` |

### Control cards

| Card | Use for | Key properties |
|------|---------|----------------|
| `button` | Toggle/action trigger | `entity`, `name`, `icon`, `tap_action` |
| `light` | Light with brightness | `entity`, `name` |
| `thermostat` | Climate control | `entity`, `name` |
| `humidifier` | Humidity control | `entity` |
| `alarm-panel` | Alarm system | `entity`, `states` |
| `media-control` | Media player | `entity` |

### Layout cards

| Card | Use for | Key properties |
|------|---------|----------------|
| `horizontal-stack` | Side-by-side cards | `cards[]` |
| `vertical-stack` | Stacked cards | `cards[]` |
| `grid` | Grid of cards | `cards[]`, `columns` |
| `conditional` | Show/hide by state | `conditions[]`, `card` |
| `entity-filter` | Dynamic entity list | `entities[]`, `state_filter[]`, `card` |

### Rich display cards

| Card | Use for | Key properties |
|------|---------|----------------|
| `area` | Area overview with controls | `area`, `show_camera`, `navigation_path` |
| `picture-entity` | Entity state on image | `entity`, `image`, `camera_image` |
| `picture-elements` | Interactive floorplan | `image`, `elements[]` |
| `picture-glance` | Image with entity icons | `entities[]`, `image`, `camera_image` |
| `iframe` | Embedded webpage | `url`, `aspect_ratio` |
| `todo-list` | To-do list | `entity` |

## Grid Options (Sections View)

Cards in sections views support `grid_options` for precise sizing:

```json
{
  "type": "tile",
  "entity": "light.kitchen",
  "grid_options": {
    "columns": 6,
    "rows": 1
  }
}
```

- **columns**: Width in 12ths of section width. Use multiples of 3: `3` (quarter), `6` (half), `12` (full).
- **rows**: Height in units of 56px. Default is usually 1-2.
- Section width / 12 ≈ 30px per column unit.

## Common Dashboard Patterns

### Room-based dashboard
One view per room. Each view has sections for lighting, climate, media, sensors.

```json
{
  "views": [
    {
      "title": "Living Room",
      "path": "living-room",
      "type": "sections",
      "sections": [
        {
          "cards": [
            {"type": "heading", "heading": "Lights"},
            {"type": "tile", "entity": "light.living_room_main"},
            {"type": "tile", "entity": "light.living_room_accent"}
          ]
        },
        {
          "cards": [
            {"type": "heading", "heading": "Climate"},
            {"type": "thermostat", "entity": "climate.living_room"}
          ]
        }
      ]
    }
  ]
}
```

### Status overview dashboard
Single view with key information at a glance.

```json
{
  "views": [
    {
      "title": "Status",
      "path": "status",
      "type": "sections",
      "sections": [
        {
          "cards": [
            {"type": "heading", "heading": "Home"},
            {"type": "weather-forecast", "entity": "weather.home", "show_forecast": true},
            {"type": "entities", "entities": ["person.alice", "person.bob"], "title": "People"}
          ]
        },
        {
          "cards": [
            {"type": "heading", "heading": "Energy"},
            {"type": "sensor", "entity": "sensor.power_consumption", "graph": "line"},
            {"type": "gauge", "entity": "sensor.solar_production", "min": 0, "max": 10000}
          ]
        }
      ]
    }
  ]
}
```

### Mobile-friendly layout
Use fewer columns, larger tiles, and `grid_options` with `columns: 12` for full-width cards.

## Quirks and Gotchas

1. **Full config replacement** — `dash save` replaces the entire dashboard config. There is no partial update. Always `dash show --raw` first, modify, then `dash save`.

2. **url_path must contain a hyphen** — When creating dashboards, `url_path` must include a `-` character (e.g., `my-dashboard`). Single words fail validation.

3. **Strategy dashboards** — Some dashboards use `strategy` instead of `views` for auto-generated layouts (e.g., the built-in Overview). These have no persisted config until the user "takes control." Fetching their config may return an error.

4. **Sections vs masonry** — Sections view uses `sections[].cards[]`; masonry view uses `cards[]` directly on the view. Don't mix them.

5. **Badges** — Badges appear at the top of a view. In sections view, badge position is controlled via `header.badges_position`.

6. **Conditional visibility** — Cards and sections support `visibility` conditions to show/hide based on entity state, user, screen size, etc.

7. **Custom cards** — Cards with `type: "custom:card-name"` require the corresponding resource to be registered. Check with `hactl dash resources`.

8. **View `path`** — Each view should have a unique `path` (URL slug). If omitted, HA auto-generates one from the view index.

9. **Subviews** — Views with `subview: true` don't show as tabs. They are accessed via navigation actions from other cards (e.g., `tap_action: {action: "navigate", navigation_path: "/my-dashboard/sub-view"}`).

10. **Theme per view** — Each view can have its own `theme` property for visual differentiation.

## Config Structure Reference

Full dashboard config JSON structure:

```json
{
  "views": [
    {
      "title": "string",
      "path": "string",
      "icon": "mdi:icon-name",
      "type": "sections|masonry|panel|sidebar",
      "theme": "string",
      "subview": false,
      "max_columns": 4,
      "background": {
        "image": "/local/bg.png",
        "opacity": 50,
        "size": "cover",
        "alignment": "center",
        "repeat": "no-repeat",
        "attachment": "fixed"
      },
      "badges": [
        {"type": "entity", "entity": "sensor.temp"}
      ],
      "sections": [
        {
          "title": "Section Title",
          "column_span": 1,
          "cards": [
            {"type": "card-type", "...": "card-specific-config"}
          ],
          "visibility": [
            {"condition": "state", "entity": "input_boolean.show", "state": "on"}
          ]
        }
      ]
    }
  ]
}
```
