BINARY   := vault-plugin-secrets-harbor
MODULE   := github.com/corelyr-oss/vault-plugin-secrets-harbor
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
GOFLAGS  := -trimpath
PLUGIN_DIR ?= $(CURDIR)/bin/plugins
TOOLS_DIR  := $(CURDIR)/bin/tools

.PHONY: all build build-tools test lint integration dev dev-harbor clean fmt tidy action-test action-build

all: lint test build

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(PLUGIN_DIR)/$(BINARY) ./cmd/$(BINARY)

# Dev helpers (fake Harbor for manual testing; not part of the release).
build-tools:
	go build -o $(TOOLS_DIR)/fakeharbor ./internal/harbor/harbortest/cmd/fakeharbor

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w .

tidy:
	go mod tidy

# Integration tests need Docker (Harbor via compose) and a vault/bao binary on PATH.
integration: build
	go test -tags integration -count=1 -timeout 45m ./test/integration/... -v

# Start a Vault dev server that can load the plugin from bin/plugins/ (see README "Development").
dev: build
	vault server -dev -dev-root-token-id=root -dev-plugin-dir=$(PLUGIN_DIR) -log-level=debug

# Start the in-memory fake Harbor on 127.0.0.1:8089 (admin / Harbor12345).
dev-harbor: build-tools
	$(TOOLS_DIR)/fakeharbor -addr 127.0.0.1:8089

# The GitHub Action lives in action/ and has its own toolchain.
action-test:
	cd action && npm ci && npm run all

action-build:
	cd action && npm ci && npm run build

clean:
	rm -rf bin dist
