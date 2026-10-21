# Fake SMPP Gateway (FSG)

FSG simulates SMPP 3.4 vendor gateways so you can load-test and HA-test SMS platforms
(sized for a 25,000 TPS benchmark). An admin creates fake gateways in a web panel. Each
gateway is a real SMPP listener with its own port, credentials, outcome rules, DLR
behaviour, MO traffic and TPS limit.

## Status

| Milestone | State |
|---|---|
| M0 Scaffolding: compose, CI, health, worker registry | ✅ Done |
| M1 Worker core: SMPP 3.4 codec, bind auth, `submit_sm`, `enquire_link`, `unbind`, many listeners | ✅ Done |
| M2 Hot path: outcome engine, TPS limits, timing-wheel DLRs, DLR delivery/queue/retry, load generator | ✅ Done (short benchmark runs, see below) |
| M3 Control plane: admin login, gateway/profile CRUD in MariaDB, Redis config sync | ⏳ Next. Until then gateways come from `config/gateways.json` |
| M4–M9 Reports, panel screens, `submit_multi`/`data_sm`/`query_sm`…, MO, licensing, hardening | ⏳ Not started |

See [docs/build-prompt.md](docs/build-prompt.md) for the full scope.

## Layout

```
worker/         Go: one process per host, many SMPP listeners       -> image fsg-worker
control/        Python 3.12 + FastAPI: REST API, scheduler, checks  -> image fsg-control
  migrations/     MariaDB migrations (Alembic)
  clickhouse/     ClickHouse DDL
panel/          React + TypeScript + Vite SPA, served by fsg-control
loadgen/        Go: open-loop windowed SMPP load generator
tools/licgen/   Go: licence signing CLI, HWPL internal only (M8)
deploy/         Env bootstrap, example gateways file, bundled MariaDB config
config/         Your gateways.json (git-ignored, created by `make env`)
docs/           Build brief, technical design, plan
```

## Run locally

Needs Docker with Compose v2 on Linux. `fsg-control` and `fsg-worker` use
`network_mode: host`, which Docker Desktop on macOS/Windows does not fully support.

```sh
make env           # creates .env and config/gateways.json with random secrets
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
| SMPP listeners | the ports in `config/gateways.json` (samples: 20001–20003) |

## Test the simulator

`make env` creates three sample gateways in `config/gateways.json`. Each gateway's
`system_id` and `password` are in that file.

| Gateway | Port | system_id | Behaviour |
|---|---|---|---|
| vendor-happy | 20001 | `happy` | Accepts everything; DELIVRD DLR after 0.5–2 s |
| vendor-realistic | 20002 | `realistic` | 500 TPS gateway limit; 92% DELIVRD, 5% UNDELIV, 2% EXPIRED (DLR after 10–20 s), 0.5% REJECTD `err:T01`, 0.5% rejected with `ESME_RSYSERR` |
| vendor-broken | 20003 | `broken` | TRX only, max 2 binds; exactly 50% rejected with vendor code 0x401, 50% UNDELIV `err:G01` after exactly 3 s |

**With your own SMPP client or SMS platform.** Point it at `<server IP>:<port>` with the
gateway's `system_id` and `password`, bind as TX, RX or TRX, and send `submit_sm` with
`registered_delivery=1`. DLRs arrive as `deliver_sm` (`esm_class` 0x04) on the RX or TRX
sessions of the same `system_id`:

```
id:2a3f0c8e1d40001 sub:001 dlvrd:001 submit date:2610211231 done date:2610211231 stat:DELIVRD err:000 text:Your OTP is 123456
```

with the TLVs `receipted_message_id`, `message_state` and, if set, `network_error_code`.

> Listeners bind to all interfaces (`FSG_LISTEN_HOST`), so remote platforms can reach
> them. Firewall the port range if the server is public.

**With the bundled load generator:**

```sh
make bin
./bin/fsg-loadgen -addr 127.0.0.1:20001 -system-id happy -password <password> \
    -binds 4 -tps 2000 -duration 30s
```

It prints throughput, `submit_sm_resp` latency percentiles, throttled/rejected counts,
DLR status counts and DLR delay, and exits non-zero on unanswered submits or missing DLRs.

**Change gateways live.** Edit `config/gateways.json`; the worker applies changes within
1 s: new gateways start listening, edits apply to new binds and messages, a changed port
moves the listener, and `"status": "disabled"` (or removing the gateway) closes the
port and unbinds its sessions. An invalid file is rejected and the last good config
keeps running (see `docker compose logs worker`).

**Watch it.** Worker metrics at http://127.0.0.1:9108/metrics: `fsg_submit_total`,
`fsg_dlr_total`, `fsg_bind_total`, `fsg_sessions`, `fsg_dlr_pending`, `fsg_dlr_queued`.

### Gateway file format

`defaults` apply to every gateway; a gateway overrides only the fields it sets. See
[deploy/gateways.example.json](deploy/gateways.example.json).

| Field | Meaning |
|---|---|
| `id`, `name`, `port` | Unique per worker. Port must be in `FSG_PORT_RANGE` and not in `FSG_PORT_EXCLUDE` |
| `system_id`, `password` | Max 15 and 8 characters |
| `status` | `active`, `disabled` or `suspended` |
| `max_binds`, `bind_types` | `0` = unlimited; any of `tx`, `rx`, `trx` |
| `tps.limit`, `tps.scope` | `0` = unlimited; `bind` (each bind gets the limit) or `gateway` (shared). Excess gets `ESME_RTHROTTLED` |
| `dlr.enabled`, `dlr.timing` | `immediate`, `fixed` (`delay_ms`) or `random` (`min_ms`–`max_ms`) |
| `dlr.id_format`, `dlr.date_seconds`, `dlr.include_text`, `dlr.include_tlvs` | DLR format options |
| `outcome.mode` | `fixed` (first outcome), `random` (uniform) or `weighted` (`weight_bp`, basis points totalling 10000) |
| `outcome.distribution` | `probabilistic` (default) or `exact` (every block of 10,000 matches the weights exactly) |
| `outcome.seed` | Optional; makes runs repeatable |
| outcome entries | `command_status` (non-zero = rejected, no DLR; SMPP 3.4 codes or vendor 0x400–0x4FF), `dlr_status`, `dlr_error` (≤3 chars), `network_error_code` `{type, code}`, `dlr_text`, `dlr_delay` `{min_ms, max_ms}` |

A DLR is sent only when DLR is enabled, the message was accepted, and
`registered_delivery` asks for it (bits `01` always, `10` failures only; intermediate
`ENROUTE`/`ACCEPTD` need bit `0x10`). With no RX/TRX session bound, DLRs queue (persisted
to Redis, kept 72 h) and are delivered when a receiver binds. A `deliver_sm_resp` error or
a 30 s timeout is retried up to 5 times with backoff.

## Benchmarks

Worker on loopback, 8 vCPU shared with other services, DLRs on (random 500–2000 ms),
weighted outcomes, `fsg-loadgen` on the same host:

| Offered | Duration | Sent | Errors | `submit_sm_resp` p50 / p99 / p99.9 | DLRs received | DLR delay p99 |
|---|---|---|---|---|---|---|
| 25,000 TPS | 30 s | 749,975 | 0 | 85 µs / 475 µs / 1.3 ms | 100% | 1,988 ms |
| 50,000 TPS | 30 s | 1,499,950 | 0 | 121 µs / 878 µs / 1.6 ms | 100% | 1,988 ms |

The worker used under one CPU core at 50k TPS, and GC pauses stayed under 1 ms. The brief's
full gate (25k TPS for 60 min, 50k TPS for 10 min) still needs a dedicated host: an
earlier 50k run on this shared server had one 5 s window with a ~50 ms stall, which
GC traces ruled out as the cause.

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

Needs Go 1.26, Node 22 and Python 3.12:

```sh
python3 -m venv control/.venv && control/.venv/bin/pip install -e "control[dev]"
(cd panel && npm ci)
make test          # Go vet + race tests, ruff + pytest, panel typecheck + build
```

```sh
cd worker && go test ./...                                   # unit + integration tests
FSG_TEST_REDIS_ADDR=127.0.0.1:16379 go test ./internal/server  # + DLR queue tests on real Redis
cd panel  && npm run dev    # proxies /api to the control plane on :8080
```

The worker's integration tests drive it with an independent SMPP client
([gosmpp](https://github.com/linxGnu/gosmpp)) and raw PDUs.

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs the same checks and builds
both images on every push to `main` and on every pull request.
