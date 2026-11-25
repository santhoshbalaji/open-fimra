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
# Photos and files from field reports, when they are kept on local disk.
FIMRA_ATTACHMENT_DIR ?= .form-attachments
export FIMRA_ATTACHMENT_DIR := $(abspath $(FIMRA_ATTACHMENT_DIR))
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
api-run: export FIMRA_ATTACHMENT_STORE := $(FIMRA_ATTACHMENT_STORE)
api-run: export FIMRA_ATTACHMENT_MAX_MB := $(FIMRA_ATTACHMENT_MAX_MB)
api-run: export FIMRA_S3_ENDPOINT := $(FIMRA_S3_ENDPOINT)
api-run: export FIMRA_S3_REGION := $(FIMRA_S3_REGION)
api-run: export FIMRA_S3_BUCKET := $(FIMRA_S3_BUCKET)
api-run: export FIMRA_S3_PREFIX := $(FIMRA_S3_PREFIX)
api-run: export FIMRA_S3_ACCESS_KEY := $(FIMRA_S3_ACCESS_KEY)
api-run: export FIMRA_S3_SECRET_KEY := $(FIMRA_S3_SECRET_KEY)
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
	cd services/api && FIMRA_API_INTEGRATION=1 go test -run '^(TestIntegration|TestCoreLoopIntegration|TestBackfillIntegration|TestFeedHealthIntegration|TestNotificationsIntegration|TestNotificationEmailIntegration|TestGISLinkIntegration|TestIncidentsIntegration|TestDatabaseIntegration|TestFormsIntegration)$$' -count=1 -v .

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

# The S3 attachment test needs an S3-compatible server. This starts rclone's
# small one for the test only; it is not a choice of store for installs.
S3_TEST_IMAGE ?= rclone/rclone:1.76.0
S3_TEST_PORT ?= 19020
.PHONY: attachments-s3-test
attachments-s3-test: ## Check S3 attachment storage against a throwaway S3-compatible server
	@set -e; name=$${COMPOSE_PROJECT_NAME:-open-fimra-dev}-s3-test; \
		docker run -d --rm --name "$$name" --memory 128m -p 127.0.0.1:$(S3_TEST_PORT):8080 $(S3_TEST_IMAGE) \
			serve s3 --auth-key fimra-test-key,fimra-test-secret --addr :8080 /data > /dev/null; \
		trap 'docker stop "$$name" > /dev/null' EXIT; \
		for i in $$(seq 1 30); do curl -s -o /dev/null http://127.0.0.1:$(S3_TEST_PORT)/ && break; sleep 0.5; done; \
		cd services/api && FIMRA_TEST_S3_ENDPOINT=http://127.0.0.1:$(S3_TEST_PORT) FIMRA_TEST_S3_BUCKET=fimra-test \
			FIMRA_TEST_S3_ACCESS_KEY=fimra-test-key FIMRA_TEST_S3_SECRET_KEY=fimra-test-secret \
			go test -run '^TestS3AttachmentsIntegration$$' -count=1 -v .

api-lint: ## Check core API formatting and run Go vet
	cd services/api && unformatted=$$(gofmt -l .) && if [[ -n "$$unformatted" ]]; then printf '%s\n' "$$unformatted"; exit 1; fi
	cd services/api && go vet ./...

.PHONY: form-schema-test
FORM_SCHEMA := packages/form-schema
form-schema-test: ## Run the Go and TypeScript form validators on the shared examples (needs make web-install)
	cd $(FORM_SCHEMA) && unformatted=$$(gofmt -l .) && if [[ -n "$$unformatted" ]]; then printf '%s\n' "$$unformatted"; exit 1; fi
	cd $(FORM_SCHEMA) && go vet ./... && go test -count=1 ./...
	node --test $(FORM_SCHEMA)/ts/form-schema.test.ts
	@test -x apps/web/node_modules/.bin/tsc || { echo "Run make web-install first: the type check uses the web app's TypeScript."; exit 1; }
	apps/web/node_modules/.bin/tsc -p $(FORM_SCHEMA)/tsconfig.json

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
# The XLSForm tests round-trip the example forms from packages/form-schema.
FORM_EXAMPLES_MOUNT = -v "$(CURDIR)/packages/form-schema/examples:/form-examples:ro"

geo-worker-up: ## Build and start the geo worker for layer imports and exports
	install -d -m 700 "$(FIMRA_UPLOAD_DIR)" "$(FIMRA_EXPORT_DIR)"
	$(COMPOSE) up -d --build geo-worker

geo-test: ## Run the geo worker unit tests in the GDAL container
	install -d -m 700 "$(FIMRA_UPLOAD_DIR)" "$(FIMRA_EXPORT_DIR)"
	$(COMPOSE) build geo-worker
	$(COMPOSE) run --rm --no-deps $(FORM_EXAMPLES_MOUNT) geo-worker python -m unittest discover -v

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

# XLSForm conversion (PLAN.md section 5.4). Output goes to the ignored .xlsforms/ folder unless OUT is given.
.PHONY: xlsform-import xlsform-export xlsform-test-integration xlsform-odk-check
XLSFORM_OUT = $(abspath $(or $(OUT),.xlsforms))
XLSFORM_RUN = install -d -m 700 "$(XLSFORM_OUT)" && $(COMPOSE) run --rm --no-deps \
	-v "$(abspath $(FILE)):/in/$(notdir $(FILE)):ro" -v "$(XLSFORM_OUT):/out" geo-worker python xlsform_cli.py
PYXFORM_VERSION := 3.1.0
PYTHON_IMAGE := python:3.12.12-slim

xlsform-import: ## Convert an XLSForm given FILE to a draft form and a review report in OUT (default .xlsforms/)
	@test -f "$(FILE)" || { echo "Give the workbook as FILE=path/to/form.xlsx"; exit 2; }
	$(COMPOSE) build geo-worker
	$(XLSFORM_RUN) import "/in/$(notdir $(FILE))" /out

xlsform-export: ## Convert a form file given FILE to an XLSForm in OUT (default .xlsforms/)
	@test -f "$(FILE)" || { echo "Give the form as FILE=path/to/form.json"; exit 2; }
	$(COMPOSE) build geo-worker
	$(XLSFORM_RUN) export "/in/$(notdir $(FILE))" /out

xlsform-test-integration: ## Run XLSForm conversion jobs through the worker on the running, migrated database
	$(COMPOSE) build geo-worker
	$(COMPOSE) run --rm -e FIMRA_FORM_JOB_TEST=1 $(FORM_EXAMPLES_MOUNT) geo-worker python -m unittest -v test_form_jobs

xlsform-odk-check: ## Export every example form and convert it with pyxform, the converter ODK uses
	$(COMPOSE) build geo-worker
	install -d -m 700 "$(CURDIR)/.xlsforms/odk-check"
	$(COMPOSE) run --rm --no-deps $(FORM_EXAMPLES_MOUNT) -v "$(CURDIR)/.xlsforms/odk-check:/out" geo-worker \
		sh -c 'for f in /form-examples/*.json; do python xlsform_cli.py export "$$f" /out > /dev/null || exit 1; done'
	docker run --rm -v "$(CURDIR)/.xlsforms/odk-check:/xls:ro" $(PYTHON_IMAGE) sh -c \
		'pip install --quiet --disable-pip-version-check --root-user-action=ignore pyxform==$(PYXFORM_VERSION) && \
		for f in /xls/*.xlsx; do echo "$$f"; xls2xform --skip_validate "$$f" /tmp/form.xml || exit 1; done'

# Field app (Flutter). Everything runs in a pinned Flutter image through
# Docker, so no Flutter or Android SDK install is needed. Packages, Gradle
# files and the Android parts the build downloads (NDK, CMake, platforms)
# are cached in the ignored .mobile-cache folder.
FLUTTER_IMAGE ?= ghcr.io/cirruslabs/flutter:3.44.0@sha256:46691e311715845de03a3ba4753a475476936805b29431b1f00f1816981033f8
MOBILE_MEMORY ?= 4g
MOBILE_CPUS ?= 2
# Extra docker run flags, for example a network setting your machine needs.
MOBILE_DOCKER_FLAGS ?=
# Addresses built into the Android test build. The app can change them under Server settings.
MOBILE_API_URL ?= http://127.0.0.1:18090
MOBILE_OIDC_ISSUER ?= http://127.0.0.1:18180/realms/open-fimra
MOBILE_TEST_API_ADDR ?= 127.0.0.1:18099
MOBILE_WEB_PORT ?= 4300
MOBILE_SHOTS ?= $(HOME)/open-fimra-screenshots/field-app
MAPLIBRE_JS_VERSION := 6.16.0
MAPLIBRE_JS_SHA512 := gApO66EMtoDZuio680RUBvskR7ahFd/NNhLN4tBAs+Y7FeT4gQV8zVFgYVMRmFxQznOEzecW19w6xOd9XjZjiA==
MOBILE_RUN = docker run --rm --init --memory $(MOBILE_MEMORY) --cpus $(MOBILE_CPUS) \
	-v "$(CURDIR):/repo" -w /repo/apps/mobile \
	-e PUB_CACHE=/repo/.mobile-cache/pub -e GRADLE_USER_HOME=/repo/.mobile-cache/gradle \
	-v "$(CURDIR)/.mobile-cache/android-ndk:/opt/android-sdk-linux/ndk" \
	-v "$(CURDIR)/.mobile-cache/android-cmake:/opt/android-sdk-linux/cmake" \
	-v "$(CURDIR)/.mobile-cache/android-platforms:/opt/android-sdk-linux/platforms" \
	-e GRADLE_OPTS=-Dorg.gradle.daemon=false \
	$(MOBILE_DOCKER_FLAGS) $(FLUTTER_IMAGE)
MOBILE_DEFINES = --dart-define=FIMRA_API_URL=$(MOBILE_API_URL) --dart-define=FIMRA_OIDC_ISSUER=$(MOBILE_OIDC_ISSUER)

.PHONY: mobile-deps mobile-tokens mobile-lint mobile-test mobile-build mobile-web mobile-test-integration mobile-walkthrough

mobile-deps: ## Fetch the field app's locked Flutter packages in the container
	install -d .mobile-cache/pub .mobile-cache/gradle .mobile-cache/android-ndk .mobile-cache/android-cmake .mobile-cache/android-platforms
	$(MOBILE_RUN) flutter --suppress-analytics pub get --enforce-lockfile

mobile-tokens: mobile-deps ## Regenerate the field app's theme from docs/design-system/tokens.json
	$(MOBILE_RUN) dart --suppress-analytics run tool/generate_tokens.dart

mobile-lint: mobile-deps ## Check the field app's formatting, analysis and generated theme
	$(MOBILE_RUN) bash -c 'dart --suppress-analytics format --output=none --set-exit-if-changed lib test test_integration tool && \
		flutter --suppress-analytics analyze --fatal-infos && dart --suppress-analytics run tool/generate_tokens.dart --check'

mobile-test: mobile-deps ## Run the field app's unit and widget tests in the container
	$(MOBILE_RUN) flutter --suppress-analytics test

mobile-build: mobile-deps ## Build the field app's Android test build (debug APK) in the container
	$(MOBILE_RUN) flutter --suppress-analytics build apk --debug $(MOBILE_DEFINES)
	@echo "Built apps/mobile/build/app/outputs/flutter-apk/app-debug.apk"

mobile-web: mobile-deps ## Build the field app for a browser, for automated tests only
	scripts/fetch-maplibre-js.sh $(MAPLIBRE_JS_VERSION) $(MAPLIBRE_JS_SHA512) apps/mobile/web/vendor/maplibre-gl
	$(MOBILE_RUN) flutter --suppress-analytics build web --release --no-web-resources-cdn

MOBILE_SCRIPT_ENV = MOBILE_DB=fimra_mobile_test \
	MOBILE_DATABASE_URL="postgres://$(FIMRA_DB_USER):$(FIMRA_DB_PASSWORD)@127.0.0.1:$(FIMRA_DB_PORT)/fimra_mobile_test?sslmode=disable" \
	MOBILE_API_ADDR=$(MOBILE_TEST_API_ADDR) MOBILE_WEB_PORT=$(MOBILE_WEB_PORT) \
	FIMRA_OIDC_ISSUER="$(FIMRA_OIDC_ISSUER)" FIMRA_OIDC_AUDIENCE="$(FIMRA_OIDC_AUDIENCE)" \
	GOOSE_VERSION=$(GOOSE_VERSION) COMPOSE_CMD="$(COMPOSE)" SHOTS="$(MOBILE_SHOTS)" \
	PLAYWRIGHT_IMAGE=mcr.microsoft.com/playwright:v1.63.0-noble FLUTTER_IMAGE=$(FLUTTER_IMAGE) \
	MOBILE_MEMORY=$(MOBILE_MEMORY) MOBILE_CPUS=$(MOBILE_CPUS) MOBILE_DOCKER_FLAGS="$(MOBILE_DOCKER_FLAGS)"

mobile-test-integration: mobile-deps ## Test the field app's API client as each demo user against a fresh copy of the demo data
	MODE=test $(MOBILE_SCRIPT_ENV) scripts/mobile-integration.sh

mobile-walkthrough: mobile-web ## Sign in to the browser build as the beat guard in headless Chromium, acknowledge an alert and save screenshots outside the repository
	MODE=walkthrough $(MOBILE_SCRIPT_ENV) scripts/mobile-integration.sh
