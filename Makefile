BINARY    := infai
MODULE    := github.com/dipankardas011/infai
MAIN      := ./cmd/inference
GO        ?= go
CGO_ENABLED ?= 1

VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X $(MODULE)/internal/config.version=$(VERSION)

BUILD_DIR := build

# Compile a single binary. Usage: $(call go-build,<name>,<main package>)
define go-build
	@mkdir -p $(BUILD_DIR)
	@CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$1 $2
endef

# Run a single binary. Usage: $(call go-run,<main package>)
define go-run
	@CGO_ENABLED=$(CGO_ENABLED) $(GO) run -ldflags "$(LDFLAGS)" $1
endef

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: inference-build
inference-build: ## Compile the inference binary into ./$(BUILD_DIR)
	$(call go-build,$(BINARY),$(MAIN))

.PHONY: inference-run
inference-run: ## Build and run inference
	$(call go-run,$(MAIN))

.PHONY: inference-install
inference-install: ## Install inference into GOBIN
	@CGO_ENABLED=$(CGO_ENABLED) $(GO) install -trimpath -ldflags "$(LDFLAGS)" $(MAIN)

.PHONY: agent-run
agent-run: ## Run the agent
	$(call go-run, ./cmd/agent)

.PHONY: test
test: ## Run all tests
	CGO_ENABLED=$(CGO_ENABLED) $(GO) test ./...

.PHONY: test-race
test-race: ## Run all tests with the race detector
	CGO_ENABLED=$(CGO_ENABLED) $(GO) test -race ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format all Go source files
	gofmt -w $$($(GO) list -f '{{.Dir}}' ./... | xargs -I{} find {} -name '*.go')

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is not formatted
	@out=$$(gofmt -l $$($(GO) list -f '{{.Dir}}' ./... | xargs -I{} find {} -name '*.go')); \
	if [ -n "$$out" ]; then \
		echo "The following files are not formatted:"; \
		echo "$$out"; \
		exit 1; \
	fi

.PHONY: lint
lint: ## Run golangci-lint (if installed)
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not found; skipping (install: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)"; \
	fi

.PHONY: tidy
tidy: ## Tidy go.mod/go.sum
	$(GO) mod tidy

.PHONY: check
check: fmt-check vet test ## Run all static checks and tests

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BUILD_DIR)

.PHONY: version
version: ## Print the version that would be baked into the binary
	@echo $(VERSION)

.PHONY: release
release: ## Tag and push a release: make release VERSION=v0.1.0
	@test -n "$(VERSION)" || (echo "Usage: make release VERSION=v0.1.0" && exit 1)
	@git tag -a $(VERSION) -m "Release $(VERSION)"
	@git push origin $(VERSION)

.PHONY: goreleaser
goreleaser: ## Run goreleaser locally (needs goreleaser installed)
	@command -v goreleaser >/dev/null 2>&1 || (echo "goreleaser not found" && exit 1)
	goreleaser release --clean
