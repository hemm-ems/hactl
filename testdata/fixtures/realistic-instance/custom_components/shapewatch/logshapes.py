"""Rig capability R6 — the shapes a real Home Assistant's error log has.

Every entry Home Assistant keeps in ``system_log`` on the reference instance is
one of a handful of shapes, and the rig had none of them: a container that just
booted logs short, single-line, ASCII messages from loggers one segment deep.
Findings #14 and #16 both live in shapes this fixture had no way to produce.

The four records below are the properties, not the sentences. Each says which
finding it carries and why a rig without it is green by construction:

``LONG_SINGLE_LINE``
    Longer than the 60-byte display budget with no newline in it — the plain
    case of #14. 43 of the reference instance's 54 entries are this shape.

``SHORT_FIRST_LINE``
    A first line UNDER the budget and more lines after it. The truncation that
    #14 is about is a length test, so a message like this passed straight
    through it with its newline intact and broke the text table's column
    alignment — the reference instance printed 58 lines for 54 rows plus a
    header. A message that is merely long cannot show that.

``RUNE_AT_THE_BOUNDARY``
    A two-byte character whose bytes straddle offset 57, which is where a
    ``msg[:57]`` cut lands. The reference instance's messages are German and
    full of umlauts, so this is not a contrived shape; it is the one that had
    not happened yet. Slicing a Go string by bytes there yields invalid UTF-8.

``EXCEPTION``
    A multi-kilobyte traceback. Home Assistant carries it in ``exception``
    rather than ``message``, so it is a second field hactl has to join, and the
    truncated cell is the only view a caller had of it.

The logger names are deliberately four segments deep with ``shapewatch`` in the
middle. That is #16: ``--component shapewatch`` matches the full name, and the
displayed value is the last segment, ``probe`` — which does not contain the
filter term. A logger called ``shapewatch`` alone would match and display the
same string, and the defect would have had a passing test.
"""

from __future__ import annotations

import logging

_PROBE = logging.getLogger("custom_components.shapewatch.diagnostics.probe")
_LOADER = logging.getLogger("custom_components.shapewatch.helpers.loader")

LONG_SINGLE_LINE = (
    "Shape watch probe could not reach the configured endpoint at "
    "192.0.2.41:8443 after 3 attempts; the collector stays degraded until the "
    "next successful poll and no measurement is recorded for this interval"
)

SHORT_FIRST_LINE = (
    "Shape watch loader skipped 2 sources\n"
    "  source alpha: manifest declares a version the loader cannot parse\n"
    "  source beta: no reader is registered for content type application/cbor"
)

# 56 ASCII characters, then "ü". Its two bytes occupy offsets 56 and 57, so a
# byte slice at 57 cuts the character in half. The tail keeps the whole message
# over the budget so the cut actually happens.
RUNE_AT_THE_BOUNDARY = (
    "Shape watch probe rejected a reading from sensor number "
    "über dem zulässigen Bereich — Messwert verworfen"
)

EXCEPTION_MESSAGE = "Shape watch probe failed while decoding a reading"


def _raise_nested() -> None:
    """Raise through two frames so the traceback is worth truncating."""

    def inner() -> None:
        raise ValueError(
            "reading 'sensor.shape_watch_alpha' carried a payload the decoder "
            "does not recognise: expected a mapping with 'value' and 'unit'"
        )

    inner()


def emit() -> None:
    """Write the four shapes into Home Assistant's error log."""
    assert RUNE_AT_THE_BOUNDARY.encode()[56:58] == "ü".encode(), (
        "RUNE_AT_THE_BOUNDARY no longer straddles offset 57; the shape is the "
        "byte offset, so an edit to the wording has to keep it"
    )
    _PROBE.error(LONG_SINGLE_LINE)
    _LOADER.warning(SHORT_FIRST_LINE)
    _PROBE.warning(RUNE_AT_THE_BOUNDARY)
    try:
        _raise_nested()
    except ValueError:
        _PROBE.exception(EXCEPTION_MESSAGE)
