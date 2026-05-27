.PHONY: up down restart pull ps logs health build push test test-services \
        rehearsal rehearsal-analyst clean db-psql migrate migrate-up help \
        builder-regression analyst-llm-smoke

# ──────────────────────────────────────────────────────────────────────────────
# text2ontology — single docker compose stack (docker-compose.yml).
# The nginx `gateway` is the ONLY published port (28080); Postgres, the 6 Go
# services, and the observability stack are internal (obs UIs on 127.0.0.1:
# grafana 3000 / prometheus 9090 / jaeger 16686 / alertmanager 9093).
#
# compose AUTO-READS `.env` for overrides; every secret has a safe dev default
# baked into docker-compose.yml, so `make up` works with zero config. For
# production: cp .env.example .env, set strong secrets, REQUIRE_STRONG_SECRETS=true.
# ──────────────────────────────────────────────────────────────────────────────

IMAGES   = gateway frontend backend-api agent-server recall-server lakehouse-sql-server mcp-tools-server collector-server
REGISTRY = ghcr.io/agentofreef

help: ## Show available targets
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?##"} {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ── Stack lifecycle (pulls prebuilt :latest images from GHCR) ──────────────────

up: ## Pull prebuilt images and start the whole stack
	@docker compose up -d

down: ## Stop and remove all containers (add ARGS=-v to also drop volumes)
	@docker compose down $(ARGS)

restart: ## Re-pull latest images and force-recreate
	@docker compose pull && docker compose up -d --force-recreate

pull: ## Pull all prebuilt images from GHCR
	@docker compose pull

ps: ## Show container status
	@docker compose ps

logs: ## Tail logs for all services (use `make logs SVC=backend-api` for one)
	@if [ -n "$(SVC)" ]; then docker compose logs -f $(SVC); else docker compose logs -f; fi

health: ## Probe the gateway (sole public ingress) + show container health
	@curl -fsS --max-time 3 http://localhost:28080/healthz && echo "  gateway :28080 → ok" || echo "  gateway :28080 → UNREACHABLE"
	@docker compose ps

# ── Build / publish images (maintainers; CI publishes :latest on push to main) ─
# compose no longer carries build: sections — it pulls prebuilt images. Build
# locally with plain docker build against each service Dockerfile.

build: ## Build all images locally and tag :latest
	@for svc in $(IMAGES); do \
	  echo "▼// build $$svc"; \
	  docker build -f services/$$svc/Dockerfile -t $(REGISTRY)/text2ontology-$$svc:latest . || exit 1; \
	done

push: build ## Build then push all :latest images to GHCR
	@for svc in $(IMAGES); do docker push $(REGISTRY)/text2ontology-$$svc:latest || exit 1; done

# ── Testing ──────────────────────────────────────────────────────────────────

test: test-services ## Run all unit tests

test-services: ## Run go build + go test ./... across all 6 services
	@for svc in backend-api agent-server recall-server lakehouse-sql-server mcp-tools-server collector-server; do \
	  echo "▼// $$svc"; \
	  (cd services/$$svc && go build ./... && go test ./... -count=1) || exit 1; \
	done

rehearsal: ## Run scripts/rehearsal-1.sh end-to-end regression (24 SSE turns)
	@bash scripts/rehearsal-1.sh

builder-regression: ## Run builder handler regression tests (needs DATABASE_URL for DB-backed tests)
	cd services/agent-server && go test ./handler/ -run TestBuilder -timeout 30s

rehearsal-analyst: ## Run analyst mode E2E probe (needs running stack + DATABASE_URL)
	@bash scripts/rehearsal-analyst.sh

analyst-llm-smoke: ## Phase 2B gate: 25-tool surface compatible w/ claude/openai/deepseek
	@bash scripts/analyst-llm-smoke.sh

# ── Database (Postgres is internal — operate via the container) ────────────────

db-psql: ## Open a psql shell inside the postgres container
	@docker compose exec postgres psql -U text2ontology_community -d text2ontology_community

migrate: ## Apply a single migration file (pass FILE=docs/migrations/<name>.sql)
	@if [ -z "$(FILE)" ]; then echo "ERROR: FILE=docs/migrations/<name>.sql is required"; exit 2; fi
	@docker compose exec -T postgres psql -U text2ontology_community -d text2ontology_community < "$(FILE)"

migrate-up: ## Re-run the full migration runner (schema + roles + versioned migrations)
	@docker compose run --rm db-migrate

# ── Cleanup ──────────────────────────────────────────────────────────────────

clean: ## Remove local build artifacts (Go binaries, Next.js out/)
	@rm -f services/*/server
	@rm -rf frontend/out frontend/.next
	@echo "  cleaned: services/*/server, frontend/out, frontend/.next"
