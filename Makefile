.PHONY: bootstrap up down nuke test lint seed doctor chaos help

COMPOSE     := docker compose -f infra/docker-compose.yml --profile full
COMPOSE_MIN := docker compose -f infra/docker-compose.yml

##@ General
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development
bootstrap: ## Install all toolchain dependencies (run once on new machine)
	@bash scripts/bootstrap.sh

up: ## Start all local services (Docker Compose)
	$(COMPOSE) up -d --wait
	@echo "Stack ready. Run: make doctor"

up-min: ## Start minimal services (no ClickHouse/Kafka/Qdrant)
	$(COMPOSE_MIN) up -d --wait

down: ## Stop services, keep volumes
	$(COMPOSE) down

nuke: ## Stop services AND destroy all volumes
	$(COMPOSE) down -v --remove-orphans

seed: ## Populate all services with fixture data (5-tenant universe)
	@pnpm --filter @platform/scripts run seed

doctor: ## Diagnose local environment (ports, RAM, services, certs)
	@bash scripts/verify-env.sh

chaos: ## Kill a random compose service to test reconnection
	@bash scripts/chaos.sh

##@ Testing
test: ## Run all tests (unit + integration)
	@go test ./... && pnpm turbo run test

test-unit: ## Run unit tests only
	@go test ./... -short && pnpm turbo run test:unit

test-integration: ## Run integration tests (requires compose up)
	@go test ./... -run Integration && pnpm turbo run test:integration

##@ Code Quality
lint: ## Run all linters
	@golangci-lint run ./... && pnpm turbo run lint

fmt: ## Format all code
	@gofmt -w . && pnpm turbo run fmt

typecheck: ## Run TypeScript type checking
	@pnpm turbo run typecheck

##@ Build
build: ## Build all service images
	@pnpm turbo run build:image

##@ Database
migrate: ## Run forward migrations (DATABASE_URL or POSTGRES_* env vars)
	@go run ./scripts/migrate/main.go up

migrate-status: ## Show current migration version
	@go run ./scripts/migrate/main.go status

migrate-hash: ## Regenerate migrations/hashes.manifest (run after adding a new migration)
	@go run ./scripts/migrate/main.go hash

migrate-down: ## Roll back all migrations in dev/staging only (requires MIGRATE_ALLOW_DOWN=true)
	@MIGRATE_ALLOW_DOWN=true go run ./scripts/migrate/main.go down

psql-dev: ## Open psql as app_login_user with app.tenant_id preset to acme fixture tenant
	@PGPASSWORD=$${POSTGRES_PASSWORD:-changeme_app} psql \
	  -h $${POSTGRES_HOST:-127.0.0.1} \
	  -U app_login_user \
	  -d $${POSTGRES_DB:-governance} \
	  -c "SET app.tenant_id='018f4e1a-0001-7000-8000-000000000001';" \
	  --variable=PROMPT1="governance[acme]> "
