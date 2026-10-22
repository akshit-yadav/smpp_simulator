# Native toolchains: Go 1.26, Node 22, and control/.venv (see README).
COMPOSE      := docker compose

.PHONY: help env build up up-bundled down logs ps test test-stores test-worker test-control test-panel bin bench

# Throwaway MariaDB/Redis/ClickHouse for tests (make test-stores starts them).
TEST_MARIADB_URL    ?= mysql+pymysql://root:testroot@127.0.0.1:13399
TEST_REDIS_ADDR     ?= 127.0.0.1:16399
TEST_CLICKHOUSE_URL ?= http://127.0.0.1:18199

help:
	@grep -E '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-14s %s\n", $$1, $$2}'

env: ## Create .env with generated secrets (no-op if it exists)
	./deploy/init-env.sh

build: ## Build psg-control and psg-worker images
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

test: test-stores test-worker test-control test-panel ## Run all tests

test-stores: ## Start throwaway MariaDB (13399), Redis (16399) and ClickHouse (18199) containers for tests
	@docker start psg-test-mariadb >/dev/null 2>&1 || docker run -d --name psg-test-mariadb \
	  -e MARIADB_ROOT_PASSWORD=testroot -p 127.0.0.1:13399:3306 mariadb:10.11 >/dev/null
	@docker start psg-test-redis >/dev/null 2>&1 || docker run -d --name psg-test-redis \
	  -p 127.0.0.1:16399:6379 redis:7.2-alpine >/dev/null
	@docker start psg-test-clickhouse >/dev/null 2>&1 || docker run -d --name psg-test-clickhouse \
	  -e CLICKHOUSE_SKIP_USER_SETUP=1 --ulimit nofile=262144:262144 \
	  -p 127.0.0.1:18199:8123 clickhouse/clickhouse-server:24.8 >/dev/null
	@until curl -sf $(TEST_CLICKHOUSE_URL)/ping >/dev/null; do sleep 1; done
	@until docker exec psg-test-mariadb healthcheck.sh --connect --innodb_initialized >/dev/null 2>&1; do sleep 1; done

test-worker:
	cd worker && go vet ./... && PSG_TEST_REDIS_ADDR=$(TEST_REDIS_ADDR) \
	  PSG_TEST_CLICKHOUSE_URL=$(TEST_CLICKHOUSE_URL) go test -race ./...

bin: ## Build psg-worker, psg-loadgen and psg-tester into ./bin (needs Go)
	cd worker && go build -o ../bin/psg-worker ./cmd/psg-worker
	cd loadgen && GOWORK=off go build -o ../bin/psg-loadgen ./cmd/psg-loadgen
	cd loadgen && GOWORK=off go build -o ../bin/psg-tester ./cmd/psg-tester

# make bench GW=127.0.0.1:20001 SYS=happy PW=<password> TPS=25000 DUR=60s
bench: bin ## Run psg-loadgen against a gateway
	./bin/psg-loadgen -addr $(GW) -system-id $(SYS) -password $(PW) -binds $${BINDS:-8} -tps $(TPS) -duration $(DUR)

test-control:
	cd control && .venv/bin/ruff check . && .venv/bin/ruff format --check . && \
	  PSG_TEST_MARIADB_URL=$(TEST_MARIADB_URL) PSG_TEST_REDIS_ADDR=$(TEST_REDIS_ADDR) \
	  PSG_TEST_CLICKHOUSE_URL=$(TEST_CLICKHOUSE_URL) .venv/bin/pytest -q

test-panel:
	cd panel && npm run build
