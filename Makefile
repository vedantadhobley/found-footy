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

CACHE_DIR        := $(HOME)/.cache/found-footy
DOCKER_ENV       := -e GOCACHE=/gocache -e GOMODCACHE=/gomodcache
DOCKER_CACHE_VOL := -v $(CACHE_DIR)/gocache:/gocache -v $(CACHE_DIR)/gomodcache:/gomodcache
DOCKER_VOLS      := -v $(PWD):/src $(DOCKER_CACHE_VOL)
DOCKER_RUN       := docker run --rm $(DOCKER_ENV) $(DOCKER_VOLS) -w /src

GO_RUN       := $(DOCKER_RUN) $(GO_IMAGE)
GOLANGCI_RUN := $(DOCKER_RUN) $(GOLANGCI_IMAGE)
GO_MODULES   := $(shell ./scripts/go-scope.sh modules)
GO_FILES     := $(shell ./scripts/go-scope.sh files)

# testcontainers-go spawns sibling containers via the host Docker socket.
# --network=host lets the test container reach those siblings on
# localhost:<host-mapped-port>, matching what testcontainers.ConnectionString
# returns by default. Without this, tests get the port but can't dial it.
TEST_DOCKER_ARGS := -v /var/run/docker.sock:/var/run/docker.sock --network=host
GO_TEST_RUN      := docker run --rm $(DOCKER_ENV) $(DOCKER_VOLS) $(TEST_DOCKER_ARGS) -w /src $(GO_IMAGE)

# Run one gate against every Git-visible Go module. Git supplies concrete
# source directories; go-run.sh applies build tags and skips packages that are
# ineligible for that gate. Each nested module keeps its own working directory.
define run_go_modules
	@set -eu; \
	for module in $(GO_MODULES); do \
		packages="$$(./scripts/go-scope.sh packages "$$module" | tr '\n' ' ')"; \
		[ -n "$$packages" ] || { printf 'no Go packages in module %s\n' "$$module" >&2; exit 1; }; \
		printf '==> %s: $(1)\n' "$$module"; \
		docker run --rm $(DOCKER_ENV) $(DOCKER_VOLS) $(2) -w "/src/$$module" $(3) /src/scripts/go-run.sh $(1) $$packages; \
	done
endef

# Tidy in a Git-visible snapshot so ignored forensic programs cannot add or
# remove module requirements. Mode is "-diff" for the gate and empty for the
# mutating target; the latter copies only module metadata back to the tree.
define run_tidy_modules
	@set -eu; \
	snapshot="$$(mktemp -d /tmp/found-footy-tidy.XXXXXX)"; \
	trap 'rm -rf "$$snapshot"' EXIT HUP INT TERM; \
	./scripts/go-scope.sh snapshot "$$snapshot"; \
	for module in $(GO_MODULES); do \
		printf '==> %s: go mod tidy $(1)\n' "$$module"; \
		docker run --rm $(DOCKER_ENV) -v "$$snapshot:/src" $(DOCKER_CACHE_VOL) -w "/src/$$module" $(GO_IMAGE) go mod tidy $(1); \
		if [ "$(1)" != "-diff" ]; then \
			cp "$$snapshot/$$module/go.mod" "$$module/go.mod"; \
			if [ -f "$$snapshot/$$module/go.sum" ]; then cp "$$snapshot/$$module/go.sum" "$$module/go.sum"; fi; \
		fi; \
	done
endef

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
	$(call run_go_modules,build,,$(GO_IMAGE))

# ────── Test ──────

test: cache-init ## Run all tests (integration tests spawn containers via docker socket)
	$(call run_go_modules,test,$(TEST_DOCKER_ARGS),$(GO_IMAGE))

test-short: cache-init ## Run unit tests only (skips integration tests that require Docker)
	$(call run_go_modules,test-short,,$(GO_IMAGE))

test-live: cache-init ## Run live-network tests (real Wikidata/Wikipedia) — deliberate, NOT in the push gate
	$(call run_go_modules,test-live,$(TEST_DOCKER_ARGS),$(GO_IMAGE))

test-race: cache-init ## Run tests with the race detector
	$(call run_go_modules,test-race,$(TEST_DOCKER_ARGS),$(GO_IMAGE))

test-corpus: cache-init ## Run the scenario harness (test/scenarios/*.yaml)
	$(GO_TEST_RUN) go test -buildvcs=false -v -run TestScenarios ./test/

check-short: fmt-check tidy-check vet lint test-short ## Run every fast, non-integration gate

check: fmt-check tidy-check vet lint test ## Run every gate, including integration tests

hooks: ## Install git gates (core.hooksPath → .githooks)
	git config core.hooksPath .githooks
	@echo "git gates active: pre-commit → make check-short, pre-push → make check"

# ────── Lint + format ──────

lint: cache-init ## Run golangci-lint against the full tree
	$(call run_go_modules,lint,,$(GOLANGCI_IMAGE))

fmt: cache-init ## Run gofmt on every .go file (mutates in place)
	$(GO_RUN) gofmt -w -s $(GO_FILES)

fmt-check: cache-init ## Fail if any Go file needs gofmt; never mutates source
	@unformatted="$$( $(GO_RUN) gofmt -l $(GO_FILES) )"; \
	if [ -n "$$unformatted" ]; then \
		printf 'gofmt required:\n%s\n' "$$unformatted"; \
		exit 1; \
	fi

vet: cache-init ## Run go vet
	$(call run_go_modules,vet,,$(GO_IMAGE))

# ────── Modules ──────

tidy: cache-init ## Run go mod tidy
	$(call run_tidy_modules,)

tidy-check: cache-init ## Fail if go.mod/go.sum need tidying; never mutates source
	$(call run_tidy_modules,-diff)

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
