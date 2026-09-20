# Container-first validation and image targets for the standalone operator UI.
GO_VERSION := 1.25.11
GO_IMAGE := golang:$(GO_VERSION)-bookworm
GOLANGCI_LINT_VERSION := 2.12.2
GOLANGCI_IMAGE := golangci/golangci-lint:v$(GOLANGCI_LINT_VERSION)-alpine
CACHE_DIR := $(HOME)/.cache/found-footy-operator-ui
DOCKER_RUN := docker run --rm -e GOCACHE=/gocache -e GOMODCACHE=/gomodcache \
	-v $(PWD):/src -v $(CACHE_DIR)/gocache:/gocache -v $(CACHE_DIR)/gomodcache:/gomodcache \
	-w /src $(GO_IMAGE)
LINT_RUN := docker run --rm -v $(PWD):/src -w /src $(GOLANGCI_IMAGE)

.PHONY: cache-init tidy tidy-check fmt-check vet lint test check image

cache-init:
	@mkdir -p $(CACHE_DIR)/gocache $(CACHE_DIR)/gomodcache

tidy: cache-init
	$(DOCKER_RUN) go mod tidy

tidy-check: cache-init
	$(DOCKER_RUN) go mod tidy -diff

fmt-check: cache-init
	@unformatted="$$( $(DOCKER_RUN) gofmt -l . )"; test -z "$$unformatted" || { printf 'gofmt required:\n%s\n' "$$unformatted"; exit 1; }

vet: cache-init
	$(DOCKER_RUN) go vet ./...

lint:
	$(LINT_RUN) golangci-lint run --timeout 5m

test: cache-init
	$(DOCKER_RUN) go test -buildvcs=false ./...

check: fmt-check tidy-check vet lint test

image:
	docker build -t found-footy-operator-ui:dev .
