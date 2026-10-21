# Native toolchains: Go 1.26, Node 22, and control/.venv (see README).
COMPOSE      := docker compose

.PHONY: help env build up up-bundled down logs ps test test-worker test-control test-panel bin bench

help:
	@grep -E '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-14s %s\n", $$1, $$2}'

env: ## Create .env with generated secrets (no-op if it exists)
	./deploy/init-env.sh

build: ## Build fsg-control and fsg-worker images
	$(COMPOSE) build

up-bundled: env ## Run everything, including bundled MariaDB/Redis/ClickHouse
	$(COMPOSE) --profile bundled up -d --build

up: env ## Run control + worker against stores configured in .env
	$(COMPOSE) up -d --build

down: ## Stop all services (data volumes are kept)
	$(COMPOSE) --profile bundled down

logs: ## Follow logs
	$(COMPOSE) --profile bundled logs -f --tail=100

ps: ## Show service status
	$(COMPOSE) --profile bundled ps

test: test-worker test-control test-panel ## Run all tests

test-worker:
	cd worker && go vet ./... && go test -race ./...

bin: ## Build fsg-worker and fsg-loadgen into ./bin (needs Go)
	cd worker && go build -o ../bin/fsg-worker ./cmd/fsg-worker
	cd loadgen && GOWORK=off go build -o ../bin/fsg-loadgen ./cmd/fsg-loadgen

# make bench GW=127.0.0.1:20001 SYS=happy PW=<password> TPS=25000 DUR=60s
bench: bin ## Run fsg-loadgen against a gateway
	./bin/fsg-loadgen -addr $(GW) -system-id $(SYS) -password $(PW) -binds $${BINDS:-8} -tps $(TPS) -duration $(DUR)

test-control:
	cd control && .venv/bin/ruff check . && .venv/bin/ruff format --check . && .venv/bin/pytest -q

test-panel:
	cd panel && npm run build
