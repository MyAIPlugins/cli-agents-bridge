# cli-agents-bridge Makefile
#
# Defaults: builds darwin-arm64 binary into bin/cab-bridge.
# Cross-compile targets produce static (CGO_ENABLED=0) binaries portable
# across macOS arm64 and Linux amd64/arm64.

BINARY      := cab-bridge
PKG         := github.com/myAIPlugins/cli-agents-bridge/cmd/cab-bridge
BIN_DIR     := bin
PLUGIN_DIR  := plugins/cli-agents-bridge
# Version comes from the git tag (single source of truth, injected into
# main.version). `--always` falls back to a short SHA when off-tag; `dev` only
# when not in a git repo (e.g. source tarball build).
GIT_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
VERSION     := $(if $(GIT_VERSION),$(GIT_VERSION),dev)

GO_FLAGS    := -trimpath -ldflags="-s -w -X main.version=$(VERSION)"

# How many TEST BINARIES run at once. Go's default is GOMAXPROCS — 8 here — so
# one `make test-race` alone assumes it owns the machine, and three agents
# building on one laptop reached load 10.85 with an e2e dying on a 300s timeout.
#
# 4 is measured, not guessed (two runs each, same load window):
#
#   -p 8 (default)  9.09s      -p 2  14.55s
#   -p 4            9.26s      -p 1  26.99s
#
# Half the footprint for 170ms, because the wall clock is set by the slowest
# single package (cmd/cab-bridge, ~9s) and 4 slots still cover every slow one.
#
# It is NOT GOMAXPROCS: the test binaries are separate processes, each with its
# own full GOMAXPROCS, so the concurrency INSIDE each test — the thing a race
# detector explores — is untouched. Throttling GOMAXPROCS instead would weaken
# exactly what the gate exists to verify.
#
# Override on an idle machine: make test-race GO_TEST_PARALLEL=8
GO_TEST_PARALLEL ?= 4

.PHONY: help build test test-race cross-compile-all install-dev install-plugin lint clean

help: ## Show this help
	@echo "cli-agents-bridge — make targets"
	@echo ""
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build binary for host platform (darwin-arm64 on Alan's machine)
	@mkdir -p $(BIN_DIR)
	go build $(GO_FLAGS) -o $(BIN_DIR)/$(BINARY) $(PKG)
	@echo "built: $(BIN_DIR)/$(BINARY) ($(VERSION))"

# -count=1 disables the test cache, and it is not optional here (LL-11, twice):
# without it Go serves untouched packages from cache and prints `(cached)`, so a
# change to a SHARED struct or helper never re-runs the tests of the packages
# that use it but that nobody opened. Both times the result was a green declared
# by one side and red when the other re-ran it. A gate that may answer from
# cache is not a gate; the price is ~9s instead of ~0s, and ~0s was never real.
test: ## Run unit + integration tests
	go test -count=1 -p $(GO_TEST_PARALLEL) ./...

test-race: ## Run tests with race detector (CI gate)
	go test -race -count=1 -p $(GO_TEST_PARALLEL) ./...

cross-compile-all: ## Cross-compile darwin-{arm64,amd64} + linux-{amd64,arm64} (no cgo) — matches .goreleaser.yml + ci.yml
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build $(GO_FLAGS) -o $(BIN_DIR)/$(BINARY)-darwin-arm64 $(PKG)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build $(GO_FLAGS) -o $(BIN_DIR)/$(BINARY)-darwin-amd64 $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(GO_FLAGS) -o $(BIN_DIR)/$(BINARY)-linux-amd64  $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(GO_FLAGS) -o $(BIN_DIR)/$(BINARY)-linux-arm64  $(PKG)
	@echo "cross-compile artifacts:"
	@ls -lh $(BIN_DIR)/$(BINARY)-*

install-dev: build ## Symlink local binary into ~/.local/bin for --plugin-dir development
	@mkdir -p $$HOME/.local/bin
	@ln -sf $(PWD)/$(BIN_DIR)/$(BINARY) $$HOME/.local/bin/$(BINARY)
	@echo "symlinked: $$HOME/.local/bin/$(BINARY) -> $(PWD)/$(BIN_DIR)/$(BINARY)"
	@echo "ensure \$$HOME/.local/bin is in your PATH"

# NOTE: VERSION now derives from `git describe`. To ship the committed plugin
# binary with a clean release version, run install-plugin from a checkout of the
# tag (e.g. after `git checkout v0.2.3`); off-tag it embeds a <ver>-<n>-g<sha>.
install-plugin: build ## Copy binary into plugins/cli-agents-bridge/bin/ for marketplace install (cp, NOT symlink — Claude Code cache install copies files, symlink targets would dangle)
	@mkdir -p $(PLUGIN_DIR)/bin
	@cp -f $(BIN_DIR)/$(BINARY) $(PLUGIN_DIR)/bin/$(BINARY)
	@chmod +x $(PLUGIN_DIR)/bin/$(BINARY)
	@echo "installed: $(PLUGIN_DIR)/bin/$(BINARY) ($(VERSION))"
	@echo "next: from a fresh Claude Code session, run:"
	@echo "  /plugin marketplace add $(PWD)"
	@echo "  /plugin install cli-agents-bridge@cli-agents-bridge-marketplace"

# STATICCHECK IS REQUIRED, and looked for where `go install` actually puts it.
#
# This used to be one line:
#
#	@command -v staticcheck >/dev/null 2>&1 && staticcheck ./... || echo "not installed, skipping"
#
# which had three defects, and the third is the one that mattered:
#
#  1. `command -v` only searches $PATH, and `go install` writes to
#     $(go env GOPATH)/bin, which is not on the PATH of this machine. The tool
#     was installed and the gate could not see it.
#  2. The `||` catches BOTH "not found" AND "found, and it failed". With
#     staticcheck present and five warnings, `make lint` printed all five and
#     then announced "staticcheck not installed, skipping" — the wrong cause,
#     stated after the evidence to the contrary had scrolled past.
#  3. And it exited 0. A green `make lint` with five live warnings, which is how
#     they survived nine rounds of gates.
#
# So: resolve the binary in PATH *and* in GOPATH/bin, fail loudly when it is
# missing instead of narrating the skip, and let its exit code through. A gate
# that skips a check and mentions it in an echo nobody reads is not a gate.
# WHERE `go install` ACTUALLY PUTS IT: $GOBIN when set, $GOPATH/bin otherwise.
# The first version of this line only knew about GOPATH/bin, so with GOBIN set
# the gate looked where the binary is NOT — and then printed an install command
# that would put it there again. A refusal with a remediation that cannot fix it
# is worse than a refusal: it sends the reader in a loop (CRI, third instance of
# this class today).
STATICCHECK_DIR := $(shell go env GOBIN)
ifeq ($(STATICCHECK_DIR),)
STATICCHECK_DIR := $(shell go env GOPATH)/bin
endif
# Single source for the pinned version, shared with .github/workflows/ci.yml, so
# the local gate and the gate that authorises merges cannot drift onto two
# different linters.
STATICCHECK_VERSION := $(shell cat .staticcheck-version)
STATICCHECK ?= $(shell command -v staticcheck 2>/dev/null || echo $(STATICCHECK_DIR)/staticcheck)

lint: ## Run go vet + staticcheck (required; version pinned in .staticcheck-version)
	go vet ./...
	@if [ ! -x "$(STATICCHECK)" ]; then \
		echo "make lint: staticcheck not found (looked in \$$PATH and $(STATICCHECK_DIR))"; \
		echo "  install it:  go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)"; \
		echo "  this is a hard failure on purpose: a lint that silently skips is not a lint"; \
		exit 1; \
	fi
	@# EVERY EXPANSION OF $(STATICCHECK) IS QUOTED, including this probe and the
	@# invocation below. The existence guard was quoted and these two were not, so
	@# a GOBIN whose basename contains a space split the path: the correct binary,
	@# reporting exactly the pinned version, was never invoked — the gate read
	@# "unknown", refused it, and advised reinstalling it in the same place.
	@#
	@# That is F-124's own defect, in the Makefile written to verify F-124's fix.
	@# Found by a critic applying the method to our tooling, which was the one
	@# place none of us had pointed it at. The class does not close by knowing it;
	@# it closes where somebody runs it.
	@#
	@# AND IT HAS TO BE THE PINNED ONE. Resolving a path is not the same as
	@# running the version the project pinned: whatever sits first in $$PATH was
	@# executed regardless, so an old binary gave a false green and a new one a
	@# false red — while the commit that introduced .staticcheck-version claimed
	@# a single source. It governed the CI install and this help text, not the
	@# binary that actually ran (CRI).
	@got="$$("$(STATICCHECK)" --version 2>/dev/null | sed -E 's/.*\((v[0-9.]+)\).*/\1/')"; \
	if [ "$$got" != "$(STATICCHECK_VERSION)" ]; then \
		echo "make lint: staticcheck version mismatch"; \
		echo "  pinned  (.staticcheck-version): $(STATICCHECK_VERSION)"; \
		echo "  running ($(STATICCHECK)): $${got:-unknown}"; \
		echo "  install the pinned one:  go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)"; \
		exit 1; \
	fi
	"$(STATICCHECK)" ./...

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
