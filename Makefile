# Open FIMRA developer commands. Run `make` or `make help` for the list.

SHELL := /bin/bash
.DEFAULT_GOAL := help

COMPOSE_FILE := deploy/compose/compose.dev.yml
COMPOSE := docker compose -f $(COMPOSE_FILE) $(if $(wildcard .env),--env-file .env)

# Database settings match the defaults in compose.dev.yml; .env overrides them.
ifneq ($(wildcard .env),)
include .env
endif
FIMRA_DB_NAME ?= fimra
FIMRA_DB_USER ?= fimra
FIMRA_DB_PASSWORD ?= fimra-dev
FIMRA_DB_PORT ?= 15432
DATABASE_URL ?= postgres://$(FIMRA_DB_USER):$(FIMRA_DB_PASSWORD)@127.0.0.1:$(FIMRA_DB_PORT)/$(FIMRA_DB_NAME)?sslmode=disable

GOOSE_VERSION := v3.30.1
GOOSE := go run github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION) -dir db/migrations postgres "$(DATABASE_URL)"
PSQL := $(COMPOSE) exec -T db sh -c 'psql -X -q -v ON_ERROR_STOP=1 -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"'

.PHONY: help dev dev-down dev-reset dev-logs dev-ps db-up db-shell db-migrate db-rollback db-status db-test seed seed-test check

help: ## Show this list
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

dev: ## Start the development stack (PostGIS and Keycloak) and wait until it is healthy
	$(COMPOSE) up -d --wait
	@$(COMPOSE) ps --format 'table {{.Service}}\t{{.Status}}\t{{.Ports}}'

dev-down: ## Stop the development stack, keeping its data
	$(COMPOSE) down

dev-reset: ## Stop the development stack and delete its data
	$(COMPOSE) down --volumes

dev-logs: ## Follow the development stack's logs
	$(COMPOSE) logs -f

dev-ps: ## Show the development stack's containers
	$(COMPOSE) ps

db-up: ## Start only the development database and wait until it is healthy
	$(COMPOSE) up -d --wait db

db-shell: ## Open psql in the development database
	$(COMPOSE) exec db sh -c 'psql -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"'

db-migrate: ## Apply all database migrations
	$(GOOSE) up

db-rollback: ## Roll back the most recent database migration
	$(GOOSE) down

db-status: ## Show which database migrations are applied
	$(GOOSE) status

db-test: ## Check that migrations apply, roll back and reapply, then run the schema tests
	$(GOOSE) up
	$(GOOSE) reset
	$(GOOSE) up
	@for f in db/tests/*.sql; do echo "Running $$f"; $(PSQL) < "$$f" || exit 1; done

seed: ## Regenerate the synthetic demo state in seed/demo-state
	python3 seed/generate.py

seed-test: ## Check the demo state is up to date, then load it and run the reference check
	python3 seed/generate.py --check
	$(GOOSE) up
	@{ echo 'BEGIN;'; cat seed/demo-state/load.sql seed/demo-state/reference.sql seed/tests/reference-check.sql; echo 'ROLLBACK;'; } | $(PSQL)

check: ## Run the repository checks that continuous integration runs
	scripts/check-repo.sh
	python3 seed/generate.py --check
	$(COMPOSE) config --quiet
	@echo "Compose file is valid."
