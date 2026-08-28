# Found Footy — build, test, and lint targets.
#
# The host has no Go toolchain — everything runs inside throwaway
# containers per the workspace's Docker-first tooling policy. Repeated
# builds share a module + build cache at ~/.cache/found-footy so we
# don't re-download deps on every invocation.
#
GO_VERSION             := 1.25.11
GOLANGCI_LINT_VERSION  := 2.12.2
GO_IMAGE               := golang:$(GO_VERSION)-bookworm
GOLANGCI_IMAGE         := golangci/golangci-lint:v$(GOLANGCI_LINT_VERSION)-alpine

CACHE_DIR   := $(HOME)/.cache/found-footy
DOCKER_ENV  := -e GOCACHE=/gocache -e GOMODCACHE=/gomodcache
DOCKER_VOLS := -v $(PWD):/src -v $(CACHE_DIR)/gocache:/gocache -v $(CACHE_DIR)/gomodcache:/gomodcache
DOCKER_RUN  := docker run --rm $(DOCKER_ENV) $(DOCKER_VOLS) -w /src

GO_RUN       := $(DOCKER_RUN) $(GO_IMAGE)
GOLANGCI_RUN := $(DOCKER_RUN) $(GOLANGCI_IMAGE)

# testcontainers-go spawns sibling containers via the host Docker socket.
# --network=host lets the test container reach those siblings on
# localhost:<host-mapped-port>, matching what testcontainers.ConnectionString
# returns by default. Without this, tests get the port but can't dial it.
TEST_DOCKER_ARGS := -v /var/run/docker.sock:/var/run/docker.sock --network=host
GO_TEST_RUN      := docker run --rm $(DOCKER_ENV) $(DOCKER_VOLS) $(TEST_DOCKER_ARGS) -w /src $(GO_IMAGE)

.PHONY: help build check check-short test test-short test-race test-corpus hooks \
        lint fmt fmt-check vet tidy tidy-check clean cache-init \
        dev-up dev-down dev-logs dev-restart dev-shell dev-ps \
        twitter-vnc-up twitter-vnc-down twitter-vnc-logs migrate-prod deploy-prod

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

test: cache-init ## Run all tests (integration tests spawn containers via docker socket)
	$(GO_TEST_RUN) go test -buildvcs=false ./...

test-short: cache-init ## Run unit tests only (skips integration tests that require Docker)
	$(GO_RUN) go test -buildvcs=false -short ./...

test-live: cache-init ## Run live-network tests (real Wikidata/Wikipedia) — deliberate, NOT in the push gate
	$(GO_TEST_RUN) go test -buildvcs=false -tags live ./...

test-race: cache-init ## Run tests with the race detector
	$(GO_TEST_RUN) go test -buildvcs=false -race ./...

test-corpus: cache-init ## Run the scenario harness (test/scenarios/*.yaml)
	$(GO_TEST_RUN) go test -buildvcs=false -v -run TestScenarios ./test/

check-short: fmt-check tidy-check vet lint test-short ## Run every fast, non-integration gate

check: fmt-check tidy-check vet lint test ## Run every gate, including integration tests

hooks: ## Install git gates (core.hooksPath → .githooks)
	git config core.hooksPath .githooks
	@echo "git gates active: pre-commit → make check-short, pre-push → make check"

# ────── Lint + format ──────

lint: cache-init ## Run golangci-lint against the full tree
	$(GOLANGCI_RUN) golangci-lint run --timeout 5m

fmt: cache-init ## Run gofmt on every .go file (mutates in place)
	$(GO_RUN) gofmt -w -s .

fmt-check: cache-init ## Fail if any Go file needs gofmt; never mutates source
	@unformatted="$$( $(GO_RUN) gofmt -l . )"; \
	if [ -n "$$unformatted" ]; then \
		printf 'gofmt required:\n%s\n' "$$unformatted"; \
		exit 1; \
	fi

vet: cache-init ## Run go vet
	$(GO_RUN) go vet ./...

# ────── Modules ──────

tidy: cache-init ## Run go mod tidy
	$(GO_RUN) go mod tidy

tidy-check: cache-init ## Fail if go.mod/go.sum need tidying; never mutates source
	$(GO_RUN) go mod tidy -diff

# ────── Dev stack ──────

DEV_COMPOSE := docker-compose.dev.yml

dev-up: ## Bring up the dev stack (infrastructure plus three Go services)
	docker compose -f $(DEV_COMPOSE) up -d --build

dev-down: ## Stop and remove the dev stack containers
	docker compose -f $(DEV_COMPOSE) down

dev-logs: ## Tail logs from every dev service
	docker compose -f $(DEV_COMPOSE) logs -f --tail 50

dev-restart: ## Restart the dev stack (down + up)
	docker compose -f $(DEV_COMPOSE) down
	docker compose -f $(DEV_COMPOSE) up -d --build

dev-shell: ## Open a shell inside the worker dev container
	docker compose -f $(DEV_COMPOSE) exec worker bash

dev-ps: ## List running dev containers
	docker compose -f $(DEV_COMPOSE) ps

# ────── Twitter VNC (opt-in cookie re-auth container) ──────
#
# These targets bring up the twitter-vnc container ONLY when an
# operator needs to log in (cookies expired). The container runs
# raw Firefox ESR on an Xvfb display and exposes noVNC over
# http://found-footy-<env>-twitter-vnc.luv — open in a browser,
# log in, then close Firefox so the capture service can publish the
# shared file. Verify /status and the static service before running
# `make twitter-vnc-down` to reclaim resources.
#
# The dev target defaults to the dev compose file; prod targets
# operate against the prod compose file explicitly.

PROD_COMPOSE := docker-compose.prod.yml

deploy-prod: ## Build, roll out, and verify the exact clean commit in production
	./scripts/deploy-prod.sh

migrate-prod: ## Build and run the exact clean commit's migration command in production
	./scripts/migrate-prod.sh

twitter-vnc-up: ## Bring up the DEV twitter-vnc container for manual cookie re-auth
	docker compose -f $(DEV_COMPOSE) --profile vnc up -d --build twitter-vnc
	@echo ""
	@echo "  ✓ twitter-vnc running. Log in at http://found-footy-dev-twitter-vnc.luv"
	@echo "  Close Firefox after login, then require twitter-vnc /status state=ready."
	@echo "  When done: make twitter-vnc-down"
	@echo ""

twitter-vnc-down: ## Stop and remove the DEV twitter-vnc container
	docker compose -f $(DEV_COMPOSE) --profile vnc stop twitter-vnc
	docker compose -f $(DEV_COMPOSE) --profile vnc rm -f twitter-vnc

twitter-vnc-logs: ## Tail logs from the DEV twitter-vnc container
	docker compose -f $(DEV_COMPOSE) --profile vnc logs -f --tail 50 twitter-vnc

# ────── Housekeeping ──────

clean: ## Remove the shared Docker cache directory
	rm -rf $(CACHE_DIR)
