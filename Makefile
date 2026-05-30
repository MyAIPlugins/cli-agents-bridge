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

.PHONY: help build test test-race cross-compile-all install-dev install-plugin lint clean

help: ## Show this help
	@echo "cli-agents-bridge — make targets"
	@echo ""
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build binary for host platform (darwin-arm64 on Alan's machine)
	@mkdir -p $(BIN_DIR)
	go build $(GO_FLAGS) -o $(BIN_DIR)/$(BINARY) $(PKG)
	@echo "built: $(BIN_DIR)/$(BINARY) ($(VERSION))"

test: ## Run unit + integration tests
	go test ./...

test-race: ## Run tests with race detector (CI gate)
	go test -race ./...

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

lint: ## Run go vet (staticcheck optional — install with: go install honnef.co/go/tools/cmd/staticcheck@latest)
	go vet ./...
	@command -v staticcheck >/dev/null 2>&1 && staticcheck ./... || echo "staticcheck not installed, skipping (run: go install honnef.co/go/tools/cmd/staticcheck@latest)"

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
