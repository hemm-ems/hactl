"""Rig capability R3 — a custom component that publishes entities.

The point of this component is the gap between two names. Its integration
domain is ``shapewatch``; the entities it publishes live in the ``sensor``
entity domain and are called ``sensor.shape_watch_*``. Nothing about their
entity_id says which integration owns them, and the entity registry's
``platform`` field is the only thing that does.

That gap is finding #15: ``cc show`` counted the entities of a custom
component by matching entity_ids against the component's domain, and reported
``entities: 0`` for all fourteen real custom components on the reference
instance — including one with 467 entities. A rig with no custom component at
all could not have caught it, and a rig whose custom component happened to
publish ``shapewatch.*`` entities would have counted them correctly by
accident, which is worse: the wrong rule would have had a passing test.
"""

from __future__ import annotations

from homeassistant.core import HomeAssistant
from homeassistant.helpers.discovery import async_load_platform
from homeassistant.helpers.typing import ConfigType

DOMAIN = "shapewatch"


async def async_setup(hass: HomeAssistant, config: ConfigType) -> bool:
    """Set up the component and hand its sensor platform to HA."""
    hass.async_create_task(async_load_platform(hass, "sensor", DOMAIN, {}, config))
    return True
