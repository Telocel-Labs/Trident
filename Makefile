.PHONY: all dev stop db migrate indexer grpc-api go-api sdk-build test lint help

# Load environment variables from .env if it exists
ifneq (,$(wildcard .env))
    include .env
    export
endif

# Default target
all: dev

help: ## Show this help message
	@echo ""
	@echo "Trident — Soroban Event Indexer for Stellar"
	@echo ""
	@echo "Usage: make <target>"
	@echo ""
	@echo "Targets:"
	@echo "  dev        Start the full development stack (DB + migrate + all services)"
	@echo "  stop       Stop all Docker containers"
	@echo "  db         Start only Postgres and Redis via Docker Compose"
	@echo "  migrate    Apply database migrations (requires sqlx-cli or psql)"
	@echo "  indexer    Run the Rust indexer with dev env vars"
	@echo "  grpc-api   Run the Rust gRPC API with dev env vars"
	@echo "  go-api     Run the Go REST API"
	@echo "  sdk-build  Build the TypeScript and React SDKs"
	@echo "  test       Run all unit tests (integration tests require TEST_DATABASE_URL)"
	@echo "  lint       Run all linters (cargo fmt, clippy, go vet, tsc)"
	@echo "  deploy     Deploy all services to Fly.io (requires flyctl)"
	@echo "  help       Show this help message"
	@echo ""

dev: db migrate
	@echo "Starting indexer, grpc-api, and go-api..."
	@trap 'kill 0' INT TERM EXIT; \
	cargo run --bin trident-indexer 2>&1 | sed -e 's/^/[indexer] /' & \
	cargo run --bin trident-api 2>&1 | sed -e 's/^/[grpc-api] /' & \
	cd services/api && go run main.go 2>&1 | sed -e 's/^/[go-api] /' & \
	wait

stop:
	docker compose -f docker/docker-compose.dev.yml down

db:
	docker compose -f docker/docker-compose.dev.yml up -d
	@echo "Waiting for PostgreSQL to be healthy..."
	@until docker exec $$(docker compose -f docker/docker-compose.dev.yml ps -q postgres) pg_isready -U trident -d trident >/dev/null 2>&1; do \
		sleep 1; \
	done
	@echo "PostgreSQL is healthy!"

migrate:
	@echo "Applying database migrations..."
	@if command -v sqlx >/dev/null 2>&1; then \
		sqlx db create --database-url "$(DATABASE_URL)" || true; \
		sqlx migrate run --database-url "$(DATABASE_URL)" --source database/migrations; \
	else \
		echo "sqlx-cli not found, attempting raw psql migrations..."; \
		psql "$(DATABASE_URL)" -f database/schema.sql; \
		for f in database/migrations/*.sql; do \
			echo "Applying $$f..."; \
			psql "$(DATABASE_URL)" -f "$$f"; \
		done; \
	fi

indexer:
	cargo run --bin trident-indexer

grpc-api:
	cargo run --bin trident-api

go-api:
	cd services/api && go run main.go

sdk-build:
	cd sdk/typescript && npm install && npm run build
	cd sdk/react && npm install && npm run build

test:
	cargo test --all
	cd services/api && go test ./...
	cd sdk/typescript && npm install && npm run test
	cd sdk/react && npm install && npm run test

deploy: ## Deploy all services to Fly.io in dependency order (requires flyctl)
	@echo "Deploying gRPC API..."
	fly deploy -c fly/grpc-api.toml --remote-only
	@echo "Deploying Indexer..."
	fly deploy -c fly/indexer.toml --remote-only
	@echo "Deploying Go REST API..."
	fly deploy -c fly/api.toml --remote-only
	@echo "All services deployed."

lint:
	cargo fmt --all -- --check
	cargo clippy --all-targets --all-features -- -D warnings
	cd services/api && go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		cd services/api && golangci-lint run; \
	fi
	cd sdk/typescript && npm install && npm run lint
	cd sdk/react && npm install && npm run lint>>>>>>> 7e309c3 (chore(db): integrate sqlx-cli database migration management)