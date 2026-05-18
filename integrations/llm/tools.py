"""Read-only hactl wrappers exposed as llm tools.

Each function shells out to the hactl CLI and returns its stdout. Errors are
returned as text (so the model sees them) rather than raised. Write commands
(svc call, auto apply, dash apply, label create, ent set-area, ent set-label,
rollback) are intentionally absent so the agent must respond textually and
ask for confirmation per the eval contract.

Env:
  HACTL_BIN  path to the hactl binary (defaults to "hactl" on PATH)
  HACTL_DIR  instance directory (forwarded as --dir; overrides auto-discovery)
"""

import os
import subprocess

HACTL_BIN = os.environ.get("HACTL_BIN", "hactl")
HACTL_DIR = os.environ.get("HACTL_DIR")
TIMEOUT_S = 120


def _run(*args: str) -> str:
    cmd = [HACTL_BIN]
    if HACTL_DIR:
        cmd += ["--dir", HACTL_DIR]
    cmd += list(args)
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=TIMEOUT_S)
    except subprocess.TimeoutExpired:
        return f"ERROR: hactl {' '.join(args)} timed out after {TIMEOUT_S}s"
    if result.returncode != 0:
        return f"ERROR (exit {result.returncode}): {result.stderr.strip() or result.stdout.strip()}"
    return result.stdout


def hactl_health() -> str:
    """Show Home Assistant health: version, state, recorder status, ERROR count, location, timezone."""
    return _run("health")


def hactl_issues() -> str:
    """List active HA repairs/issues (domain, severity, fixable)."""
    return _run("issues")


def hactl_log(errors: bool = False, unique: bool = False, component: str = "", since: str = "24h") -> str:
    """View HA log entries. errors=True restricts to ERROR-level. unique=True deduplicates identical messages.
    component is a substring filter (e.g. 'recorder', 'zha'). since is a duration like '24h' or '7d'."""
    extra = ["--since", since]
    if errors:
        extra.append("--errors")
    if unique:
        extra.append("--unique")
    if component:
        extra += ["--component", component]
    return _run("log", *extra)


def hactl_log_show(log_id: str) -> str:
    """Show full detail (timestamp, component, message) for a single log entry by id (e.g. 'log:f2')."""
    return _run("log", "show", log_id)


def hactl_changes(since: str = "24h") -> str:
    """Show recent logbook entries: state changes and automation triggers within the time window."""
    return _run("changes", "--since", since)


def hactl_auto_ls(failing: bool = False, pattern: str = "", label: str = "") -> str:
    """List automations with id, state, area, labels, runs_24h, errors. failing=True shows only ones with errors.
    pattern is a glob/substring on the id (e.g. 'ess_*'). label filters by label substring."""
    extra = []
    if failing:
        extra.append("--failing")
    if pattern:
        extra += ["--pattern", pattern]
    if label:
        extra += ["--label", label]
    return _run("auto", "ls", *extra)


def hactl_auto_show(automation_id: str) -> str:
    """Show one automation's config summary and its last 5 traces (with stable trace IDs like trc:a7)."""
    return _run("auto", "show", automation_id)


def hactl_trace_show(trace_id: str, full: bool = False) -> str:
    """Show a condensed trace (trigger -> condition -> action, pass/fail). full=True returns raw trace JSON."""
    extra = ["--full"] if full else []
    return _run("trace", "show", trace_id, *extra)


def hactl_ent_ls(domain: str = "", area: str = "", pattern: str = "", label: str = "") -> str:
    """List entities. domain (e.g. 'sensor'), area (substring), pattern (glob), label (substring) all filter."""
    extra = []
    if domain:
        extra += ["--domain", domain]
    if area:
        extra += ["--area", area]
    if pattern:
        extra += ["--pattern", pattern]
    if label:
        extra += ["--label", label]
    return _run("ent", "ls", *extra)


def hactl_ent_show(entity_id: str) -> str:
    """Show full entity profile: state, attributes, area, device, related entities."""
    return _run("ent", "show", entity_id)


def hactl_ent_hist(entity_id: str, since: str = "24h") -> str:
    """Show entity state history over the time window. entity_id like 'sensor.wp_vl'."""
    return _run("ent", "hist", entity_id, "--since", since)


def hactl_ent_anomalies(entity_id: str, since: str = "24h") -> str:
    """Detect anomalies in an entity's history (sudden value changes, drops, outliers, gaps)."""
    return _run("ent", "anomalies", entity_id, "--since", since)


def hactl_ent_related(entity_id: str) -> str:
    """List entities related to the given one (same area/device, or referenced by automations)."""
    return _run("ent", "related", entity_id)


def hactl_label_ls() -> str:
    """List all labels in HA with their usage counts."""
    return _run("label", "ls")


def hactl_area_ls() -> str:
    """List all areas (rooms) in HA with entity counts."""
    return _run("area", "ls")


def hactl_floor_ls() -> str:
    """List all floors in HA."""
    return _run("floor", "ls")


def hactl_script_ls(failing: bool = False, pattern: str = "") -> str:
    """List scripts with state, runs_24h, errors. failing=True shows only ones with errors."""
    extra = []
    if failing:
        extra.append("--failing")
    if pattern:
        extra += ["--pattern", pattern]
    return _run("script", "ls", *extra)
