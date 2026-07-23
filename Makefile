VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
TESTED_HA ?=
LDFLAGS := -s -w \
	-X 'github.com/hemm-ems/hactl/internal/cmd.version=$(VERSION)' \
	-X 'github.com/hemm-ems/hactl/internal/cmd.commit=$(COMMIT)' \
	-X 'github.com/hemm-ems/hactl/internal/cmd.date=$(DATE)' \
	-X 'github.com/hemm-ems/hactl/internal/cmd.testedHA=$(TESTED_HA)'

COMPANION_DIR  ?= ../hactl-companion
COMPANION_SPEC := $(COMPANION_DIR)/openapi/companion-v1.yaml
VENDORED_SPEC  := testdata/companion-v1.yaml

.PHONY: build lint test test-int test-companion test-int-discovery test-matrix \
        gates require-docker hooks hooks-check clean sync-spec check-spec-drift

build:
	go build -ldflags "$(LDFLAGS)" -o hactl ./cmd/hactl

# golangci-lint is commonly installed to $GOPATH/bin, which is not always on
# PATH. Resolve it either way so `make gates` works on a stock dev machine
# instead of failing before it reaches the tests that matter.
GOLANGCI ?= $(shell command -v golangci-lint 2>/dev/null || echo "$$(go env GOPATH)/bin/golangci-lint")

lint:
	@test -x "$(GOLANGCI)" || { \
	  echo "ERROR: golangci-lint not found (looked on PATH and in $$(go env GOPATH)/bin)."; \
	  echo "Install: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"; \
	  exit 1; }
	$(GOLANGCI) run ./...

test:
	@echo "NOTE: 'make test' is the unit tier only — it starts no Home Assistant."
	@echo "      It is a fast sanity check, never acceptance. Run 'make gates' before you call anything done."
	go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic

# ---------------------------------------------------------------------------
# gates — the ONLY definition of "done".
#
# Every tier below that starts a real Home Assistant container is mandatory,
# locally and in CI, and CI runs these same targets. hactl's job is to report
# what HA actually contains, so a change is only proven by asking a real HA.
# Both root causes found in the 2026-07-23 audit (traces keyed by the wrong
# identifier; device-inherited areas ignored) were invisible to the unit tier
# by construction and only observable against a live instance.
#
# There is deliberately no way to mark a Docker tier optional. If Docker is not
# running, this fails loudly rather than silently narrowing what was verified.
# ---------------------------------------------------------------------------
gates: require-docker lint test test-int test-companion test-int-discovery
	@echo
	@echo "================================================================"
	@echo " ALL GATES GREEN — lint + unit + integration + companion +"
	@echo " discovery, every Docker tier included."
	@echo "================================================================"

require-docker:
	@docker info >/dev/null 2>&1 || { \
	  echo "ERROR: Docker is not running."; \
	  echo "The integration, companion and discovery tiers each start a real"; \
	  echo "Home Assistant container. They are mandatory — a run without them"; \
	  echo "proves nothing about how hactl behaves against HA."; \
	  echo "Start Docker and re-run 'make gates'."; \
	  exit 1; }
	@echo "docker: ok"

# Install the repo's git hooks (pre-push runs the full gates).
#
# This points core.hooksPath at the tracked dev/hooks directory FOR THIS REPO
# ONLY, rather than copying into .git/hooks. Copying is not reliable: a global
# `core.hooksPath` (increasingly common, and set on at least one machine here)
# overrides .git/hooks completely, so the copied hook is never executed and
# enforcement silently does nothing — which is worse than no hook at all,
# because it looks installed.
hooks:
	@git rev-parse --git-dir >/dev/null 2>&1 || { echo "not a git repo"; exit 1; }
	@prev="$$(git config --global --get core.hooksPath || true)"; \
	if [ -n "$$prev" ]; then \
	  echo "note: a global core.hooksPath is set ($$prev)."; \
	  echo "      Overriding it for THIS repo only; your other repos are untouched."; \
	fi
	@git config --local core.hooksPath dev/hooks
	@chmod +x dev/hooks/*
	@echo "hooks active: $$(git rev-parse --show-toplevel)/dev/hooks (repo-local core.hooksPath)"
	@echo "verify with:  make hooks-check"
	@echo "bypass once:  git push --no-verify"

# Prove the hook is actually wired up. `make hooks` used to copy into .git/hooks,
# which a global core.hooksPath silently overrode — so "installed" was not the
# same as "runs". Never trust the install; check it.
hooks-check:
	@path="$$(git config --get core.hooksPath || echo "$$(git rev-parse --git-dir)/hooks")"; \
	echo "git will run hooks from: $$path"; \
	if [ -x "$$path/pre-push" ]; then \
	  echo "pre-push: present and executable — gates will run on push"; \
	else \
	  echo "pre-push: MISSING at $$path/pre-push — run 'make hooks'"; exit 1; \
	fi

test-int:
	go test ./... -tags=integration -count=1 -timeout 300s

test-companion:
	go test -tags=companion -v -count=1 -timeout 300s ./internal/companiontest/...

test-int-discovery:
	go test -tags=companion_discovery -v -count=1 -timeout 300s ./internal/companiontest_discovery/...

test-matrix:
	@echo "Run via CI (see .github/workflows/ci.yml)"
	@echo "Locally: make test-int"

clean:
	rm -f hactl hactl.exe
	go clean -cache

# Copy the companion's generated OpenAPI spec into testdata/ (the CLI's contract).
sync-spec:
	cp $(COMPANION_SPEC) $(VENDORED_SPEC)

# Fail if the vendored spec has drifted from the companion's generated spec.
# CI wires this so a released companion API change can't silently outrun the CLI.
check-spec-drift:
	@diff -u $(VENDORED_SPEC) $(COMPANION_SPEC) \
		|| { echo "ERROR: $(VENDORED_SPEC) drifted from companion; run: make sync-spec"; exit 1; }
