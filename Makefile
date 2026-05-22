.PHONY: up down restart ps logs health build build-frontend build-services \
        rebuild rebuild-clean test test-services rehearsal rehearsal-analyst clean \
        db-psql migrate help builder-regression analyst-llm-smoke

# ──────────────────────────────────────────────────────────────────────────────
# text2ontology — docker-compose wrapper for the 6-service stack
# (frontend + backend-api + agent-server + recall-server + lakehouse-sql-server
#  + mcp-tools-server) plus 4 observability containers (otel / jaeger /
#  prometheus / grafana). Host ports: 18080 / 18090-18095 plus 127.0.0.1-only
#  3001 / 9090 / 16686 for the observability UIs.
# ──────────────────────────────────────────────────────────────────────────────

# Load .env.shared into the compose environment. Compose does NOT auto-read
# .env.shared — we export it first so variables like DATABASE_URL_CONTAINER
# and GRAFANA_ADMIN_PASSWORD interpolate in docker-compose.yml.
define COMPOSE
	set -a && . ./.env.shared && set +a && docker compose
endef

help: ## Show available targets
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?##"} {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# ── Stack lifecycle ──────────────────────────────────────────────────────────

up: ## Start all 6 containers in background
	@$(COMPOSE) up -d

down: ## Stop and remove all containers
	@$(COMPOSE) down

restart: ## Force-recreate all containers with latest images
	@$(COMPOSE) up -d --force-recreate

ps: ## Show container status
	@$(COMPOSE) ps

logs: ## Tail logs for all services (use `make logs SVC=backend-api` for one)
	@if [ -n "$(SVC)" ]; then $(COMPOSE) logs -f $(SVC); else $(COMPOSE) logs -f; fi

health: ## Probe all 6 /healthz endpoints
	@for p in 18080 18090 18092 18093 18094 18095; do \
	  printf "  :%s → " $$p; \
	  curl -sS --max-time 3 localhost:$$p/healthz 2>/dev/null | head -c 60 || echo "UNREACHABLE"; \
	  echo; \
	done

# ── Build ────────────────────────────────────────────────────────────────────

build: build-services build-frontend ## Build all 6 images

build-services: ## Build the 5 Go service images
	@$(COMPOSE) build backend-api agent-server recall-server lakehouse-sql-server mcp-tools-server

build-frontend: ## Build the frontend nginx image
	@$(COMPOSE) build frontend

rebuild: build restart ## Build all images (with cache) + force-recreate containers
	@echo "▼// rebuild complete — probing health…"
	@$(MAKE) --no-print-directory health

rebuild-clean: ## Build all images WITHOUT docker layer cache (slow, ~8-10 min) + force-recreate
	@$(COMPOSE) build --no-cache frontend backend-api agent-server recall-server lakehouse-sql-server mcp-tools-server
	@$(COMPOSE) up -d --force-recreate
	@echo "▼// rebuild-clean complete — probing health…"
	@$(MAKE) --no-print-directory health

# ── Testing ──────────────────────────────────────────────────────────────────

test: test-services ## Run all unit tests

test-services: ## Run go test ./... across all 5 services
	@for svc in backend-api agent-server recall-server lakehouse-sql-server mcp-tools-server; do \
	  echo "▼// $$svc"; \
	  (cd services/$$svc && go build ./... && go test ./... -count=1) || exit 1; \
	done

rehearsal: ## Run scripts/rehearsal-1.sh end-to-end regression (24 SSE turns)
	@bash scripts/rehearsal-1.sh

builder-regression: ## Run builder handler regression tests (needs DATABASE_URL for DB-backed tests)
	cd services/agent-server && go test ./handler/ -run TestBuilder -timeout 30s

rehearsal-analyst: ## Run analyst mode E2E probe (needs agent-server :18092 + backend-api :18090 + DATABASE_URL)
	@bash scripts/rehearsal-analyst.sh

analyst-llm-smoke: ## Phase 2B gate: 25-tool surface compatible w/ claude/openai/deepseek
	@bash scripts/analyst-llm-smoke.sh

# ── Database ─────────────────────────────────────────────────────────────────

db-psql: ## Open psql shell on the project DB ($$DATABASE_URL from .env.shared)
	@set -a && . ./.env.shared && set +a && psql "$$DATABASE_URL"

migrate: ## Apply a single migration file (pass FILE=docs/migrations/<name>.sql)
	@if [ -z "$(FILE)" ]; then echo "ERROR: FILE=docs/migrations/<name>.sql is required"; exit 2; fi
	@set -a && . ./.env.shared && set +a && psql "$$DATABASE_URL" -f "$(FILE)"

migrate-up: ## Run the full migration runner (baseline + roles + versioned migrations) vs $$DATABASE_URL
	@set -a && . ./.env.shared && set +a && \
		SCHEMA_FILE=docs/schema/schema.sql ROLES_FILE=ops/db-roles.sql MIGRATIONS_DIR=docs/migrations \
		sh scripts/run-migrations.sh

# ── Cleanup ──────────────────────────────────────────────────────────────────

clean: ## Remove local build artifacts (Go binaries, Next.js out/)
	@rm -f services/*/server
	@rm -rf frontend/out frontend/.next
	@echo "  cleaned: services/*/server, frontend/out, frontend/.next"
