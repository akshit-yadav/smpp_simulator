# Toolchains run in Docker, so only Docker is required on the host.
GO_IMAGE     := golang:1.26-alpine
PY_IMAGE     := python:3.12-slim
NODE_IMAGE   := node:22-alpine
COMPOSE      := docker compose

.PHONY: help env build up up-bundled down logs ps test test-worker test-control test-panel lint

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
	docker run --rm -v "$(CURDIR)/worker:/src" -w /src $(GO_IMAGE) sh -c 'go vet ./... && go test ./...'

test-control:
	docker run --rm -v "$(CURDIR)/control:/src" -w /src $(PY_IMAGE) sh -c \
	  'pip install -q -e ".[dev]" && ruff check . && ruff format --check . && pytest -q; s=$$?; rm -rf *.egg-info; exit $$s'

test-panel:
	docker run --rm -v "$(CURDIR)/panel:/src" -w /src $(NODE_IMAGE) sh -c \
	  'npm ci --no-audit --no-fund && npm run build; s=$$?; rm -rf node_modules dist; exit $$s'
