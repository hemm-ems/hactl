## hactl

[![CI](https://github.com/hemm-ems/hactl/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/hemm-ems/hactl/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/hemm-ems/hactl)](https://github.com/hemm-ems/hactl/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/hemm-ems/hactl)](https://goreportcard.com/report/github.com/hemm-ems/hactl)
[![CodeQL](https://github.com/hemm-ems/hactl/actions/workflows/codeql.yml/badge.svg?branch=main)](https://github.com/hemm-ems/hactl/actions/workflows/codeql.yml)
[![License](https://img.shields.io/github/license/hemm-ems/hactl)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/hemm-ems/hactl)](go.mod)

# Home Assistant control from the command line

## Background

I manage several Home Assistant instances. Logging into each one, hunting through the UI for broken automations, editing YAML by hand — it's fine, but it adds up. You can also wire an LLM into HA directly, but it still feels clunky: the API is chatty, context fills up fast, and there's no good way to keep multiple instances straight.

`hactl` is the tool I created to solve this: a CLI that talks to HA's REST API from outside, works fine by hand, but is really designed to be driven by an LLM. I use hactl daily — or rather, my LLMs do. It works well with local models but also with Claude Code, Codex, or similar. The goal is that you can point an LLM at one or more HA instances and mostly just describe what you want done.

With the [hactl-companion](https://github.com/hemm-ems/hactl-companion) add-on, which runs inside Home Assistant, you can edit all entities including ones the API doesn't normally expose.

## What it does

`hactl` covers most of what you'd normally do through the HA UI or SSH: checking system health, diagnosing automations, inspecting entities, reading and writing config. No SSH access needed, just a long-lived token.

```
hactl health
# → HA 2026.4.3  state=RUNNING  recorder=ok  errors=4
#   location=Home  tz=Europe/Berlin
```

See the [manual](docs/manual.md) for the full command reference.

## How it's built for LLMs

Every response is capped at 500 tokens by default, and compact token estimates are available with `--tokens` (`[~N tok]`). Extended output is opt-in — the idea is that an LLM working through a task shouldn't have its context blown out by a single command.

`hactl rtfm` prints a guide that's specifically written for LLMs: how HA is structured, what the API does and doesn't expose, common pitfalls. The intention is that you can hand an LLM this manual once and it can navigate HA confidently from there.

The manual also delivers itself: when stdout and stderr are both captured (an agent harness, not a terminal), hactl writes it to stderr progressively — the ~1.4k-token core with the first command of a session, each command family's how-to with the first command from that family. stdout is untouched; humans at a terminal see nothing. `HACTL_MANUAL_MODE=off` disables it, `full` sends the whole manual once; sessions are per instance (`HACTL_SESSION`, or a 30-minute idle window).

## How it works

Point any LLM agent at hactl. The manual reaches it automatically (or explicitly via `hactl rtfm`), then it uses hactl commands as tools to answer your questions.

![hactl demo](docs/demo.gif)

```
$ claw "balcony watering didn't run yesterday — why?"

  ● hactl rtfm                            [manual loaded · 6231 tok]
  ● hactl health → HA 2026.4.4  RUNNING  errors=12  companion=ok
  ● hactl auto show balcony_minimum_watering
    on  last=2026-05-22  trc:a7 (condition stop)
  ● hactl trace show trc:a7
    ✓ trigger  ✗ condition: sensor.balcony_soil_moisture = unknown

  Sensor offline since May 22 — numeric condition can't evaluate.
  Likely dead Zigbee battery.
```

The transcript above is illustrative (the GIF is scripted), but it shows the intended shape: the manual arrives once per session (~1.4k-token core, family sections on first use), after which each tool call costs tens of tokens. Tool wrappers in [`integrations/llm/tools.py`](integrations/llm/tools.py) — they expose read-only commands; config writes go through a separate dry-run + `--confirm` step.

## MCP server

`hactl mcp` serves the whole CLI over the [Model Context Protocol](https://modelcontextprotocol.io) on stdio — one `hactl` tool that takes a command line, for clients like Claude Code or Claude Desktop. The full manual is injected with the first tool result (also available via `rtfm` or the `hactl://manual` resource); `--no-manual-inject` turns that off.

```bash
claude mcp add hactl -- hactl mcp --dir ~/.hactl/default
```

```json
{ "mcpServers": { "hactl": { "command": "hactl", "args": ["mcp", "--dir", "/path/to/instance"] } } }
```

The server is read-only by default: mutating commands (`svc call`, `auto apply`, `script apply`, create/delete, …) are rejected. Start it with `hactl mcp --allow-writes` to permit them — the dry-run + `--confirm` write path still applies on top. One instance per server process; pin it with `--dir`.

## CLI vs MCP, measured

A tuning loop (`dev/tuning/`: 12 tasks against a live instance, graded on command choice, call budget, and write discipline, run with a local qwen3.5-122b) compares how the manual reaches the model. Same tasks, two runs per mode:

| manual delivery | tasks passed | manual tok/task | notes |
|---|---|---|---|
| progressive injection (CLI default) | 11, 10 / 12 | ~2.5k | no unconfirmed writes in any run |
| full manual once (`hactl mcp` today) | 10, 8 / 12 | ~7.5k | 3–4× the uncached input, ~2× slower |
| "run `rtfm` first" instruction, no injection | 6, 5 / 12 | 0 | the model actually ran `rtfm` first in 4/12 resp. 1/12 tasks |

To the model, CLI-over-shell and MCP differ only in tool schema (~0.1k vs ~2.5k tokens per request for the bundled multi-function wrappers; MCP's JSON-RPC framing never enters the context). The delivery mode is what matters: an agent with a plain shell tool gets the best-measured setup with zero client configuration. Methodology and full numbers: [LLM tuning notes](docs/llm-tuning.md).

## Safety

Config writes (`auto apply`/`create`/`delete`, `script apply`, templates, helpers, dashboards, registry changes) are dry-run by default and need an explicit `--confirm`. Automation and script configs are validated with HA's own `validate_config` before anything is written, and a backup is saved before every apply write (`hactl auto rollback` undoes automation applies). Template syntax is evaluated by HA's real Jinja engine, not a mock.

Two command families execute immediately, because acting is their purpose: `svc call` (service calls like `light.turn_on`) and `script run`. If you hand an agent unrestricted shell access to hactl, it can call services — the bundled LLM tool wrappers in `integrations/llm/` deliberately expose only read-only commands.

## Multi-instance

Each HA instance gets its own directory with a `.env` file. `hactl` picks up whichever one is active via `--dir`, `HACTL_DIR`, the current directory or its parents (git-style), or `~/.hactl/default/`.

## Install

```bash
# Homebrew (macOS / Linux)
brew install hemm-ems/tap/hactl

# Go
go install github.com/hemm-ems/hactl/cmd/hactl@latest

# Source
git clone https://github.com/hemm-ems/hactl && cd hactl && make build
```

Pre-built binaries for Linux, macOS, and Windows (amd64/arm64) on the [releases page](https://github.com/hemm-ems/hactl/releases/latest). Release checksums are signed with cosign (keyless/OIDC):

```bash
cosign verify-blob --bundle checksums.txt.sig checksums.txt
```

## Setup

```bash
hactl setup
```

`hactl setup` prompts for the HA URL and a long-lived token, tests connectivity, and writes `.env` in the current directory (or the path given via `--dir`). The companion add-on is auto-detected if installed. For scripts and agents there's a non-interactive form: `hactl setup --url http://ha:8123 --token <token>` (use `--token -` to read the token from stdin, `--force` to overwrite).

To create a token: HA → Profile → Long-lived access tokens.

For multiple instances, create one directory per instance with its own `.env` and use `--dir` or `HACTL_DIR` to select it.

## The companion

The HA API doesn't expose everything needed to fully manage a Home Assistant instance — creating, editing, and deleting template entities, for example, isn't available. The [hactl-companion](https://github.com/hemm-ems/hactl-companion) add-on fills that gap.

Install it from HA → Settings → Add-ons, then run `hactl setup` or `hactl health` — the companion URL is discovered automatically by enumerating add-ons through the Supervisor WS proxy (`supervisor/api`).

**Discovery requires a long-lived token created by an HA admin (owner)** and a Supervisor-backed install (HA OS / Supervised). On HA Container (Docker without Supervisor) the WS proxy is not available — set `COMPANION_URL` in `.env` directly. If you get `companion=not found (auth_denied)`, create a new token as an owner. If your reverse proxy strips `/api/hassio/*`, set `COMPANION_URL` in `.env` instead (Settings → Add-ons → hactl companion → Web UI → copy the URL).

**External access works automatically.** When discovery resolves to an Ingress URL (`/api/hassio_ingress/<token>/…`), HA Core proxies straight through to Supervisor, which only honors its own `ingress_session` cookie. hactl asks Supervisor for a session via the WS `supervisor/api` `/ingress/session` endpoint and sets the cookie on each Companion request. Sessions are cached and refreshed on 401, so this is transparent to users.

Run `hactl companion status` to diagnose connectivity.

**Remote lifeline.** The companion can also run a WireGuard tunnel so an instance stays reachable from anywhere. Manage it with `hactl companion wireguard {status,config,up,down}` — configs persist across restarts. Set the add-on's `vpn.enabled` option to have the tunnel come back up on every (re)start, including after a host reboot.

---

[Manual](docs/manual.md) · [Setup](docs/setup.md) · [Local LLMs](docs/local-llm.md) · [LLM tuning notes](docs/llm-tuning.md) · [Testing](docs/testing.md)
