---
name: run-tests
description: 'How to run tests for hactl. Use when asked to "run tests", "run all tests", "execute tests", "check tests", or "verify tests". Covers unit tests, integration tests, and golden-file regeneration.'
---

# Running hactl Tests

## Quick Answer

"Run all tests" = `make test-int`. That is the only correct command.

## Prerequisites

**Docker must be running.** Verify first:

```bash
docker info
```

If this fails, Docker Desktop is not running. Start it before proceeding.

## Procedure

```bash
# 1. Verify Docker is running
docker info

# 2. Run all tests (unit + integration, ~2 min first run, ~60s cached)
make test-int
```

Expected: 199 unit tests + 202 integration tests pass. The integration tests start a real Home Assistant container via Docker (Testcontainers).


## Unit Tests Only (no Docker)

```bash
make test
```

This is faster (~5s) but **does NOT cover the 202 integration tests**. Only use this when Docker is unavailable. It does not count as a full test run.

## What NOT to Run

Do **not** run `go test ./...` directly — it silently skips all 202 integration tests because they require `-tags=integration`. Always use `make test-int`.
