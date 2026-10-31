VERSION := $(shell git describe --tags --always --dirty)
COMMIT  := $(shell git rev-parse HEAD)
TAG     := $(shell git describe --exact-match --tags HEAD 2>/dev/null)
DIRTY   := $(shell test -n "$$(git status --porcelain)" && echo true || echo false)
LDFLAGS := -X github.com/bawdo/caddy_shack/internal/version.Version=$(VERSION) \
           -X github.com/bawdo/caddy_shack/internal/version.Commit=$(COMMIT) \
           -X github.com/bawdo/caddy_shack/internal/version.Tag=$(TAG) \
           -X github.com/bawdo/caddy_shack/internal/version.Dirty=$(DIRTY)

VULN_VERSION := v1.9.0

# go test -fuzz accepts one target per invocation, so make fuzz runs these
# (<package>:<Name>) in turn for FUZZTIME each.
FUZZTIME ?= 15s
FUZZ_TARGETS := \
	./internal/certstore:FuzzValidDomain \
	./internal/state:FuzzLoad \
	./internal/app:FuzzValidLANName \
	./internal/app:FuzzValidateCertDomain \
	./internal/docker:FuzzParseContainerInfo \
	./internal/docker:FuzzParseBridgeGateway \
	./internal/docker:FuzzParseContainerStatus

.DEFAULT_GOAL := help

build: ## Build the caddy_shack binary into bin/
	go build -ldflags "$(LDFLAGS)" -o bin/caddy_shack .

install: ## Install caddy_shack into the Go bin directory
	go install -ldflags "$(LDFLAGS)" .

test: ## Run the unit test suite
	go test ./...

lint: ## Run golangci-lint over the whole module
	golangci-lint run

vuln: ## Run govulncheck over the module
	go run golang.org/x/vuln/cmd/govulncheck@$(VULN_VERSION) ./...

integration: ## Run the Docker-backed integration tests
	go test -tags integration -count=1 -timeout 10m -v ./test/integration/...

fuzz: ## Run every fuzz target for FUZZTIME each (default 15s)
	@set -e; for t in $(FUZZ_TARGETS); do \
		pkg=$${t%%:*}; name=$${t##*:}; \
		echo "==> $$name ($$pkg, $(FUZZTIME))"; \
		go test "$$pkg" -run='^$$' -fuzz="^$$name\$$" -fuzztime=$(FUZZTIME); \
	done

pre-ci: ## Run the pre-CI checks (gofmt, vet, lint, test)
	./scripts/pre-ci-check.sh

pre-ci-fix: ## Run the pre-CI checks, auto-fixing gofmt
	./scripts/pre-ci-check.sh --fix gofmt

test-scripts: ## Run the shell script test suites
	@for t in $$(ls scripts/*_test.sh | sort); do echo "==> $$t"; bash "$$t" || exit 1; done

changelog: ## Check CHANGELOG.md is well formed and matches the tags
	scripts/changelog.sh check

release: ## Tag a release: make release RELEASE_VERSION=X.Y.Z (never pushes)
	@test -n "$(RELEASE_VERSION)" || { echo "usage: make release RELEASE_VERSION=X.Y.Z"; exit 1; }
	scripts/release.sh $(RELEASE_VERSION)

help: ## List the available make targets and what they do
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "} {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build install test test-scripts lint vuln fuzz integration changelog release pre-ci pre-ci-fix help
