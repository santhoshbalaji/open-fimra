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
FIMRA_DATABASE_URL ?= $(DATABASE_URL)
FIMRA_KEYCLOAK_PORT ?= 18180
FIMRA_OIDC_ISSUER ?= http://127.0.0.1:$(FIMRA_KEYCLOAK_PORT)/realms/open-fimra
FIMRA_OIDC_AUDIENCE ?= open-fimra-api
FIMRA_HTTP_ADDR ?= 127.0.0.1:18090
FIMRA_UPLOAD_DIR ?= .geo-uploads
FIMRA_MAILPIT_HTTP_PORT ?= 18025
FIMRA_MAILPIT_SMTP_PORT ?= 11025
export FIMRA_UPLOAD_DIR := $(abspath $(FIMRA_UPLOAD_DIR))
FIMRA_EXPORT_DIR ?= .geo-exports
export FIMRA_EXPORT_DIR := $(abspath $(FIMRA_EXPORT_DIR))
# Downloads from the export integration test, read back with OGR.
EXPORT_CHECK_DIR := $(CURDIR)/.geo-exports-check

GOOSE_VERSION := v3.30.1
GOOSE := go run github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION) -dir db/migrations postgres "$(DATABASE_URL)"
PSQL := $(COMPOSE) exec -T db sh -c 'psql -X -q -v ON_ERROR_STOP=1 -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"'

.PHONY: help dev dev-down dev-reset dev-logs dev-ps db-up db-shell db-migrate db-rollback db-status db-test seed seed-test check
.PHONY: api-run api-test api-test-integration api-lint ingest-fixture demo-detections
.PHONY: backfill-firms backfill-demo backfill-queue feed-health rotate-settings-key wizard-test-integration

api-run api-test-integration ingest-fixture demo-detections backfill-firms backfill-demo backfill-queue feed-health rotate-settings-key: export FIMRA_DATABASE_URL := $(FIMRA_DATABASE_URL)
api-run api-test-integration: export FIMRA_OIDC_ISSUER := $(FIMRA_OIDC_ISSUER)
api-run api-test-integration: export FIMRA_OIDC_AUDIENCE := $(FIMRA_OIDC_AUDIENCE)
api-run: export FIMRA_HTTP_ADDR := $(FIMRA_HTTP_ADDR)
api-run: export FIMRA_EXPORT_TTL := $(FIMRA_EXPORT_TTL)
api-run backfill-firms: export FIMRA_FIRMS_MAP_KEY := $(FIMRA_FIRMS_MAP_KEY)
api-run: export FIMRA_CORS_ORIGINS := $(FIMRA_CORS_ORIGINS)
api-run: export FIMRA_SMTP_ADDR := $(FIMRA_SMTP_ADDR)
api-run: export FIMRA_SMTP_FROM := $(FIMRA_SMTP_FROM)
api-run: export FIMRA_SMTP_USERNAME := $(FIMRA_SMTP_USERNAME)
api-run: export FIMRA_SMTP_PASSWORD := $(FIMRA_SMTP_PASSWORD)
api-run: export FIMRA_SMTP_SECURITY := $(FIMRA_SMTP_SECURITY)
api-run: export FIMRA_WEB_URL := $(FIMRA_WEB_URL)
api-run: export FIMRA_SETUP_CODE := $(FIMRA_SETUP_CODE)
api-run backfill-firms rotate-settings-key: export FIMRA_SETTINGS_KEY := $(FIMRA_SETTINGS_KEY)
api-run backfill-firms rotate-settings-key: export FIMRA_SETTINGS_OLD_KEYS := $(FIMRA_SETTINGS_OLD_KEYS)
api-test-integration: export FIMRA_TEST_MAILPIT_URL := http://127.0.0.1:$(FIMRA_MAILPIT_HTTP_PORT)
api-test-integration: export FIMRA_TEST_MAILPIT_SMTP := 127.0.0.1:$(FIMRA_MAILPIT_SMTP_PORT)
# The GIS link test reads the API with ogrinfo in the geo worker's GDAL base image.
GDAL_IMAGE ?= $(shell sed -n 's/^FROM \(ghcr.io\/osgeo\/gdal:[^ ]*\).*/\1/p' services/geo-worker/Dockerfile)
api-test-integration: export FIMRA_GIS_OGR_IMAGE := $(GDAL_IMAGE)
ingest-fixture: export FIMRA_FIXTURE_FILE := $(if $(FILE),$(if $(filter /%,$(FILE)),$(FILE),$(CURDIR)/$(FILE)))
ingest-fixture: export FIMRA_FIXTURE_PRODUCT := $(or $(PRODUCT),VIIRS_SNPP_NRT)

# Backfill settings. FROM and TO are UTC days in YYYY-MM-DD form, both included.
WORKSPACE ?= demo
PRODUCTS ?= VIIRS_SNPP_SP,MODIS_SP
BACKFILL_FLAGS = -workspace "$(WORKSPACE)" -from "$(FROM)" -to "$(TO)" -products "$(PRODUCTS)"

help: ## Show this list
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-22s %s\n", $$1, $$2}'

dev: ## Start the development stack (PostGIS and Keycloak) and wait until it is healthy
	$(COMPOSE) up -d --wait
	@$(COMPOSE) ps --format 'table {{.Service}}\t{{.Status}}\t{{.Ports}}'

dev-down: ## Stop the development stack, keeping its data
	$(COMPOSE) --profile geo --profile mail down

dev-reset: ## Stop the development stack and delete its data
	$(COMPOSE) --profile geo --profile mail down --volumes

.PHONY: mail-up
mail-up: ## Start the local mail catcher
	$(COMPOSE) up -d --wait mailpit

dev-logs: ## Follow the development stack's logs
	$(COMPOSE) --profile geo logs -f

dev-ps: ## Show the development stack's containers
	$(COMPOSE) --profile geo ps

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
	@set -o pipefail; { \
		echo 'BEGIN;'; \
		echo "SELECT pg_advisory_xact_lock(hashtext('open-fimra:demo-seed'));"; \
		echo "SELECT NOT EXISTS (SELECT 1 FROM workspaces WHERE slug = 'demo') AS demo_missing"; \
		printf '%s\n' '\gset' '\if :demo_missing'; \
		cat seed/demo-state/load.sql; \
		printf '%s\n' '\endif'; \
		cat seed/demo-state/roles.sql seed/demo-state/reference.sql seed/tests/reference-check.sql; \
		echo 'SET CONSTRAINTS ALL IMMEDIATE;'; \
		echo 'ROLLBACK;'; \
	} | $(PSQL)

api-run: ## Run the core API on the development stack
	cd services/api && go run .

api-test: ## Run the core API unit tests
	cd services/api && go test ./...

api-test-integration: mail-up ## Test the core API against the migrated development stack
	cd services/api && FIMRA_API_INTEGRATION=1 go test -run '^(TestIntegration|TestCoreLoopIntegration|TestBackfillIntegration|TestFeedHealthIntegration|TestNotificationsIntegration|TestNotificationEmailIntegration|TestGISLinkIntegration)$$' -count=1 -v .

ingest-fixture: ## Import a FIRMS CSV given FILE and optional PRODUCT
	@test -n "$$FIMRA_FIXTURE_FILE" || { echo "Set FILE to a FIRMS CSV path."; exit 1; }
	cd services/api && go run . ingest-firms -file "$$FIMRA_FIXTURE_FILE" -product "$$FIMRA_FIXTURE_PRODUCT"

demo-detections: ## Load the synthetic workspace and detections at four ages
	cd services/api && go run . ingest-firms -demo -file ../../packages/connectors/firms/fixtures/demo.csv

backfill-firms: ## Backfill FIRMS standard-processing data given FROM, TO and optional WORKSPACE and PRODUCTS
	@test -n "$(FROM)" -a -n "$(TO)" || { echo "Set FROM and TO, for example FROM=2024-01-01 TO=2024-06-30."; exit 1; }
	cd services/api && go run . backfill-firms $(BACKFILL_FLAGS)

backfill-queue: ## Queue a FIRMS backfill job for the running API given FROM, TO and optional WORKSPACE and PRODUCTS
	@test -n "$(FROM)" -a -n "$(TO)" || { echo "Set FROM and TO, for example FROM=2024-01-01 TO=2024-06-30."; exit 1; }
	cd services/api && go run . backfill-firms -enqueue $(BACKFILL_FLAGS)

backfill-demo: ## Backfill the demo workspace from the synthetic standard-processing fixtures; safe to repeat
	cd services/api && go run . backfill-firms -demo -workspace demo -from 2000-01-01 -to 2000-02-15 \
		-products VIIRS_SNPP_SP,MODIS_SP -fixtures ../../packages/connectors/firms/fixtures

feed-health: ## Check every workspace's live feeds once, record feed-health alerts and print each feed's status
	cd services/api && go run . feed-health

rotate-settings-key: ## Seal every saved secret with FIMRA_SETTINGS_KEY; list the previous keys in FIMRA_SETTINGS_OLD_KEYS
	cd services/api && go run . rotate-settings-key

# The wizard test needs a database with only migrations applied. It makes a
# fresh one beside the development database and runs a geo worker on it.
WIZARD_DB := fimra_wizard_test
WIZARD_DATABASE_URL := postgres://$(FIMRA_DB_USER):$(FIMRA_DB_PASSWORD)@127.0.0.1:$(FIMRA_DB_PORT)/$(WIZARD_DB)?sslmode=disable
wizard-test-integration: export FIMRA_DATABASE_URL := $(WIZARD_DATABASE_URL)
wizard-test-integration: export FIMRA_OIDC_ISSUER := $(FIMRA_OIDC_ISSUER)
wizard-test-integration: export FIMRA_OIDC_AUDIENCE := $(FIMRA_OIDC_AUDIENCE)
wizard-test-integration: export FIMRA_TEST_MAILPIT_URL := http://127.0.0.1:$(FIMRA_MAILPIT_HTTP_PORT)
wizard-test-integration: export FIMRA_TEST_MAILPIT_SMTP := 127.0.0.1:$(FIMRA_MAILPIT_SMTP_PORT)
wizard-test-integration: mail-up ## Take an empty, migrated database to a working alert map through the first-run wizard
	install -d -m 700 "$(FIMRA_UPLOAD_DIR)" "$(FIMRA_EXPORT_DIR)"
	$(COMPOSE) build geo-worker
	$(COMPOSE) exec -T db sh -c 'dropdb -U "$$POSTGRES_USER" --if-exists --force $(WIZARD_DB) && createdb -U "$$POSTGRES_USER" $(WIZARD_DB)'
	go run github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION) -dir db/migrations postgres "$(WIZARD_DATABASE_URL)" up
	@set -e; worker=$$($(COMPOSE) run -d --rm -e PGDATABASE=$(WIZARD_DB) geo-worker); \
		trap 'docker stop "$$worker" > /dev/null' EXIT; \
		cd services/api && FIMRA_WIZARD_INTEGRATION=1 go test -run '^TestWizardIntegration$$' -count=1 -v .

WALKTHROUGH_SHOTS ?= $(HOME)/open-fimra-screenshots/first-run-wizard
.PHONY: wizard-walkthrough
wizard-walkthrough: mail-up web-build ## Drive the wizard in headless Chromium on a fresh database and save screenshots outside the repository
	$(COMPOSE) build geo-worker
	WALK_DB=fimra_walkthrough \
	WALK_DATABASE_URL="postgres://$(FIMRA_DB_USER):$(FIMRA_DB_PASSWORD)@127.0.0.1:$(FIMRA_DB_PORT)/fimra_walkthrough?sslmode=disable" \
	SHOTS="$(WALKTHROUGH_SHOTS)" ISSUER="$(FIMRA_OIDC_ISSUER)" API_ADDR="$(FIMRA_HTTP_ADDR)" \
	MAIL_SMTP=127.0.0.1:$(FIMRA_MAILPIT_SMTP_PORT) MAIL_HTTP=127.0.0.1:$(FIMRA_MAILPIT_HTTP_PORT) \
	GOOSE_VERSION=$(GOOSE_VERSION) PLAYWRIGHT_IMAGE=mcr.microsoft.com/playwright:v1.63.0-noble \
	COMPOSE_CMD="$(COMPOSE)" FIMRA_OIDC_ISSUER="$(FIMRA_OIDC_ISSUER)" FIMRA_OIDC_AUDIENCE="$(FIMRA_OIDC_AUDIENCE)" \
	scripts/wizard-walkthrough.sh

api-lint: ## Check core API formatting and run Go vet
	cd services/api && unformatted=$$(gofmt -l .) && if [[ -n "$$unformatted" ]]; then printf '%s\n' "$$unformatted"; exit 1; fi
	cd services/api && go vet ./...

check: ## Run the repository checks that continuous integration runs
	scripts/check-repo.sh
	python3 seed/generate.py --check
	$(COMPOSE) config --quiet
	@echo "Compose file is valid."

# Corepack uses the pnpm version pinned in apps/web/package.json.
PNPM ?= corepack pnpm
.PHONY: web-install web-dev web-build web-test web-lint

web-install: ## Install the web app's locked dependencies
	cd apps/web && $(PNPM) install --frozen-lockfile

web-dev: ## Serve the web app on 127.0.0.1:4200
	cd apps/web && $(PNPM) run dev

web-build: ## Build the web app for distribution
	cd apps/web && $(PNPM) run build

web-test: ## Run the web unit tests
	cd apps/web && $(PNPM) run test

web-lint: ## Lint the web app and its templates
	cd apps/web && $(PNPM) run lint

.PHONY: geo-worker-up geo-test geo-test-integration export-test-integration

geo-worker-up: ## Build and start the geo worker for layer imports and exports
	install -d -m 700 "$(FIMRA_UPLOAD_DIR)" "$(FIMRA_EXPORT_DIR)"
	$(COMPOSE) up -d --build geo-worker

geo-test: ## Run the geo worker unit tests in the GDAL container
	install -d -m 700 "$(FIMRA_UPLOAD_DIR)" "$(FIMRA_EXPORT_DIR)"
	$(COMPOSE) build geo-worker
	$(COMPOSE) run --rm --no-deps geo-worker python -m unittest discover -v

geo-test-integration: export FIMRA_DATABASE_URL := $(FIMRA_DATABASE_URL)
geo-test-integration: export FIMRA_OIDC_ISSUER := $(FIMRA_OIDC_ISSUER)
geo-test-integration: export FIMRA_OIDC_AUDIENCE := $(FIMRA_OIDC_AUDIENCE)
geo-test-integration: ## Test layer imports and publication on the running, migrated stack
	cd services/api && FIMRA_GEO_INTEGRATION=1 go test -run '^(TestLayerImportIntegration|TestLayerPublishIntegration)$$' -count=1 -v .

export-test-integration: export FIMRA_DATABASE_URL := $(FIMRA_DATABASE_URL)
export-test-integration: export FIMRA_OIDC_ISSUER := $(FIMRA_OIDC_ISSUER)
export-test-integration: export FIMRA_OIDC_AUDIENCE := $(FIMRA_OIDC_AUDIENCE)
export-test-integration: ## Export as each demo user on the running stack, then read every format back with OGR
	install -d -m 700 "$(EXPORT_CHECK_DIR)"
	cd services/api && FIMRA_EXPORT_INTEGRATION=1 FIMRA_EXPORT_CHECK_DIR="$(EXPORT_CHECK_DIR)" \
		go test -run '^TestExportIntegration$$' -count=1 -v .
	$(COMPOSE) run --rm --no-deps -v "$(EXPORT_CHECK_DIR):/check:ro" geo-worker sh -c 'python readback.py /check/*'
