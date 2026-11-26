# Loaded after .env so shared services cannot pick up another stack's settings.
# Explicit command-line assignments take precedence over Compose's environment.
override SHARED_SETTINGS := $(shell sed '/^#/d' deploy/compose/compose.shared.env)
shared-setting = $(patsubst $(1)=%,%,$(filter $(1)=%,$(SHARED_SETTINGS)))
override SHARED_COMPOSE := env COMPOSE_PROFILES= $(SHARED_SETTINGS) \
	docker compose --env-file /dev/null -p open-fimra-shared -f $(COMPOSE_FILE) --profile mail --profile push

.PHONY: shared-up shared-ps shared-down worktree-db worktree-db-reset worktree-db-drop
shared-up: ## Start the shared database, sign-in, mail and push services if needed
	bash scripts/shared-stack-up.sh $(SHARED_COMPOSE)

shared-ps: ## Show the shared services
	$(SHARED_COMPOSE) ps

shared-down: ## Stop the shared stack for every worktree, keeping its data
	@echo 'Stopping the shared stack affects every worktree using it.'
	$(SHARED_COMPOSE) stop db keycloak mailpit ntfy

ifeq ($(FIMRA_SHARED),1)
# Hash the full path so similarly named or truncated directories stay distinct.
override WORKTREE_ID := $(shell pwd -P | sha256sum | cut -c1-16)
WORKTREE_SLUG := $(shell basename "$$(pwd -P)" | LC_ALL=C tr '[:upper:]' '[:lower:]' | \
	sed -E 's/[^a-z0-9]+/_/g; s/^_//' | cut -c1-23 | sed 's/_$$//')
FIMRA_DB_NAME ?= fimra_wt_$(or $(WORKTREE_SLUG),worktree)_$(WORKTREE_ID)
# Double underscores separate the worktree namespace from disposable test names.
ifeq ($(shell printf '%s\n' '$(subst ','"'"',$(FIMRA_DB_NAME))' | LC_ALL=C grep -Ex '[a-z][a-z0-9_]{0,49}' | grep -Ev '__|_$$|^(postgres|template0|template1)$$'),)
$(error FIMRA_DB_NAME must be a lowercase name of at most 50 characters, starting with a letter, with no double or trailing underscore or reserved database name)
endif
override FIMRA_DB_USER := $(call shared-setting,FIMRA_DB_USER)
override FIMRA_DB_PASSWORD := $(call shared-setting,FIMRA_DB_PASSWORD)
override FIMRA_DB_PORT := $(call shared-setting,FIMRA_DB_PORT)
override FIMRA_KEYCLOAK_PORT := $(call shared-setting,FIMRA_KEYCLOAK_PORT)
override FIMRA_MAILPIT_SMTP_PORT := $(call shared-setting,FIMRA_MAILPIT_SMTP_PORT)
override FIMRA_MAILPIT_HTTP_PORT := $(call shared-setting,FIMRA_MAILPIT_HTTP_PORT)
override FIMRA_NTFY_PORT := $(call shared-setting,FIMRA_NTFY_PORT)
override DATABASE_URL = $(call database-url,$(FIMRA_DB_NAME))
override FIMRA_DATABASE_URL = $(DATABASE_URL)
override FIMRA_OIDC_ISSUER := http://127.0.0.1:$(FIMRA_KEYCLOAK_PORT)/realms/open-fimra
FIMRA_CORS_ORIGINS ?= http://127.0.0.1:$(WEB_PORT),http://localhost:$(WEB_PORT)
MOBILE_TEST_API_ADDR ?= $(FIMRA_HTTP_ADDR)

override DB_COMPOSE := $(SHARED_COMPOSE)
override GEO_COMPOSE := env FIMRA_DB_NAME=$(FIMRA_DB_NAME) FIMRA_DB_USER=$(FIMRA_DB_USER) FIMRA_DB_PASSWORD=$(FIMRA_DB_PASSWORD) \
	docker compose --env-file /dev/null -p open-fimra-geo-$(WORKTREE_ID) -f deploy/compose/compose.geo.yml
override WIZARD_DB := $(FIMRA_DB_NAME)__wizard
override WALK_DB := $(FIMRA_DB_NAME)__wizard_web
override FORMS_WALK_DB := $(FIMRA_DB_NAME)__forms_web
override SUBMISSION_EXPORT_DB := $(FIMRA_DB_NAME)__sub_export
override MOBILE_DB := $(FIMRA_DB_NAME)__mobile

# Only the explicitly listed databases are ever dropped.
worktree-databases = $(FIMRA_DB_NAME),$(WIZARD_DB),$(WALK_DB),$(FORMS_WALK_DB),$(SUBMISSION_EXPORT_DB),$(MOBILE_DB)
shared-db = $(SHARED_COMPOSE) exec -T db psql -Xq -U $(FIMRA_DB_USER) -d postgres -v ON_ERROR_STOP=1 \
	-v base=$(FIMRA_DB_NAME) -v worktree=$(WORKTREE_ID) -v action=$(1) -v databases=$(2) < scripts/shared-db.sql
recreate-db = $(call shared-db,recreate,$(1))

.PHONY: worktree-db-owned
worktree-db-owned:
	@$(call shared-db,check,$(FIMRA_DB_NAME))

# Guard the worktree's main database before any recipe uses it. The disposable
# tests claim/check their own namespace through recreate-db.
db-shell db-migrate db-rollback db-status db-test seed-test api-run api-test-integration ingest-fixture demo-detections demo-dispatch demo-incident-response demo-operations-records demo-response-forms \
	backfill-firms backfill-demo backfill-queue feed-health rotate-settings-key geo-worker-up geo-test-integration \
	export-test-integration situation-report-test-integration xlsform-test-integration shell-walkthrough \
	incident-walkthrough layers-walkthrough notifications-walkthrough: | worktree-db-owned

worktree-db: private override DB_ACTION := create
worktree-db-reset: private override DB_ACTION := reset
worktree-db worktree-db-reset: | shared-up
	@$(call shared-db,$(DB_ACTION),$(worktree-databases))
	$(GOOSE) up
	@set -o pipefail; { \
		$(demo-seed-sql) \
		echo 'COMMIT;'; \
	} | $(PSQL)

worktree-db-drop:
	@$(call shared-db,drop,$(worktree-databases))

else
ifneq ($(filter open-fimra-shared,$(COMPOSE_PROJECT_NAME)),)
$(error The project open-fimra-shared is reserved. Set FIMRA_SHARED=1 to use it)
endif
worktree-db worktree-db-reset worktree-db-drop:
	@echo 'Set FIMRA_SHARED=1 in .env to manage a worktree database.' >&2
	@exit 2
endif

# Help text lives here so help has one entry for each mode.
worktree-db: ## Create, migrate and seed this worktree's database
worktree-db-reset: ## Drop this worktree's databases and recreate its demo database
worktree-db-drop: ## Drop this worktree's demo and test databases
