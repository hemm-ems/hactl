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

.PHONY: build lint check-markers deadcode tools test test-assert-floor test-surface surfaces \
        test-int test-companion test-int-discovery test-matrix gates require-docker \
        testcount hooks hooks-check clean sync-spec check-spec-drift

build:
	go build -ldflags "$(LDFLAGS)" -o hactl ./cmd/hactl

# Tool versions live here so the Makefile and CI cannot pin different ones.
# CI runs `make tools` and then the same targets a developer runs locally.
GOLANGCI_VERSION ?= v2.11.4
DEADCODE_VERSION ?= v0.48.0

# Both tools are commonly installed to $GOPATH/bin, which is not always on
# PATH. Resolve them either way so `make gates` works on a stock dev machine
# instead of failing before it reaches the tests that matter.
GOLANGCI ?= $(shell command -v golangci-lint 2>/dev/null || echo "$$(go env GOPATH)/bin/golangci-lint")
DEADCODE ?= $(shell command -v deadcode 2>/dev/null || echo "$$(go env GOPATH)/bin/deadcode")

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	go install golang.org/x/tools/cmd/deadcode@$(DEADCODE_VERSION)

# Every build configuration the gates compile must also be linted.
#
# golangci-lint only reads files whose build constraints the tags it was given
# satisfy. Running it bare — as this target used to — means every file behind
# `//go:build integration`, `//go:build companion` or
# `//go:build companion_discovery` is invisible to every linter: the whole
# Docker tier. Adding the tags surfaced 63 findings that no gate had ever
# reported, including a `TestMain` that leaked a file holding a real HA token
# on every setup failure, two dead harness functions, and a deprecated
# reverse-proxy hook.
#
# The four invocations below mirror the four test targets one for one, so a
# file any gate compiles is a file some lint invocation reads. They are
# deliberately NOT collapsed into a single `--build-tags=a,b,c` run: that
# combination is not a build that ever happens, and it lets a symbol used only
# by the discovery tier look "used" in the companion tier.
#
# All four run even when an earlier one fails, for the same reason
# `issues.uniq-by-line: false` is set in .golangci.yml: a gate has to report
# everything it knows in one run. Stopping at the first red invocation would
# hide the companion tier's findings behind an untagged one and turn a single
# fix into four round trips.
LINT_TAGSETS := untagged integration companion companion_discovery

# check-markers — a [NEEDS ORACLE: ...] marker records an assumption about HA
# that has not been verified against a live instance. Markers may exist on a
# branch; they may not merge. Resolve by probing, then delete the marker.
check-markers:
	@if git grep -n --untracked "NEEDS ORACLE" -- ':!Makefile' ':!AGENTS.md'; then \
	  echo "ERROR: unresolved [NEEDS ORACLE] markers — probe a live HA, then remove them."; \
	  exit 1; \
	fi

lint: check-markers
	@test -x "$(GOLANGCI)" || { \
	  echo "ERROR: golangci-lint not found (looked on PATH and in $$(go env GOPATH)/bin)."; \
	  echo "Install: make tools"; \
	  exit 1; }
	@status=0; \
	for tags in $(LINT_TAGSETS); do \
	  if [ "$$tags" = untagged ]; then \
	    echo "==> $(GOLANGCI) run ./..."; \
	    $(GOLANGCI) run ./... || status=1; \
	  else \
	    echo "==> $(GOLANGCI) run --build-tags=$$tags ./..."; \
	    $(GOLANGCI) run --build-tags=$$tags ./... || status=1; \
	  fi; \
	done; \
	if [ "$$status" -ne 0 ]; then \
	  echo "ERROR: lint failed in at least one build configuration (see above)."; \
	fi; \
	exit $$status

# Fail when a function is unreachable from the hactl binary and not on the
# recorded allowlist. This is the structural defense against the escape
# mechanism that cost the most: code the command tree no longer reaches, kept
# alive and "covered" by its own tests. See dev/deadcode-gate.sh.
deadcode:
	@DEADCODE="$(DEADCODE)" ./dev/deadcode-gate.sh

test:
	@echo "NOTE: 'make test' is the unit tier only — it starts no Home Assistant."
	@echo "      It is a fast sanity check, never acceptance. Run 'make gates' before you call anything done."
	go test ./... -count=1 -coverprofile=coverage.out -covermode=atomic

# test-assert-floor — H-19, the assertion floor. It parses every test file in
# every tier from disk (so the build-tag-gated Docker tiers are covered too) and
# fails any test that can only fail by crashing. It needs no Docker and no HA.
#
# `make test` already reaches it as one package among ./... . It is named here
# anyway, and named before the tiers it judges, so that the rule is wired on
# purpose: the floor is what stops a new "run the command, discard the answer"
# test from being added, and that must not depend on nobody ever narrowing what
# the unit tier compiles.
test-assert-floor:
	go test ./internal/testaudit/... -count=1

# test-surface — the closure gates. Every other gate in this repository answers
# "does the thing I fixed stay fixed?". This one answers "did I fix every place
# this applies?", which nothing answered before, and which is the question all
# four defects reported against v2026.7.12 turned on: each was the unfixed half
# of a fix shipped in the same release.
#
# A surface is derived mechanically — from the cobra tree, from the source, from
# INVARIANTS.md — and a manifest in dev/surfaces/ must disposition every site on
# it as proven, knowingly exempt, or recorded debt. A site nobody has considered
# fails the build the day it appears. Debt is legal; invisible debt is not.
#
# Run with -v to read the outstanding debt on each surface. See
# dev/surfaces/README.md. Needs no Docker and no HA.
test-surface:
	go test ./internal/surfaceaudit/... -count=1 -v
	go test ./internal/cmd/ -count=1 -run 'Surface|FilterFlagsAgreeOnCase' -v

# surfaces — print every ledger without judging it, for deciding what to work on.
surfaces:
	@go test ./internal/surfaceaudit/... -count=1 -v -run 'IsClosed' 2>&1 | grep -vE '^(=== RUN|--- (PASS|FAIL)|PASS|FAIL|ok)' || true
	@go test ./internal/cmd/ -count=1 -v -run 'SurfaceIsClosed' 2>&1 | grep -vE '^(=== RUN|--- (PASS|FAIL)|PASS|FAIL|ok)' || true

# testcount — the per-tier test counts, derived (TC-7). docs/testing.md states no
# count of its own; it points here, because the four it used to state had all
# drifted and three different hand-counting methods disagreed about the right
# correction. Prints `<tier> <count>` per line. See dev/testcount.sh for why the
# assertion-floor gate is the oracle and `go test -tags=<tier> -list` is not.
# Needs no Docker and no HA.
testcount:
	@./dev/testcount.sh

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
gates: require-docker lint deadcode test-assert-floor test-surface test test-int test-companion test-int-discovery
	@echo
	@echo "================================================================"
	@echo " ALL GATES GREEN — lint (every build tag) + deadcode + assertion"
	@echo " floor + surface closure + unit + integration + companion +"
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
