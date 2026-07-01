VERSION := $(shell git describe --tags --always --dirty)
COMMIT  := $(shell git rev-parse HEAD)
TAG     := $(shell git describe --exact-match --tags HEAD 2>/dev/null)
DIRTY   := $(shell test -n "$$(git status --porcelain)" && echo true || echo false)
LDFLAGS := -X github.com/bawdo/caddy_shack/internal/version.Version=$(VERSION) \
           -X github.com/bawdo/caddy_shack/internal/version.Commit=$(COMMIT) \
           -X github.com/bawdo/caddy_shack/internal/version.Tag=$(TAG) \
           -X github.com/bawdo/caddy_shack/internal/version.Dirty=$(DIRTY)

.DEFAULT_GOAL := help

build: ## Build the caddy_shack binary into bin/
	go build -ldflags "$(LDFLAGS)" -o bin/caddy_shack .

install: ## Install caddy_shack into the Go bin directory
	go install -ldflags "$(LDFLAGS)" .

test: ## Run the unit test suite
	go test ./...

lint: ## Run golangci-lint over the whole module
	golangci-lint run

pre-ci: ## Run the pre-CI checks (gofmt, vet, lint, test)
	./scripts/pre-ci-check.sh

pre-ci-fix: ## Run the pre-CI checks, auto-fixing gofmt
	./scripts/pre-ci-check.sh --fix gofmt

help: ## List the available make targets and what they do
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "} {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build install test lint pre-ci pre-ci-fix help
