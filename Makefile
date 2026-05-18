VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
TESTED_HA ?=
LDFLAGS := -s -w \
	-X 'github.com/hemm-ems/hactl/internal/cmd.version=$(VERSION)' \
	-X 'github.com/hemm-ems/hactl/internal/cmd.commit=$(COMMIT)' \
	-X 'github.com/hemm-ems/hactl/internal/cmd.date=$(DATE)' \
	-X 'github.com/hemm-ems/hactl/internal/cmd.testedHA=$(TESTED_HA)'

.PHONY: build lint test test-int test-companion test-matrix clean

build:
	go build -ldflags "$(LDFLAGS)" -o hactl ./cmd/hactl

lint:
	golangci-lint run ./...

test:
	go test ./... -count=1

test-int:
	go test ./... -tags=integration -count=1 -timeout 300s

test-companion:
	go test -tags=companion -v -count=1 -timeout 300s ./internal/companiontest/...

test-matrix:
	@echo "Run via CI (see .github/workflows/ci.yml)"
	@echo "Locally: make test-int"

clean:
	rm -f hactl hactl.exe
	go clean -cache
