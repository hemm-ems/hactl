## hactl

[![CI](https://github.com/hemm-ems/hactl/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/hemm-ems/hactl/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/hemm-ems/hactl)](https://github.com/hemm-ems/hactl/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/hemm-ems/hactl)](https://goreportcard.com/report/github.com/hemm-ems/hactl)
[![CodeQL](https://github.com/hemm-ems/hactl/actions/workflows/codeql.yml/badge.svg?branch=main)](https://github.com/hemm-ems/hactl/actions/workflows/codeql.yml)
[![License](https://img.shields.io/github/license/hemm-ems/hactl)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/hemm-ems/hactl)](go.mod)

# Home Assistant control, built for agentic workflows

Stop burning tokens on raw APIs. **hactl** is purpose-built for LLM-driven control of Home Assistant: minimal payloads, precise queries, no structural noise.

## What it does
- Diagnose automations: failures, trigger frequency, dead rules  
- Inspect entities: anomalies, outages, signal quality  
- Read/Write analyse attributes, scripts, labels, etc.
- Spider along your feature cluster
- Generate and update dashboards (beta)  
- More: see [manual](docs/manual.md)

## Why it’s different
- **Token-efficient by design**: every response starts with `[~N tok]`, capped at 500 tokens by default (`--tokensmax`)  
- **On-target validation**: executes against the real Jinja engine — no mock layers  
- **Deterministic safety**: rollback by default, explicit commit required  
- **Low traffic footprint**: aggressive request caching, filesystem-backed  
- **Multi-instance ready**: one directory per HA instance, `.env` scoped

## Engineering
- Go, single static binary, zero runtime deps  
- Integration-tested against real Home Assistant via ephemeral Docker instances  
- Security-checked

## Setup
Drop a `.env` with your token into a directory. Run. Done.

---



**[Manual (LLM usage guide)](docs/manual.md)** · **[LLM tuning notes](docs/llm-tuning.md)** · **[Testing guide](docs/testing.md)**

## Install

```bash
# Homebrew (macOS / Linux)
brew install hemm-ems/tap/hactl

# Go
go install github.com/hemm-ems/hactl/cmd/hactl@latest

# Source
git clone https://github.com/hemm-ems/hactl && cd hactl && make build
```

Pre-built binaries for Linux, macOS, and Windows (amd64/arm64) are attached to each [GitHub release](https://github.com/hemm-ems/hactl/releases/latest).

### Verify release signatures

All release checksums are signed with [cosign](https://github.com/sigstore/cosign) (keyless / OIDC).

```bash
cosign verify-blob \
  --bundle checksums.txt.sig \
  checksums.txt
```

## Setup

```bash
mkdir -p ~/ha/home && cd ~/ha/home
cat > .env << 'EOF'
HA_URL=http://homeassistant.local:8123
HA_TOKEN=<long_lived_access_token>
EOF

hactl health
# → HA 2026.4.3  state=RUNNING  recorder=ok  errors=4
#   location=Home  tz=Europe/Berlin
```

Instance discovery: `--dir` flag → `HACTL_DIR` env → CWD (if `.env` exists) → `~/.hactl/default/`.
