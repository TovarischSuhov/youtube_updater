# youtube-updater — build/test/lint entry points.
BINARY  := youtube-updater
GO      ?= go
ARGS    ?=

.PHONY: help build test vet fmt fmt-check lint run clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary (./youtube-updater)
	$(GO) build -o $(BINARY) ./cmd

test: ## Run all tests
	$(GO) test ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format all Go sources (gofmt, in place)
	$(GO) fmt ./...

fmt-check: ## Verify formatting without modifying files
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "$$unformatted"; exit 1; fi

lint: ## Run golangci-lint (requires golangci-lint v2.12.2)
	golangci-lint run

run: ## Run the tool (make run ARGS="--dry-run")
	@$(GO) run ./cmd $(ARGS)

clean: ## Remove build artifacts
	rm -f $(BINARY)
