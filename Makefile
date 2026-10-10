SHELL := /bin/sh
.DEFAULT_GOAL := help

# Local settings: `make run` reads .env when it exists (copy .env.example). Variables that are already
# set in the environment or on the command line win over the file, so `DATABASE_URL=... make migrate`
# is never overridden. Lines must be plain KEY=value.
ifneq (,$(wildcard .env))
dotenv_value = $(subst $$,$$$$,$(shell sed -n -e 's/\r$$//' -e 's/^$(1)=//p' .env | tail -n 1))
$(foreach v,$(shell sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p' .env),\
  $(if $(filter undefined,$(origin $(v))),$(eval $(v) := $(call dotenv_value,$(v)))$(eval export $(v))))
endif

# Pinned tool versions; `go run module@version` needs no global install.
GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION   := v1.8.0
MIGRATE_VERSION       := v4.20.1
MIGRATE := go run -tags 'postgres,cassandra' github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION)

IMAGE ?= messenger-api:local
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

.PHONY: help run run-worker build test lint vet vuln fmt generate tidy migrate migrate-postgres migrate-postgres-down migrate-scylla migrate-scylla-down docker env up down dev-logs

help: ## List the targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

run: ## Start the API against the local environment (reads .env)
	go run ./cmd/api

run-worker: ## Start the worker
	go run ./cmd/worker

build: ## Build all binaries into bin/
	go build -trimpath -o bin/ ./cmd/...

test: ## Run the tests with the race detector
	go test -race ./...

lint: vet ## Run golangci-lint and go vet
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

vet: ## Run go vet
	go vet ./...

vuln: ## Check dependencies and the standard library for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

fmt: ## Format the code
	gofmt -w cmd internal

generate: ## Regenerate the server (api/openapi.yaml) and the PostgreSQL queries (sqlc)
	go generate ./...

tidy: ## Tidy go.mod and go.sum
	go mod tidy

migrate: migrate-postgres migrate-scylla ## Apply all database migrations (needs DATABASE_URL, SCYLLA_HOSTS, SCYLLA_KEYSPACE)

migrate-postgres: ## Apply the PostgreSQL migrations in migrations/postgres
	$(MIGRATE) -path migrations/postgres -database "$${DATABASE_URL:?DATABASE_URL is required}" up

migrate-postgres-down: ## Roll back every PostgreSQL migration (development only: drops all data)
	$(MIGRATE) -path migrations/postgres -database "$${DATABASE_URL:?DATABASE_URL is required}" down -all

SCYLLA_MIGRATE_URL = cassandra://$$(printf %s "$${SCYLLA_HOSTS:?SCYLLA_HOSTS is required}" | cut -d, -f1 | tr -d ' ')/$${SCYLLA_KEYSPACE:?SCYLLA_KEYSPACE is required}?x-multi-statement=true

migrate-scylla: ## Apply the ScyllaDB migrations in migrations/scylla (the keyspace must exist)
	$(MIGRATE) -path migrations/scylla -database "$(SCYLLA_MIGRATE_URL)" up

migrate-scylla-down: ## Roll back every ScyllaDB migration (development only: drops all messages)
	$(MIGRATE) -path migrations/scylla -database "$(SCYLLA_MIGRATE_URL)" down -all

DEV_COMPOSE := docker compose -f compose.dev.yaml

env: ## Create .env with generated secrets for the development stack (never overwrites)
	go run deploy/dev/genenv.go

up: ## Start the whole development stack (API, worker, PostgreSQL, Redis, ScyllaDB, Elasticsearch, S3)
	$(DEV_COMPOSE) up --build --detach --wait

down: ## Stop the development stack (keeps the data; add `docker compose -f compose.dev.yaml down -v` to wipe it)
	$(DEV_COMPOSE) down

dev-logs: ## Follow the logs of the development stack
	$(DEV_COMPOSE) logs --follow --tail 100

docker: ## Build the API image (go.Dockerfile)
	docker build -f go.Dockerfile --build-arg COMMIT=$(COMMIT) -t $(IMAGE) .

