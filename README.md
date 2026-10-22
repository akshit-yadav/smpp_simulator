# Power SMPP Gateway Simulator (PSG)

PSG simulates SMPP 3.4 vendor gateways so you can load-test and HA-test SMS platforms
(sized for a 25,000 TPS benchmark). An admin creates fake gateways in a web panel. Each
gateway is a real SMPP listener with its own port, credentials, DLR rule, DLR
behaviour, MO traffic and TPS limit.

## Status

| Milestone | State |
|---|---|
| M0 Scaffolding: compose, CI, health, worker registry | ✅ Done |
| M1 Worker core: SMPP 3.4 codec, bind auth, `submit_sm`, `enquire_link`, `unbind`, many listeners | ✅ Done |
| M2 Hot path: outcome engine, TPS limits, timing-wheel DLRs, DLR delivery/queue/retry, load generator | ✅ Done (short benchmark runs, see below) |
| M3 Control plane: admin login + TOTP, audit log, gateway/DLR rule/catalog/defaults API, 3-layer port check, config sync, worker registry | ✅ Done. Manage gateways through the REST API (Swagger UI at `/api/docs`) |
| M4 Events and reports: batched ClickHouse writer with disk spill, per-minute roll-ups, report and message-search API, the panel's Reports and Message search screens | ✅ Done |
| M5 Panel: login + TOTP, live dashboard, Gateway Manager (list/editor/detail, sessions with kill), DLR Rules with preview, error catalog, Gateway Defaults, workers, settings & audit | ✅ Done |
| M6 Intermediate DLRs (ENROUTE/ACCEPTD before the final DLR: off, when requested, always) | ✅ Done |
| M7 MO generator: rate or burst, schedule, sources/destinations, weighted templates, GSM7/UCS2, long messages, queue or drop, Send test MO, MO report | ✅ Done |
| M8 Licensing: signed licences (Ed25519), machine fingerprints, gateway limit with suspension, global TPS cap shared by workers, expiry and clock checks, Licence screen, `licgen` | ✅ Done |
| M9 Hardening and acceptance | ⏳ Next |
| Next phase (deferred from M6): `submit_multi`, `data_sm`, `query_sm`, `cancel_sm`, `replace_sm`; conditional rules; live overrides; response latency | 🕓 Deferred |

See [docs/build-prompt.md](docs/build-prompt.md) for the full scope.

## Layout

```
worker/         Go: one process per host, many SMPP listeners       -> image psg-worker
control/        Python 3.12 + FastAPI: REST API, scheduler, checks  -> image psg-control
  migrations/     MariaDB migrations (Alembic)
  clickhouse/     ClickHouse DDL
panel/          React + TypeScript + Vite SPA, served by psg-control
loadgen/        Go: open-loop windowed SMPP load generator
tools/licgen/   Go: licence signing CLI, HWPL internal only (M8)
deploy/         Env bootstrap, example gateways file, bundled MariaDB config
config/         Your gateways.json (git-ignored, created by `make env`)
docs/           Build brief, technical design, plan
```

## Run locally

Needs Docker with Compose v2 on Linux. `psg-control` and `psg-worker` use
`network_mode: host`, which Docker Desktop on macOS/Windows does not fully support.

```sh
make env           # creates .env with random secrets (admin password: PSG_ADMIN_PASSWORD)
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
| SMPP listeners | each gateway's port (in `PSG_PORT_RANGE`, default 20000–20999) |

## Admin panel

Open http://127.0.0.1:8080/ and sign in as `admin` with the `PSG_ADMIN_PASSWORD` from
`.env` (on a remote server, forward port 8080 with VS Code or `ssh -L 8080:127.0.0.1:8080`).

| Screen | What you can do |
|---|---|
| Dashboard | Live submit/throttle/reject and DLR rates (2 s refresh), sessions, listener state, worker DLR backlog |
| Gateway Manager | List, search, filter; create and edit (Connection / DLR Settings / Limits tabs). Connection picks the DLR Rule and checks the port (next free port suggested); the port of a running gateway is locked until you stop it. DLR Settings and Limits follow Gateway Defaults until you change a field; **Set default** reverts the tab |
| Gateway detail | Live chart, bound sessions with **Kill**, password reveal, effective settings, DLR rule entries, **MO traffic** (settings, Start/Stop, Send test MO), change history, enable/disable/delete |
| DLR Rules | Rules with Fixed / Random / Weighted modes, exact or probabilistic distribution, seed, and a 10,000-message preview. Fixed rules have exactly one entry |
| Error catalog | DLR `err:` codes (system + custom, strictly numeric), SMPP command statuses (+ vendor 0x400–0x4FF), DLR statuses |
| Gateway Defaults | The default DLR Rule, DLR Settings and Limits every gateway follows |
| Workers | Registry, rename, drain/activate, remove offline workers |
| Settings & audit | Change password, turn on TOTP (QR code), audit log of every change |
| Reports | Traffic (submit_sm and DLRs by result, per gateway), DLR rule mix actual vs configured, DLR delay distribution, throttling, MO traffic, bind history; 1 h to 30 days; CSV export |
| Message search | A message's submit_sm, DLR and MO events by message ID (hex or decimal), or recent traffic for a number |
| Licence | Status and limits, upload/replace/remove, worker fingerprints and match, **Request licence** export, which gateways stay active when over the limit. A banner on every page warns of expiry, a wrong machine or suspended gateways |

## Manage gateways (REST API)

Everything the panel does goes through the REST API. The interactive API docs at
http://127.0.0.1:8080/api/docs work in the browser: call `POST /api/v1/auth/login` there
first, and the session cookie authorises the rest. With curl:

```sh
API=http://127.0.0.1:8080/api/v1
curl -c jar -H 'Content-Type: application/json' \
     -d '{"username":"admin","password":"<PSG_ADMIN_PASSWORD>"}' $API/auth/login

curl -b jar $API/workers          # workers register themselves within ~5 s of starting

curl -b jar -H 'Content-Type: application/json' $API/gateways -d '{
  "name": "vendor-a", "port": 20001, "system_id": "vendora", "password": "secret1",
  "tps_limit": 500, "dlr_rule_id": 2
}'
```

The gateway gets the worker's IP automatically and is listening within a second. Any
setting you leave out (or later set to `null`) follows Gateway Defaults
(`GET/PUT /api/v1/defaults`).

| Endpoint | What |
|---|---|
| `POST /auth/login`, `/auth/logout`, `GET /auth/me` | Session cookie (HttpOnly, SameSite=Strict); 5 failures lock login for 5 min |
| `POST /auth/password`, `/auth/totp/setup`, `/auth/totp/enable`, `/auth/totp/disable` | Password change, optional TOTP (any authenticator app) |
| `GET/POST /gateways`, `GET/PATCH/DELETE /gateways/{id}` | Gateways. PATCH is partial: change `port`, `worker_id` (manual failover), `status` (`active`/`disabled`), credentials or any setting |
| `POST /gateways/{id}/reveal-password` | Shows the stored password (audited) |
| `GET /ports/check?worker_id=&port=`, `GET /workers/{id}/ports/next-free` | Port validation and the next free port |
| `GET/POST /dlr-rules`, `GET/PUT/DELETE /dlr-rules/{id}`, `POST /dlr-rules/preview` | DLR rules; preview simulates 10,000 messages |
| `GET/POST /catalog/command-statuses` (+ `PATCH/DELETE /{code}`) | SMPP 3.4 list (read-only) plus vendor codes 0x400–0x4FF |
| `GET/POST /catalog/dlr-errors` (+ `PATCH/DELETE /{code}`) | `err:` codes: system GSM MAP codes plus custom ones (numeric, e.g. `901`) with optional `network_error_code` and DLR text |
| `GET /catalog/dlr-statuses`, `GET/PUT /defaults` | DLR statuses; Gateway Defaults |
| `GET /workers`, `PATCH/DELETE /workers/{id}` | Worker registry; rename or set `draining` (no new gateways) |
| `GET /audit` | Audit log of every change, newest first |

A gateway uses the DLR rule in `dlr_rule_id`, or the Gateway Defaults' rule when it is
`null`. A rule has a `mode`, `distribution`, optional `seed` and `entries` (each with
`command_status`, `dlr_status`, `dlr_error`, `weight_bp`); a `fixed` rule has exactly one entry.
The port or worker of a gateway can only change while it is `disabled`.

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

> Listeners bind to all interfaces (`PSG_LISTEN_HOST`), so remote platforms can reach
> them. Firewall the port range if the server is public.

**With the bundled load generator:**

```sh
make bin
./bin/psg-loadgen -addr 127.0.0.1:20001 -system-id vendora -password secret1 \
    -binds 4 -tps 2000 -duration 30s
```

It prints throughput, `submit_sm_resp` latency percentiles, throttled/rejected counts,
DLR status counts and DLR delay, and exits non-zero on unanswered submits or missing DLRs.

**Watch it.** The dashboard and gateway pages update live. Worker metrics at http://127.0.0.1:9108/metrics: `psg_submit_total`,
`psg_dlr_total`, `psg_bind_total`, `psg_sessions`, `psg_dlr_pending`, `psg_dlr_queued`.
`GET /api/v1/gateways` shows each gateway's listener state (`runtime`) as the worker reports it.

### Running a worker without the control plane

Set `PSG_GATEWAYS_FILE=/etc/psg/gateways.json` and the worker reads gateways from
`config/gateways.json` instead (hot-reloaded within 1 s). `make env` writes a sample
file with three gateways ([deploy/gateways.example.json](deploy/gateways.example.json)).

### Gateway settings

The same settings exist in the API and in the static file. In the file, `defaults` apply
to every gateway and a gateway overrides only the fields it sets.

| Field | Meaning |
|---|---|
| `id`, `name`, `port` | Unique per worker. Port must be in `PSG_PORT_RANGE` and not in `PSG_PORT_EXCLUDE` |
| `system_id`, `password` | Max 15 and 8 characters |
| `status` | `active`, `disabled` or `suspended` |
| `max_binds`, `bind_types` | `0` = unlimited; any of `tx`, `rx`, `trx` |
| `tps.limit`, `tps.scope` | `0` = unlimited; `bind` (each bind gets the limit) or `gateway` (shared). Excess gets `ESME_RTHROTTLED` |
| `dlr.enabled`, `dlr.timing` | `immediate`, `fixed` (`delay_ms`) or `random` (`min_ms`–`max_ms`) |
| `dlr.id_format`, `dlr.date_seconds`, `dlr.include_text`, `dlr.include_tlvs` | DLR format options |
| `dlr.intermediate`, `dlr.intermediate_status`, `dlr.intermediate_delay_ms` | Intermediate DLR before the final one: `off` (default), `requested` (only with `registered_delivery` bit `0x10`) or `always` (any DLR requested); status `ENROUTE` or `ACCEPTD`; sent this many ms after `submit_sm_resp` |
| `outcome.mode` (the DLR rule) | `fixed` (first entry), `random` (uniform) or `weighted` (`weight_bp`, basis points totalling 10000) |
| `outcome.distribution` | `probabilistic` (default) or `exact` (every block of 10,000 matches the weights exactly) |
| `outcome.seed` | Optional; makes runs repeatable |
| outcome entries | `command_status` (non-zero = rejected, no DLR; SMPP 3.4 codes or vendor 0x400–0x4FF), `dlr_status`, `dlr_error` (3 digits), `network_error_code` `{type, code}`, `dlr_text` |

A DLR is sent only when DLR is enabled, the message was accepted, and
`registered_delivery` asks for it (bits `01` always, `10` failures only; intermediate
`ENROUTE`/`ACCEPTD` need bit `0x10`). An intermediate DLR (`dlr.intermediate`) carries
`dlvrd:000 err:000`, the same `id:` and `message_state` 1 or 6; the final DLR keeps its own
timing but never arrives before it. With no RX/TRX session bound, DLRs queue (persisted
to Redis, kept 72 h) and are delivered when a receiver binds. A `deliver_sm_resp` error or
a 30 s timeout is retried up to 5 times with backoff.

## Benchmarks

Worker on loopback, 8 vCPU shared with other services, DLRs on (random 500–2000 ms),
weighted outcomes, `psg-loadgen` on the same host:

| Offered | Duration | Sent | Errors | `submit_sm_resp` p50 / p99 / p99.9 | DLRs received | DLR delay p99 |
|---|---|---|---|---|---|---|
| 25,000 TPS | 30 s | 749,975 | 0 | 85 µs / 475 µs / 1.3 ms | 100% | 1,988 ms |
| 50,000 TPS | 30 s | 1,499,950 | 0 | 121 µs / 878 µs / 1.6 ms | 100% | 1,988 ms |

With events on (M4), same host, one ClickHouse on the same 8 vCPU:

| 25,000 TPS, 30 s | `submit_sm_resp` p50 / p99 / p99.9 | Worker CPU | Worker peak RSS | Events written |
|---|---|---|---|---|
| Events off | 97 µs / 525 µs / 1.6 ms | 12 s | 44 MB | – |
| Events on, ClickHouse up | 101 µs / 779 µs / 2.7 ms | 14 s | 60 MB | 1.5 M rows, 0 dropped |
| Events on, ClickHouse down (spilling) | 101 µs / 628 µs / 4.6 ms | 14 s | 60 MB | 1.5 M rows to disk (124 MB), replayed in ~4 s |

The worker used under one CPU core at 50k TPS, and GC pauses stayed under 1 ms. The brief's
full gate (25k TPS for 60 min, 50k TPS for 10 min) still needs a dedicated host: an
earlier 50k run on this shared server had one 5 s window with a ~50 ms stall, which
GC traces ruled out as the cause.

## MO traffic

Each gateway can send mobile-originated messages to its own RX and TRX sessions
(`deliver_sm`, `esm_class` 0x00, or 0x40 with a UDH for long messages). Set it up on the
gateway page under **MO traffic**:

| Setting | Meaning |
|---|---|
| Mode | **Steady rate** (messages per second, within ±2%) or **Burst** (N messages as fast as the receivers take them) |
| Start at, Stop after | Optional start time; stop after a duration and/or a number of messages. **Stop** ends a run any time; every **Start** begins a new run from zero. A worker restart resumes the run where it was |
| Source numbers | A list, a numeric range, or a prefix plus random digits |
| Destinations | Short codes or long numbers, used in turn or at random |
| Texts | Weighted templates with `{seq}`, `{rand:N}`, `{keyword}` and `{time}` (UTC) |
| Encoding, long messages | GSM7 (unpacked, `data_coding` 0) or UCS2 (`data_coding` 8); a share sent as 2–3 concatenated parts. Each part counts as one message |
| No receiver bound | **Queue** (kept like DLRs, up to 72 h, persisted to Redis) or **Drop** |

**Send test MO** sends one message right away and fails if no receiver is bound. MO uses
the same 1,000-PDU window, retries and queue as DLRs, does not count against the gateway's
`submit_sm` TPS limit, and is recorded in `mo_events` (Reports → MO traffic, Message
search). API: `GET/PUT /api/v1/gateways/{id}/mo`, `POST …/mo/start`, `…/mo/stop`,
`…/mo/test`.

## Licensing

Without a valid licence the **free tier** applies: **10 gateways** and **1,000 TPS** in total
(`submit_sm` plus MO, across all workers). These limits are constants in
`worker/pkg/licence/licence.go` and `control/psg_control/licence.py`.

A licence is a signed file (`.psglic`) with an ID, licensee, expiry, gateway and TPS
limits, and the machines it covers. Each machine is identified by the SHA-256 of
`/etc/machine-id` and of the MAC of the interface carrying the default route. A match on
either is accepted, with a warning for a partial match. Set `PSG_LICENCE_REQUIRE_BOTH=true`
to require both.

- **Install:** Licence page → **Upload licence**, or `PUT /api/v1/licence {"licence": "<file text>"}`.
  If no licence is installed, the control plane also installs `config/licence.psglic` on
  start. A worker run from a static gateways file reads that same file (`PSG_LICENCE_FILE`).
- **Get one:** Licence page → **Request licence** downloads every worker's fingerprint for HWPL.
- **Gateways:** creating one more than the limit is refused. If the limit drops (expiry, a
  smaller licence, removal), gateways beyond it become *suspended (licence)*: their listener
  stops and sessions get an `unbind`. The oldest stay unless you pick others on the Licence
  page. A new licence restores them within 5 s.
- **TPS:** each worker has a token bucket for its share of the cap. The control plane
  re-balances shares every 5 s by demand, with an even floor. Over the cap, `submit_sm` gets
  `ESME_RTHROTTLED` and counts as **licence-throttled** (Reports → Throttling). MO slows down
  instead.
- **Expiry:** banner warnings from 30 days before. At expiry, or if the clock goes back more
  than 24 h, the free tier applies.
- **Backstop:** workers verify the licence themselves (signature, expiry, own machine) and
  never run more gateways or a bigger TPS share than they verified.

HWPL issues licences with `tools/licgen` (`keygen`, `sign`, `verify`, `fingerprint`; run it
without arguments for usage). The private key must never be committed. Only the public key
is in the code.

## Events and reports

Every worker records one ClickHouse row per `submit_sm` (any result), per DLR or MO outcome
(acknowledged, retried, failed, queued, expired; MO also dropped) and per bind, failed bind,
close or kill.
Rows are batched (every second or 10,000 rows) and sent as RowBinary over HTTP. The hot
path only appends to an in-memory buffer; if the writer falls behind by more than 500,000
rows per table, rows are dropped and counted in `psg_events_total{result="dropped"}`, never
blocking SMPP traffic.

| What | Kept | Used by |
|---|---|---|
| Raw events (`submit_events`, `dlr_events`, `mo_events`, `session_events`) | 3 days | Bind history, Message search |
| Per-minute roll-ups (`submit_minute`, `dlr_minute`, `mo_minute`) | 180 days | Traffic, DLR rule mix, DLR delay, Throttling, MO traffic |

The control plane applies the DDL in [control/clickhouse/](control/clickhouse/) on startup
(retrying until ClickHouse is up). If ClickHouse is down, the worker writes batches to
`PSG_SPILL_DIR` (fsync'd, capped at `PSG_SPILL_MAX_BYTES`, 5 GB by default) and replays them
in order once it is back. 25,000 TPS spills about 4 MB/s, so 5 GB covers about 20 minutes.
Each batch carries an `insert_deduplication_token`, so a batch resent after a lost reply is
not counted twice, in the raw tables or the roll-ups.

Set `PSG_EVENTS_ENABLED=false` to turn events off, for example while load-testing another
platform on a host short of memory. Reports then show nothing new.

Report API (admin session; every endpoint takes `from`, `to`, `format=json|csv`):
`GET /api/v1/reports/traffic`, `/reports/outcomes?gateway_id=`, `/reports/dlr-delay`,
`/reports/mo`, `/reports/binds`, `/messages?message_id=` or `?destination=`.

## Using the MariaDB, Redis and ClickHouse already on the host

Point the `PSG_MARIADB_*`, `PSG_REDIS_*` and `PSG_CLICKHOUSE_*` variables in `.env` at the
existing servers, create the `psg` database and users there, and start without the profile:

```sh
make up            # = docker compose up -d --build
```

The bundled stores publish only on `127.0.0.1` and on non-default ports (13306, 16379,
18123/19000), so they never clash with stores already on the host.

## Configuration

All settings are `PSG_*` environment variables. [.env.example](.env.example) documents
them all. `.env` holds secrets and is git-ignored. Never commit it.

## Development

Needs Go 1.26, Node 22 and Python 3.12:

```sh
python3 -m venv control/.venv && control/.venv/bin/pip install -e "control[dev]"
(cd panel && npm ci)
make test          # Go vet + race tests, ruff + pytest, panel typecheck + build
```

`make test` starts throwaway MariaDB (127.0.0.1:13399), Redis (127.0.0.1:16399) and
ClickHouse (HTTP 127.0.0.1:18199) containers: the control-plane API and report tests and
the worker's Redis and ClickHouse tests run against them.

```sh
cd panel && npm run dev    # proxies /api to the control plane on :8080
cd control && .venv/bin/alembic revision --autogenerate -m "..."   # after changing models.py
```

The worker's integration tests drive it with an independent SMPP client
([gosmpp](https://github.com/linxGnu/gosmpp)) and raw PDUs.

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) runs the same checks and builds
both images on every push to `main` and on every pull request.
