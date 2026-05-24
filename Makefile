# cli-agents-bridge Makefile
#
# Defaults: builds darwin-arm64 binary into bin/cab-bridge.
# Cross-compile targets produce static (CGO_ENABLED=0) binaries portable
# across macOS arm64 and Linux amd64/arm64.

BINARY      := cab-bridge
PKG         := github.com/myAIPlugins/cli-agents-bridge/cmd/cab-bridge
BIN_DIR     := bin
VERSION     := $(shell go run ./cmd/cab-bridge --version 2>/dev/null || echo 0.2.0-dev)

GO_FLAGS    := -trimpath -ldflags="-s -w"

.PHONY: help build test test-race cross-compile-all install-dev lint clean

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

cross-compile-all: ## Cross-compile darwin-arm64 + linux-amd64 + linux-arm64 (no cgo)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build $(GO_FLAGS) -o $(BIN_DIR)/$(BINARY)-darwin-arm64 $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(GO_FLAGS) -o $(BIN_DIR)/$(BINARY)-linux-amd64  $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(GO_FLAGS) -o $(BIN_DIR)/$(BINARY)-linux-arm64  $(PKG)
	@echo "cross-compile artifacts:"
	@ls -lh $(BIN_DIR)/$(BINARY)-*

install-dev: build ## Symlink local binary into ~/.local/bin for --plugin-dir development
	@mkdir -p $$HOME/.local/bin
	@ln -sf $(PWD)/$(BIN_DIR)/$(BINARY) $$HOME/.local/bin/$(BINARY)
	@echo "symlinked: $$HOME/.local/bin/$(BINARY) -> $(PWD)/$(BIN_DIR)/$(BINARY)"
	@echo "ensure \$$HOME/.local/bin is in your PATH"

lint: ## Run go vet (staticcheck optional — install with: go install honnef.co/go/tools/cmd/staticcheck@latest)
	go vet ./...
	@command -v staticcheck >/dev/null 2>&1 && staticcheck ./... || echo "staticcheck not installed, skipping (run: go install honnef.co/go/tools/cmd/staticcheck@latest)"

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
