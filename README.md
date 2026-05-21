## hactl

[![CI](https://github.com/hemm-ems/hactl/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/hemm-ems/hactl/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/hemm-ems/hactl)](https://github.com/hemm-ems/hactl/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/hemm-ems/hactl)](https://goreportcard.com/report/github.com/hemm-ems/hactl)
[![CodeQL](https://github.com/hemm-ems/hactl/actions/workflows/codeql.yml/badge.svg?branch=main)](https://github.com/hemm-ems/hactl/actions/workflows/codeql.yml)
[![License](https://img.shields.io/github/license/hemm-ems/hactl)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/hemm-ems/hactl)](go.mod)

# Home Assistant control, built for agentic workflows

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

Every response reports its own token count (`[~N tok]`) and is capped at 500 tokens by default. Extended output is available but opt-in — the idea is that an LLM working through a task shouldn't have its context blown out by a single command.

`hactl manual` prints a guide that's specifically written for LLMs: how HA is structured, what the API does and doesn't expose, common pitfalls. The intention is that you can hand an LLM this manual once and it can navigate HA confidently from there.

## Safety

Syntax validation runs against HA's real Jinja engine, not a mock. If the config is invalid, HA says so before anything is written. Changes are staged and require an explicit commit — the default is always safe.

## Multi-instance

Each HA instance gets its own directory with a `.env` file. `hactl` picks up whichever one is active via `--dir`, `HACTL_DIR`, the current working directory, or `~/.hactl/default/`.

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

`hactl setup` prompts for the HA URL and a long-lived token, tests connectivity, and writes `~/.hactl/default/.env`. The companion add-on is auto-detected if installed.

To create a token: HA → Profile → Long-lived access tokens.

For multiple instances, create one directory per instance with its own `.env` and use `--dir` or `HACTL_DIR` to select it.

## The companion

The HA API doesn't expose everything needed to fully manage a Home Assistant instance — creating, editing, and deleting template entities, for example, isn't available. The [hactl-companion](https://github.com/hemm-ems/hactl-companion) add-on fills that gap.

Install it from HA → Settings → Add-ons, then run `hactl setup` or `hactl health` — the companion URL is discovered automatically by enumerating add-ons through the Supervisor WS proxy (`hassio/api`).

**Discovery requires a long-lived token created by an HA admin (owner)** and a Supervisor-backed install (HA OS / Supervised). On HA Container (Docker without Supervisor) the WS proxy is not available — set `COMPANION_URL` in `.env` directly. If you get `companion=not found (auth_denied)`, create a new token as an owner. If your reverse proxy strips `/api/hassio/*`, set `COMPANION_URL` in `.env` instead (Settings → Add-ons → hactl companion → Web UI → copy the URL).

**External access works automatically.** When discovery resolves to an Ingress URL (`/api/hassio_ingress/<token>/…`), hactl signs each request via the HA WS `auth/sign_path` command — the long-lived token alone is not accepted by HA's Ingress route, but the signed `authSig` query parameter is. Signatures expire after 30 seconds; hactl re-signs on every attempt and on any 401 retry, so this is transparent to users.

Run `hactl companion status` to diagnose connectivity.

---

[Manual](docs/manual.md) · [LLM tuning notes](docs/llm-tuning.md) · [Testing](docs/testing.md)