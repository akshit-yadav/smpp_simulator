# Fake SMPP Gateway (FSG)

FSG simulates SMPP 3.4 vendor gateways so you can load-test and HA-test SMS platforms
(sized for a 25,000 TPS benchmark). An admin creates fake gateways in a web panel. Each
gateway is a real SMPP listener with its own port, credentials, outcome rules, DLR
behaviour, MO traffic and TPS limit.

> **Status: M0 scaffolding.** The services start, log JSON, report health and register
> workers. The SMPP server, outcome engine, reports and licensing come in later
> milestones. See [docs/build-prompt.md](docs/build-prompt.md).

## Layout

```
worker/         Go: one process per host, many SMPP listeners       -> image fsg-worker
control/        Python 3.12 + FastAPI: REST API, scheduler, checks  -> image fsg-control
  migrations/     MariaDB migrations (Alembic)
  clickhouse/     ClickHouse DDL
panel/          React + TypeScript + Vite SPA, served by fsg-control
loadgen/        Go: open-loop windowed SMPP load generator (M2)
tools/licgen/   Go: licence signing CLI, HWPL internal only (M8)
deploy/         Compose support files: env bootstrap, bundled MariaDB config
docs/           Build brief, technical design, plan
```

## Run locally

Needs Docker with Compose v2 on Linux. `fsg-control` and `fsg-worker` use
`network_mode: host`, which Docker Desktop on macOS/Windows does not fully support.

```sh
make env           # creates .env with random secrets (or: cp .env.example .env and edit)
make up-bundled    # builds images, starts control + worker + bundled MariaDB/Redis/ClickHouse
make ps
make logs
make down          # stops everything; data volumes are kept
```

Without `make`:

```sh
./deploy/init-env.sh
docker compose --profile bundled up -d --build
```

Then:

| What | Where |
|---|---|
| Panel | http://127.0.0.1:8080/ |
| API docs | http://127.0.0.1:8080/api/docs |
| Liveness / readiness | `/healthz`, `/readyz` (503 with each store's status if any are down) |
| Worker metrics | http://127.0.0.1:9108/metrics |
| SMPP listeners | `FSG_PORT_RANGE`, default 20000–20999 |

### Using the MariaDB, Redis and ClickHouse already on the host

Point the `FSG_MARIADB_*`, `FSG_REDIS_*` and `FSG_CLICKHOUSE_*` variables in `.env` at the
existing servers, create the `fsg` database and users there, and start without the profile:

```sh
make up            # = docker compose up -d --build
```

The bundled stores publish only on `127.0.0.1` and on non-default ports (13306, 16379,
18123/19000), so they never clash with stores already on the host.

## Configuration

All settings are `FSG_*` environment variables. [.env.example](.env.example) documents
them all. `.env` holds secrets and is git-ignored. Never commit it.

## Development

Toolchains run in Docker, so only Docker is needed:

```sh
make test          # Go vet + tests, ruff + pytest, panel typecheck + build
```

Or with local toolchains:

```sh
cd worker  && go test ./...
cd control && pip install -e ".[dev]" && pytest
cd panel   && npm ci && npm run dev    # proxies /api to the control plane on :8080
```

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs the same checks and builds
both images on every push to `main` and on every pull request.
