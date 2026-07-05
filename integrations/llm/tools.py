"""hactl wrappers exposed as llm tools.

Each function shells out to the hactl CLI and returns its stdout. Errors are
returned as text (so the model sees them) rather than raised. Most wrappers are
read-only. Safe write wrappers must expose an explicit confirm=False default and
only pass --confirm when confirm=True.

Env:
  HACTL_BIN  path to the hactl binary (defaults to "hactl" on PATH)
  HACTL_DIR  instance directory (forwarded as --dir; overrides auto-discovery)
"""

import json
import os
import subprocess
import tempfile

HACTL_BIN = os.environ.get("HACTL_BIN", "hactl")
HACTL_DIR = os.environ.get("HACTL_DIR")
TIMEOUT_S = 120

# Manual auto-delivery: the first tool call in this process (one process per
# conversation) gets the full manual prepended to its result, so the agent is
# guaranteed to have accurate syntax without spending a round on rtfm.
# Set HACTL_NO_RTFM_GATE=1 to disable, e.g. when the manual is already in the
# system prompt.
_manual_delivered = os.environ.get("HACTL_NO_RTFM_GATE") == "1"


def _exec(*args: str) -> str:
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


def _run(*args: str) -> str:
    global _manual_delivered
    out = _exec(*args)
    if not _manual_delivered:
        _manual_delivered = True
        if args[0] != "rtfm":
            manual = _exec("rtfm")
            out = (
                "[hactl manual — delivered once with your first tool call. Use it "
                "for every subsequent command, flag, and workflow decision.]\n\n"
                f"{manual}\n\n"
                f"=== RESULT of hactl {' '.join(args)} ===\n{out}"
            )
    return out


def hactl_rtfm() -> str:
    """Print the full hactl manual. Rarely needed: the manual is delivered
    automatically with your first tool call's result."""
    global _manual_delivered
    if _manual_delivered:
        return ("Manual already delivered earlier in this conversation — re-read it "
                "there instead of re-fetching.")
    _manual_delivered = True
    return _exec("rtfm")


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
    """Show entity profile: state, key attributes, area, labels, last change attribution."""
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


def hactl_ent_set_area(entity_id: str, area: str, confirm: bool = False) -> str:
    """Preview or set an entity's area. confirm=False returns the dry-run plan without writing.
    Only use confirm=True after the user explicitly confirms the exact entity and target area."""
    extra = ["--confirm"] if confirm else []
    return _run("ent", "set-area", entity_id, area, *extra)


def hactl_svc_call(service: str, data: dict = {}, confirm: bool = False) -> str:
    """Call a HA service, e.g. service='automation.turn_off', data={'entity_id': 'automation.x'}.
    confirm=False (default) executes NOTHING: it returns the planned command so you can ask the
    user. Only use confirm=True after the user explicitly confirmed the exact action."""
    payload = json.dumps(data or {})
    if not confirm:
        return (
            "DRY RUN — nothing was executed.\n"
            f"Planned: hactl svc call {service} -d '{payload}'\n"
            "Present this plan to the user and ask for confirmation. Retry with "
            "confirm=True only after the user explicitly confirms."
        )
    return _run("svc", "call", service, "-d", payload)


def hactl_dash_ls() -> str:
    """List Lovelace dashboards with url_path, title, and mode."""
    return _run("dash", "ls")


def hactl_dash_show(url_path: str) -> str:
    """Show a dashboard's config (views, cards). url_path from dash ls."""
    return _run("dash", "show", url_path)


def hactl_dash_create(url_path: str, title: str, icon: str = "", confirm: bool = False) -> str:
    """Create a dashboard. url_path must contain a hyphen (e.g. 'energy-dash').
    confirm=False returns the dry-run plan without creating anything. Only use
    confirm=True after the user explicitly confirmed."""
    extra = ["--url-path", url_path, "--title", title]
    if icon:
        extra += ["--icon", icon]
    if confirm:
        extra.append("--confirm")
    return _run("dash", "create", *extra)


def hactl_dash_save(url_path: str, config: dict, confirm: bool = False) -> str:
    """Save a dashboard's full config (JSON object with 'views'). confirm=False
    returns the dry-run diff without writing. Only use confirm=True after the
    user explicitly confirmed the exact config."""
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
        json.dump(config, f)
        path = f.name
    try:
        extra = ["-f", path]
        if confirm:
            extra.append("--confirm")
        return _run("dash", "save", url_path, *extra)
    finally:
        os.unlink(path)


def hactl_device_ls(name: str = "", area: str = "", label: str = "", pattern: str = "") -> str:
    """List devices with area, labels, and entity counts. Filters: name substring, area, label, pattern."""
    extra = []
    if name:
        extra += ["--name", name]
    if area:
        extra += ["--area", area]
    if label:
        extra += ["--label", label]
    if pattern:
        extra += ["--pattern", pattern]
    return _run("device", "ls", *extra)


def hactl_device_show(device: str) -> str:
    """Show one device by ID or name, including its registered entities."""
    return _run("device", "show", device)


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


def hactl_script_show(script_id: str) -> str:
    """Show one script's config summary and its last 5 traces (with stable trace IDs)."""
    return _run("script", "show", script_id)


def hactl_tpl_eval(template: str) -> str:
    """Evaluate a Jinja2 template server-side in HA (read-only). Supports states(),
    state_attr(), and HA's custom filters. Example: '{{ states("sensor.temperature") }}'."""
    return _run("tpl", "eval", template)
