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

.PHONY: build lint test test-int test-companion test-int-discovery test-matrix clean sync-spec check-spec-drift

build:
	go build -ldflags "$(LDFLAGS)" -o hactl ./cmd/hactl

lint:
	golangci-lint run ./...

test:
	go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic

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
