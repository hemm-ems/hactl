"""Sensors published by the shapewatch component — see __init__.py.

Each entity carries a unique_id, which is what puts it in the entity registry
with ``platform: shapewatch``. Without one the entity would exist in the state
machine and be absent from the registry, and the registry join this fixture
exists to exercise would have nothing to find.
"""

from __future__ import annotations

from homeassistant.components.sensor import (
    SensorDeviceClass,
    SensorEntity,
    SensorStateClass,
)
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddEntitiesCallback
from homeassistant.helpers.typing import ConfigType, DiscoveryInfoType

SENSORS = (
    ("Shape Watch Alpha", "alpha", 11, True),
    ("Shape Watch Beta", "beta", 22, True),
    ("Shape Watch Gamma", "gamma", 33, True),
    # Registered but disabled by default, which is how a real integration ships
    # its optional diagnostics. HA files it in the entity registry with
    # disabled_by "integration" and never adds it to the state machine.
    #
    # The shape matters because it is the difference between two honest
    # answers. On the reference instance every single registry row without a
    # live state is disabled — 5524 rows, zero exceptions — so a count that
    # filters by "has a state" is not dropping stale rows, it is dropping
    # entities the integration owns and the user turned off. dwd_weather has 19
    # of 75 enabled; homematicip_local, 159 of 402.
    ("Shape Watch Delta", "delta", 44, False),
)


async def async_setup_platform(
    hass: HomeAssistant,
    config: ConfigType,
    async_add_entities: AddEntitiesCallback,
    discovery_info: DiscoveryInfoType | None = None,
) -> None:
    """Add the component's sensors once HA discovers the platform."""
    if discovery_info is None:
        return
    async_add_entities(ShapeWatchSensor(*spec) for spec in SENSORS)


class ShapeWatchSensor(SensorEntity):
    """A sensor whose entity_id says nothing about who owns it."""

    _attr_should_poll = False
    _attr_device_class = SensorDeviceClass.POWER
    _attr_state_class = SensorStateClass.MEASUREMENT
    _attr_native_unit_of_measurement = "W"

    def __init__(self, name: str, slug: str, value: int, enabled: bool) -> None:
        """Initialise one sensor with a fixed value."""
        self._attr_name = name
        self._attr_unique_id = f"shapewatch_{slug}"
        self._attr_native_value = value
        self._attr_entity_registry_enabled_default = enabled
