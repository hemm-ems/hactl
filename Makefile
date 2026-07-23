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
        gates require-docker hooks clean sync-spec check-spec-drift

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
hooks:
	@git rev-parse --git-dir >/dev/null 2>&1 || { echo "not a git repo"; exit 1; }
	@install -m 0755 dev/hooks/pre-push "$$(git rev-parse --git-dir)/hooks/pre-push"
	@echo "installed pre-push hook -> $$(git rev-parse --git-dir)/hooks/pre-push"
	@echo "bypass for a work-in-progress branch with: git push --no-verify"

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
