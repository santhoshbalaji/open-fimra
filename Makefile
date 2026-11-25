# Open FIMRA developer commands. Run `make` or `make help` for the list.

SHELL := /bin/bash
.DEFAULT_GOAL := help

COMPOSE_FILE := deploy/compose/compose.dev.yml
COMPOSE := docker compose -f $(COMPOSE_FILE) $(if $(wildcard .env),--env-file .env)

.PHONY: help dev dev-down dev-reset dev-logs dev-ps db-shell check

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

db-shell: ## Open psql in the development database
	$(COMPOSE) exec db sh -c 'psql -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"'

check: ## Run the repository checks that continuous integration runs
	scripts/check-repo.sh
	$(COMPOSE) config --quiet
	@echo "Compose file is valid."
