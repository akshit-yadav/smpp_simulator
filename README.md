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
| M3 Control plane: admin login + TOTP, audit log, gateway/profile/catalog/defaults API, 3-layer port check, config sync, worker registry | ✅ Done. Manage gateways through the REST API (Swagger UI at `/api/docs`) |
| M4 Events and reports (ClickHouse); the panel's Reports and Message search screens | ⏳ Next |
| M5 Panel: login + TOTP, live dashboard, gateways list/editor/detail (sessions with kill), outcome profiles with preview, error catalog, defaults, workers, settings & audit | ✅ Done |
| M6–M9 `submit_multi`/`data_sm`/`query_sm`…, overrides, MO, licensing, hardening | ⏳ Not started |

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
make env           # creates .env with random secrets (admin password: FSG_ADMIN_PASSWORD)
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
| SMPP listeners | each gateway's port (in `FSG_PORT_RANGE`, default 20000–20999) |

## Admin panel

Open http://127.0.0.1:8080/ and sign in as `admin` with the `FSG_ADMIN_PASSWORD` from
`.env` (on a remote server, forward port 8080 with VS Code or `ssh -L 8080:127.0.0.1:8080`).

| Screen | What you can do |
|---|---|
| Dashboard | Live submit/throttle/reject and DLR rates (2 s refresh), sessions, listener state, worker DLR backlog |
| Gateways | List, search, filter; create and edit (Connection / Outcomes / DLR / Limits tabs) with port check and next-free-port; every setting can follow the default or be custom |
| Gateway detail | Live chart, bound sessions with **Kill**, password reveal, effective settings, outcome mix, change history, enable/disable/delete |
| Outcome profiles | Shared profiles with fixed/random/weighted modes, exact or probabilistic distribution, seed, and a 10,000-message preview |
| Error catalog | DLR `err:` codes (system + custom), SMPP command statuses (+ vendor 0x400–0x4FF), DLR statuses |
| Defaults | Global defaults every gateway can follow |
| Workers | Registry, rename, drain/activate, remove offline workers |
| Settings & audit | Change password, turn on TOTP (QR code), audit log of every change |

Reports, Message search, MO, Live overrides and Licence appear in the menu with the
milestone that delivers them.

## Manage gateways (REST API)

Everything the panel does goes through the REST API. The interactive API docs at
http://127.0.0.1:8080/api/docs work in the browser: call `POST /api/v1/auth/login` there
first, and the session cookie authorises the rest. With curl:

```sh
API=http://127.0.0.1:8080/api/v1
curl -c jar -H 'Content-Type: application/json' \
     -d '{"username":"admin","password":"<FSG_ADMIN_PASSWORD>"}' $API/auth/login

curl -b jar $API/workers          # workers register themselves within ~5 s of starting

curl -b jar -H 'Content-Type: application/json' $API/gateways -d '{
  "name": "vendor-a", "port": 20001, "system_id": "vendora", "password": "secret1",
  "tps_limit": 500, "outcome_profile_id": 2
}'
```

The gateway gets the worker's IP automatically and is listening within a second. Any
setting you leave out (or later set to `null`) follows the global defaults
(`GET/PUT /api/v1/defaults`).

| Endpoint | What |
|---|---|
| `POST /auth/login`, `/auth/logout`, `GET /auth/me` | Session cookie (HttpOnly, SameSite=Strict); 5 failures lock login for 5 min |
| `POST /auth/password`, `/auth/totp/setup`, `/auth/totp/enable`, `/auth/totp/disable` | Password change, optional TOTP (any authenticator app) |
| `GET/POST /gateways`, `GET/PATCH/DELETE /gateways/{id}` | Gateways. PATCH is partial: change `port`, `worker_id` (manual failover), `status` (`active`/`disabled`), credentials or any setting |
| `POST /gateways/{id}/reveal-password` | Shows the stored password (audited) |
| `GET /ports/check?worker_id=&port=`, `GET /workers/{id}/ports/next-free` | Port validation and the next free port |
| `GET/POST /profiles`, `GET/PUT/DELETE /profiles/{id}`, `POST /profiles/preview` | Shared outcome profiles; preview simulates 10,000 messages |
| `GET/POST /catalog/command-statuses` (+ `PATCH/DELETE /{code}`) | SMPP 3.4 list (read-only) plus vendor codes 0x400–0x4FF |
| `GET/POST /catalog/dlr-errors` (+ `PATCH/DELETE /{code}`) | `err:` codes: system GSM MAP codes plus custom ones (e.g. `T01`) with optional `network_error_code` and DLR text |
| `GET /catalog/dlr-statuses`, `GET/PUT /defaults` | DLR statuses; global defaults |
| `GET /workers`, `PATCH/DELETE /workers/{id}` | Worker registry; rename or set `draining` (no new gateways) |
| `GET /audit` | Audit log of every change, newest first |

A gateway can use a shared profile (`outcome_profile_id`) or its own: send
`custom_outcome` with `mode`, `distribution`, optional `seed` and `entries` (each with
`command_status`, `dlr_status`, `dlr_error`, `weight_bp`, optional `dlr_delay_min_ms`/`max_ms`).

**Ports** are checked three times: in the worker's range and not excluded; not taken
(Redis lock plus a database unique constraint); and actually free on the worker's host
(the worker test-binds it). A conflict answers `409` with *"Port 20001 is already in use.
Please choose another port."* and a `suggested_port`.

**Config sync.** Every change is written to MariaDB and announced on Redis; the worker
pulls its config within a second and caches it on disk. Workers keep serving their last
config if the control plane, MariaDB or Redis is down, and re-check every 60 s.

## Test the simulator

Create a gateway as above, then:

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
./bin/fsg-loadgen -addr 127.0.0.1:20001 -system-id vendora -password secret1 \
    -binds 4 -tps 2000 -duration 30s
```

It prints throughput, `submit_sm_resp` latency percentiles, throttled/rejected counts,
DLR status counts and DLR delay, and exits non-zero on unanswered submits or missing DLRs.

**Watch it.** The dashboard and gateway pages update live. Worker metrics at http://127.0.0.1:9108/metrics: `fsg_submit_total`,
`fsg_dlr_total`, `fsg_bind_total`, `fsg_sessions`, `fsg_dlr_pending`, `fsg_dlr_queued`.
`GET /api/v1/gateways` shows each gateway's listener state (`runtime`) as the worker reports it.

### Running a worker without the control plane

Set `FSG_GATEWAYS_FILE=/etc/fsg/gateways.json` and the worker reads gateways from
`config/gateways.json` instead (hot-reloaded within 1 s). `make env` writes a sample
file with three gateways ([deploy/gateways.example.json](deploy/gateways.example.json)).

### Gateway settings

The same settings exist in the API and in the static file. In the file, `defaults` apply
to every gateway and a gateway overrides only the fields it sets.

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

## Using the MariaDB, Redis and ClickHouse already on the host

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

`make test` starts throwaway MariaDB (127.0.0.1:13399) and Redis (127.0.0.1:16399)
containers: the control-plane API tests and the worker's Redis tests run against them.

```sh
cd panel && npm run dev    # proxies /api to the control plane on :8080
cd control && .venv/bin/alembic revision --autogenerate -m "..."   # after changing models.py
```

The worker's integration tests drive it with an independent SMPP client
([gosmpp](https://github.com/linxGnu/gosmpp)) and raw PDUs.

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs the same checks and builds
both images on every push to `main` and on every pull request.
