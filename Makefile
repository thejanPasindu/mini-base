# mini-base developer tasks. Run `make help` for the list.

GO        ?= go
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
COMPOSE   := docker compose -f deploy/compose/docker-compose.yml
TEST_PG   := minibase-test-pg
TEST_DB   := postgres://postgres:postgres@127.0.0.1:55432/postgres?sslmode=disable
GO_IMAGE  := golang:1.24

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

## --- Build & run ---

.PHONY: build
build: ## Build ./bin/minibase
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/minibase ./cmd/minibase

.PHONY: run
run: build db-up ## Run locally against the test Postgres with the example module
	MINIBASE_DATABASE_URL="$(TEST_DB)" MINIBASE_MODULES=example ./bin/minibase serve

.PHONY: tidy
tidy: ## Resolve dependencies and write go.sum
	$(GO) mod tidy

.PHONY: tidy-docker
tidy-docker: ## Same as tidy, without installing Go locally
	docker run --rm -v "$(CURDIR)":/src -w /src $(GO_IMAGE) go mod tidy

## --- Quality ---

.PHONY: fmt
fmt: ## Format code
	gofmt -w .

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run

.PHONY: test
test: ## Unit tests (no database needed)
	$(GO) test -race -count=1 ./...

.PHONY: test-integration
test-integration: db-up ## Integration tests against a real Postgres
	MINIBASE_TEST_DATABASE_URL="$(TEST_DB)" $(GO) test -race -count=1 -tags=integration ./...

.PHONY: test-docker
test-docker: ## Unit tests inside Docker, without installing Go locally
	docker run --rm -v "$(CURDIR)":/src -w /src $(GO_IMAGE) go test -count=1 ./...

.PHONY: cover
cover: ## Unit test coverage report
	$(GO) test -count=1 -coverprofile=coverage.out ./... && $(GO) tool cover -func=coverage.out | tail -1

## --- Local Postgres for tests ---

.PHONY: db-up
db-up: ## Start a throwaway Postgres on 127.0.0.1:55432
	@docker inspect $(TEST_PG) >/dev/null 2>&1 || \
		docker run -d --name $(TEST_PG) -e POSTGRES_PASSWORD=postgres -p 127.0.0.1:55432:5432 postgres:16-alpine >/dev/null
	@until docker exec $(TEST_PG) pg_isready -U postgres >/dev/null 2>&1; do sleep 0.5; done
	@echo "Postgres ready at $(TEST_DB)"

.PHONY: db-down
db-down: ## Remove the throwaway Postgres
	-docker rm -f $(TEST_PG)

## --- Docker ---

.PHONY: docker-build
docker-build: ## Build the container image
	docker build -f deploy/docker/Dockerfile --build-arg VERSION=$(VERSION) -t minibase:$(VERSION) .

.PHONY: up
up: ## Start the compose stack (needs deploy/compose/.env)
	$(COMPOSE) up --build -d

.PHONY: down
down: ## Stop the compose stack
	$(COMPOSE) down

.PHONY: logs
logs: ## Follow minibase logs
	$(COMPOSE) logs -f minibase
