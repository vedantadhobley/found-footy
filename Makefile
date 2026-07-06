# Found Footy — build, test, and lint targets.
#
# The host has no Go toolchain — everything runs inside throwaway
# containers per the workspace's Docker-first tooling policy. Repeated
# builds share a module + build cache at ~/.cache/found-footy so we
# don't re-download deps on every invocation.
#
# Dev-stack targets (dev-up, dev-down, dev-logs, dev-restart) land in
# commit 5 when docker-compose.dev.yml exists.

GO_IMAGE       := golang:1.23-bookworm
GOLANGCI_IMAGE := golangci/golangci-lint:latest-alpine

CACHE_DIR   := $(HOME)/.cache/found-footy
DOCKER_ENV  := -e GOCACHE=/gocache -e GOMODCACHE=/gomodcache
DOCKER_VOLS := -v $(PWD):/src -v $(CACHE_DIR)/gocache:/gocache -v $(CACHE_DIR)/gomodcache:/gomodcache
DOCKER_RUN  := docker run --rm $(DOCKER_ENV) $(DOCKER_VOLS) -w /src

GO_RUN       := $(DOCKER_RUN) $(GO_IMAGE)
GOLANGCI_RUN := $(DOCKER_RUN) $(GOLANGCI_IMAGE)

.PHONY: help build test test-race lint fmt vet tidy clean cache-init

help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

cache-init: ## Create the shared Go module + build cache directories
	@mkdir -p $(CACHE_DIR)/gocache $(CACHE_DIR)/gomodcache

# ────── Build ──────

build: cache-init ## Compile every binary + package
	# -buildvcs=false: we're inside a container with root-owned bind mount, so
	# git thinks ownership is dubious. Git SHA is injected via -ldflags in
	# Dockerfile builds per §11, so Go's built-in VCS stamping is redundant.
	$(GO_RUN) go build -buildvcs=false ./...

# ────── Test ──────

test: cache-init ## Run all tests
	$(GO_RUN) go test -buildvcs=false ./...

test-race: cache-init ## Run tests with the race detector
	$(GO_RUN) go test -buildvcs=false -race ./...

# ────── Lint + format ──────

lint: cache-init ## Run golangci-lint against the full tree
	$(GOLANGCI_RUN) golangci-lint run --timeout 5m

fmt: cache-init ## Run gofmt on every .go file (mutates in place)
	$(GO_RUN) gofmt -w -s .

vet: cache-init ## Run go vet
	$(GO_RUN) go vet ./...

# ────── Modules ──────

tidy: cache-init ## Run go mod tidy
	$(GO_RUN) go mod tidy

# ────── Housekeeping ──────

clean: ## Remove the shared Docker cache directory
	rm -rf $(CACHE_DIR)
