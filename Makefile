SHELL := /bin/sh
.DEFAULT_GOAL := help

# Local settings: `make run` reads .env when it exists (copy .env.example). Values already
# in the environment win over the file.
ifneq (,$(wildcard .env))
include .env
export
endif

# Pinned tool versions; `go run module@version` needs no global install.
GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK_VERSION   := v1.8.0
MIGRATE_VERSION       := v4.20.1
MIGRATE := go run -tags 'postgres,cassandra' github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION)

comma := ,
IMAGE ?= messenger-api:local
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

.PHONY: help run run-worker build test lint vet vuln fmt generate tidy migrate migrate-postgres migrate-scylla docker

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

generate: ## Regenerate the server from api/openapi.yaml
	go generate ./...

tidy: ## Tidy go.mod and go.sum
	go mod tidy

migrate: migrate-postgres migrate-scylla ## Apply all database migrations (needs DATABASE_URL, SCYLLA_HOSTS, SCYLLA_KEYSPACE)

migrate-postgres: ## Apply the PostgreSQL migrations in migrations/postgres
	$(MIGRATE) -path migrations/postgres -database "$(DATABASE_URL)" up

migrate-scylla: ## Apply the ScyllaDB migrations in migrations/scylla
	$(MIGRATE) -path migrations/scylla -database "cassandra://$(firstword $(subst $(comma), ,$(SCYLLA_HOSTS)))/$(SCYLLA_KEYSPACE)" up

docker: ## Build the API image (go.Dockerfile)
	docker build -f go.Dockerfile --build-arg COMMIT=$(COMMIT) -t $(IMAGE) .

