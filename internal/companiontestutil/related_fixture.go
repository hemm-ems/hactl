//go:build companion || companion_discovery

package companiontestutil

import (
	"fmt"
	"os"
	"os/exec"
)

const (
	RelatedSourceEntityID    = "sensor.hactl_related_source"
	RelatedGeneratedEntityID = "sensor.hactl_related_generated"
	RelatedYAMLPeerEntityID  = "sensor.hactl_related_yaml_peer"
	RelatedEmbeddedEntityID  = "sensor.hactl_related_embedded"
	RelatedUnknownEntityID   = "sensor.hactl_related_unknown"

	RelatedGeneratedConfigEntryID = "hactl_related_generated_entry"
)

// SeedRelatedFixture writes deterministic related-graph fixture data into the
// Docker Compose service's mounted /config directory.
func SeedRelatedFixture(composeFile, service string) error {
	cmd := exec.Command("docker", "compose", "-f", composeFile, "exec", "-T", service, "python3", "-c", relatedFixtureScript) //nolint:gosec // test harness command
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("seeding related fixture via docker compose exec: %w", err)
	}
	return nil
}

const relatedFixtureScript = `
import json
from pathlib import Path

SOURCE_ENTITY_ID = "sensor.hactl_related_source"
GENERATED_ENTITY_ID = "sensor.hactl_related_generated"
YAML_PEER_ENTITY_ID = "sensor.hactl_related_yaml_peer"
EMBEDDED_ENTITY_ID = "sensor.hactl_related_embedded"
UNKNOWN_ENTITY_ID = "sensor.hactl_related_unknown"

SOURCE_CONFIG_ENTRY_ID = "hactl_related_source_entry"
GENERATED_CONFIG_ENTRY_ID = "hactl_related_generated_entry"
YAML_PEER_CONFIG_ENTRY_ID = "hactl_related_yaml_peer_entry"
EMBEDDED_CONFIG_ENTRY_ID = "hactl_related_embedded_entry"

SOURCE_DEVICE_ID = "hactl_related_source_device"
GENERATED_DEVICE_ID = "hactl_related_generated_device"
YAML_PEER_DEVICE_ID = "hactl_related_yaml_peer_device"
EMBEDDED_DEVICE_ID = "hactl_related_embedded_device"

CONFIG_BLOCK_START = "# hactl related fixture start"
CONFIG_BLOCK_END = "# hactl related fixture end"
CONFIG_BLOCK = f"""
{CONFIG_BLOCK_START}
hactl_related_graph:
  exact_pair:
    source: {SOURCE_ENTITY_ID}
    peer: {YAML_PEER_ENTITY_ID}
  embedded_text: "prefix {EMBEDDED_ENTITY_ID} suffix"
  unknown_text: "{UNKNOWN_ENTITY_ID}"
{CONFIG_BLOCK_END}
"""


def seed_related_fixture(config_dir):
    config_dir.mkdir(parents=True, exist_ok=True)
    merge_configuration_yaml(config_dir / "configuration.yaml")
    merge_storage_list(
        config_dir / ".storage" / "core.config_entries",
        "entries",
        "entry_id",
        [
            {
                "entry_id": SOURCE_CONFIG_ENTRY_ID,
                "domain": "hactl_related_source",
                "title": "hactl related source",
                "data": {},
                "options": {},
            },
            {
                "entry_id": GENERATED_CONFIG_ENTRY_ID,
                "domain": "hactl_related_generated",
                "title": "hactl related generated",
                "data": {"source_entity_id": SOURCE_ENTITY_ID},
                "options": {"note": f"prefix {SOURCE_ENTITY_ID} suffix"},
            },
            {
                "entry_id": YAML_PEER_CONFIG_ENTRY_ID,
                "domain": "hactl_related_yaml_peer",
                "title": "hactl related yaml peer",
                "data": {},
                "options": {},
            },
            {
                "entry_id": EMBEDDED_CONFIG_ENTRY_ID,
                "domain": "hactl_related_embedded",
                "title": "hactl related embedded",
                "data": {"embedded_source": f"prefix {SOURCE_ENTITY_ID} suffix"},
                "options": {"unknown": UNKNOWN_ENTITY_ID},
            },
        ],
    )
    merge_storage_list(
        config_dir / ".storage" / "core.entity_registry",
        "entities",
        "entity_id",
        [
            {
                "entity_id": SOURCE_ENTITY_ID,
                "platform": "hactl_related_source",
                "config_entry_id": SOURCE_CONFIG_ENTRY_ID,
                "device_id": SOURCE_DEVICE_ID,
                "area_id": "hactl_related_area",
            },
            {
                "entity_id": GENERATED_ENTITY_ID,
                "platform": "hactl_related_generated",
                "config_entry_id": GENERATED_CONFIG_ENTRY_ID,
                "device_id": GENERATED_DEVICE_ID,
                "area_id": "hactl_related_area",
            },
            {
                "entity_id": YAML_PEER_ENTITY_ID,
                "platform": "hactl_related_yaml_peer",
                "config_entry_id": YAML_PEER_CONFIG_ENTRY_ID,
                "device_id": YAML_PEER_DEVICE_ID,
                "area_id": "hactl_related_area",
            },
            {
                "entity_id": EMBEDDED_ENTITY_ID,
                "platform": "hactl_related_embedded",
                "config_entry_id": EMBEDDED_CONFIG_ENTRY_ID,
                "device_id": EMBEDDED_DEVICE_ID,
                "area_id": "hactl_related_area",
            },
        ],
    )
    merge_storage_list(
        config_dir / ".storage" / "core.device_registry",
        "devices",
        "id",
        [
            {"id": SOURCE_DEVICE_ID, "config_entries": [SOURCE_CONFIG_ENTRY_ID], "area_id": "hactl_related_area"},
            {"id": GENERATED_DEVICE_ID, "config_entries": [GENERATED_CONFIG_ENTRY_ID], "area_id": "hactl_related_area"},
            {"id": YAML_PEER_DEVICE_ID, "config_entries": [YAML_PEER_CONFIG_ENTRY_ID], "area_id": "hactl_related_area"},
            {"id": EMBEDDED_DEVICE_ID, "config_entries": [EMBEDDED_CONFIG_ENTRY_ID], "area_id": "hactl_related_area"},
        ],
    )


def merge_configuration_yaml(path):
    existing = path.read_text(encoding="utf-8") if path.exists() else ""
    content = remove_marked_block(existing).rstrip()
    if content:
        content += "\n\n"
    path.write_text(content + CONFIG_BLOCK.lstrip(), encoding="utf-8")


def remove_marked_block(content):
    start = content.find(CONFIG_BLOCK_START)
    end = content.find(CONFIG_BLOCK_END)
    if start == -1 or end == -1 or end < start:
        return content
    return content[:start].rstrip() + "\n" + content[end + len(CONFIG_BLOCK_END) :].lstrip()


def merge_storage_list(path, list_key, id_key, entries):
    path.parent.mkdir(parents=True, exist_ok=True)
    raw = read_storage(path, path.name)
    data = raw.setdefault("data", {})
    current = data.get(list_key)
    if not isinstance(current, list):
        current = []
    fixture_ids = {entry[id_key] for entry in entries}
    data[list_key] = [
        item for item in current if not isinstance(item, dict) or item.get(id_key) not in fixture_ids
    ] + entries
    path.write_text(json.dumps(raw, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def read_storage(path, key):
    if path.exists():
        try:
            raw = json.loads(path.read_text(encoding="utf-8"))
        except json.JSONDecodeError:
            raw = {}
    else:
        raw = {}
    if not isinstance(raw, dict):
        raw = {}
    raw.setdefault("version", 1)
    raw.setdefault("minor_version", 1)
    raw.setdefault("key", key)
    data = raw.setdefault("data", {})
    if not isinstance(data, dict):
        raw["data"] = {}
    return raw


seed_related_fixture(Path("/config"))
`
