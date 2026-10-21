# Build prompt: Fake SMPP Gateway (FSG)

You are a senior engineer building **Fake SMPP Gateway (FSG)**. FSG is a standalone app that simulates SMPP vendor gateways for load testing and high-availability testing of SMS platforms, sized for a **25,000 TPS** benchmark. An admin uses a web panel to create fake gateways. Each gateway is a real SMPP 3.4 listener with its own port, credentials, outcome rules, DLR behaviour, MO traffic and TPS limit.

The attached **"Fake SMPP Gateway — Technical Design"** document is the source of truth. The summary below is enough to start. Where the two differ, the design document wins.

---

## How to work

1. **Plan first. Write no code until I approve the plan.** Your first reply must contain:
   - the repo layout (a monorepo with `worker/`, `control/`, `panel/`, `deploy/`, `loadgen/`, `docs/`)
   - a module breakdown for each service, and the interfaces between them: the internal API, Redis messages, and the event schema
   - the milestone plan below, refined with tasks, an estimate and a definition of done for each milestone
   - the libraries you'll use, with one line on why each (prefer well-maintained libraries; hand-rolling the SMPP server PDU layer is fine)
   - the top risks and how you'll catch them early
   - any questions where the design is ambiguous. Don't guess silently.
2. **Build one milestone at a time.** At the end of each one:
   - show what was built and how to run it
   - show test results
   - list anything you changed from the design, and why
   - stop for my go-ahead
3. **Tests come with the code, not after it.**
   - The Go worker needs unit tests for the PDU codec, outcome engine, token bucket and timing wheel.
   - It also needs integration tests that use a real SMPP client against a running worker.
   - The control plane needs API tests against MariaDB, Redis and ClickHouse running in Docker.
4. **Benchmark early.** The load generator and a 25k TPS run come in milestone 2, not at the end. If the worker can't sustain 50k TPS with DLRs on (2x headroom) on 8 vCPU, stop and tell me before continuing.
5. **Keep it simple and readable.**
   - No speculative abstractions.
   - Configuration through environment variables or files.
   - Structured JSON logs.
   - Prometheus metrics on the worker.
   - Never log passwords.
6. **Don't:**
   - create one container or pod per gateway
   - put Redis or MariaDB on the per-message hot path (the only exception is persisting queued DLRs)
   - insert into ClickHouse row by row
   - build anything listed as out of scope

---

## Fixed decisions

| Area | Decision |
|---|---|
| Architecture | Control plane + **Go workers**. One worker process per host runs **many** SMPP listeners. Scale by adding workers. |
| Control plane | **Python 3 + FastAPI**: REST API, scheduler, licence checks, port checks, reports |
| Panel | SPA served by the control plane. Propose a stack (default: React + TypeScript + Vite). Every panel action goes through the REST API. |
| Config store | **MariaDB** (existing on the host), InnoDB, utf8mb4 |
| Control and live state | **Redis** (existing on the host): config change pub/sub, worker heartbeats, port locks, live counters, persisted DLR queues |
| Events and reports | **ClickHouse** (existing on the host): batched inserts every 1 s or 10k rows, MergeTree with TTL, materialized-view roll-ups |
| Packaging | Docker images `fsg-control` and `fsg-worker`, run with **docker-compose** and `network_mode: host`. An optional `bundled` profile runs its own MariaDB, Redis and ClickHouse for a lab setup. |
| Ports | Listener range **20000–20999** per worker IP, configurable, with an exclusion list |
| Users | **Single admin**: password plus optional TOTP. Full audit log. |
| Failover | Manual move of gateways between workers. No floating IPs. |
| Out of scope | SMPP over TLS, multi-tenancy, automatic host failover, and any integration with Power SMPP or MT Rules |

---

## Functional requirements

### Gateway (create, edit, disable, delete; changes live within 1 s)
- **Name:** required, unique.
- **IP:** filled in automatically with the assigned worker's IP; read-only.
- **Port:** required. Validate in three layers:
  1. inside the range and not excluded
  2. Redis `SET NX` lock plus a MariaDB `UNIQUE(worker_id, port)` constraint
  3. the worker test-binds the port on its host

  On failure, show *"Port XXXX is already in use. Please choose another port."* The panel suggests the next free port.
- **System ID:** at most 15 characters. **Password:** at most 8 characters, stored AES-256-GCM encrypted (not hashed, because the plain value is needed to check binds) and masked in the UI.
- **Other settings:** outcome profile, DLR on/off, DLR timing, TPS limit (per bind by default, or per gateway), max binds, allowed bind types, response latency, DLR format, status (active, disabled, or suspended by licence).
- **Defaults:** every setting either follows a **global default** or uses a **custom value** per gateway. A NULL column means "follow the default".

### Outcome engine
Each accepted `submit_sm` gets one outcome: **command status + DLR status + DLR error**. A non-zero command status means the message is rejected, gets no message ID and gets no DLR.

**Catalogs:**
- **Command status:** the full SMPP 3.4 list, read-only. The admin can add **vendor codes only in 0x400–0x4FF**.
- **DLR status:** DELIVRD, EXPIRED, DELETED, UNDELIV, ACCEPTD, UNKNOWN, REJECTD, ENROUTE, each mapped to its `message_state` value.
- **DLR error (`err:`):** a seeded system list (`000` plus common GSM MAP errors) and **admin-defined custom codes** (e.g. `T01` Template Mismatch, `G01` Gateway Closed). A custom code has: code (up to 3 characters), label, suggested status, optional `network_error_code` TLV and optional DLR text.

**Outcome profiles** are named and reusable. A gateway uses either a shared profile or its own custom one.
- **Modes:** Fixed, Random (uniform), or Weighted (percentages totalling 100%, stored as basis points).
- **Distribution:** probabilistic (an alias table, so each pick is O(1)) or exact (a shuffled deck of 10,000). An optional seed makes runs repeatable.
- **Conditional rules** (at most 20 per profile, checked in priority order): match on destination prefix (trie), source or sender ID, text contains / regex / does-not-match-template, `service_type`, `data_coding`, `esm_class`, or a time window. A match sends the message to another outcome or profile. Compile the rules when the profile is saved.
- **Live overrides per gateway**, with an optional auto-revert time: force an outcome, reject binds, drop sessions, black hole (accept PDUs, never answer), gateway closed (stop the listener).

### DLRs
- Send a DLR only if DLR is on **and** the message was accepted **and** `registered_delivery` asks for one. Bits 01 mean always; bits 10 mean on failure only. Intermediate DLRs (`ENROUTE`, `ACCEPTD`) only when bit 4 is set or the admin forces them.
- **Timing per gateway:** immediate, fixed delay, or random min–max (default 500–2000 ms). An outcome in a weighted mix can override the delay. Timing must be accurate to ±20 ms. Pending DLRs live in a hierarchical timing wheel.
- **Format:** `id:… sub:001 dlvrd:… submit date:… done date:… stat:… err:… text:…`, with `esm_class` 0x04 and the TLVs `receipted_message_id`, `message_state` and `network_error_code`. Configurable: message ID as hex or decimal, date with or without seconds, whether to include `text:` and the TLVs.
- **Delivery:** round-robin across RX and TRX sessions with the same System ID. If no receiver is bound, queue the DLR (up to 72 h) and persist it to a Redis stream. Retry up to 5 times with backoff on a `deliver_sm_resp` error or a 30 s timeout.

### SMPP server (3.4)
- **PDUs:** bind TX/RX/TRX (enforce credentials, bind types and max binds), `submit_sm`, `submit_multi`, `data_sm`, `query_sm`, `cancel_sm`, `replace_sm`, `enquire_link`, `unbind`, `generic_nack`.
- **Windowing:** up to 1,000 PDUs in flight per session.
- **Concatenated messages** (UDH or SAR): each part counts as one message. An option gives all parts the same outcome.
- **TPS limit:** a token bucket per bind (or per gateway); messages over the limit get `ESME_RTHROTTLED` (0x58).
- **Message IDs:** 64-bit (worker ID 8 bits + time 40 bits + sequence 16 bits), written in hex.

### MO traffic (per-gateway generator, off by default)
- Sends `deliver_sm` (`esm_class` 0x00) to the RX and TRX sessions of that System ID.
- **Settings:** a steady rate or a burst of N; a schedule (start time, and stop after a duration or a count); source MSISDN as a list, range or random under a prefix; destination as a list (round-robin or random); weighted text templates with `{seq}`, `{rand:N}`, `{keyword}` and `{time}`; GSM7 or UCS2; a share of long messages; when no receiver is bound, queue or drop; retries on response errors.
- A **Send test MO** button in the panel, with the same action in the API.
- MO counts against the **licence TPS cap**, but not against the gateway's `submit_sm` TPS limit.

### Licensing
- **The licence** is a signed JSON file (Ed25519). Only the public key is built into the binaries. A separate internal CLI (`tools/licgen/`) signs licences with the private key (HWPL only).
- **Fields:** `licence_id`, `licensee`, `issued_at`, `expires_at`, `max_gateways`, `max_tps`, `machines[]` (fingerprints), `features[]`, `signature`.
- **Free tier** with no licence: **10 gateways**, **1,000 TPS** (to be confirmed, so keep it configurable).
- **Machine fingerprint:** SHA-256 of `/etc/machine-id` plus the primary NIC's MAC (the interface carrying the default route). Accept a match on either one, with a panel warning on a partial match. Make it configurable to require both.
- **Enforcement:**
  - The control plane blocks a Create once the gateway limit is reached.
  - Workers re-check the licence themselves, as a backstop.
  - The TPS cap is a global token bucket per worker. The control plane rebalances worker shares every 5 s based on demand. Messages over the cap get `ESME_RTHROTTLED` and are counted as **licence-throttled**, separately from gateway throttling.
  - Clock rollback of more than 24 h counts as expired.
- **Expiry:**
  - warnings at 30, 7 and 1 days
  - at expiry, the limits fall to the free tier: gateways beyond 10 become *suspended (licence)*, their listeners stop and their binds get an `unbind`
  - the oldest 10 stay active unless the admin picks others
  - configuration is kept, and a new licence restores the suspended gateways at once

### Reports (ClickHouse)
- **Tables:**
  - `submit_events` (including `throttled` and `licence_throttled`), `dlr_events`, `mo_events`, `session_events`
  - partitioned by day, ordered by `(gateway_id, ts)`, raw events kept 3 days by TTL
  - per-minute roll-ups kept 180 days
- **Panel reports:**
  - traffic by gateway
  - outcome mix, actual vs configured
  - DLR delay distribution
  - throttled messages (gateway vs licence)
  - MO traffic
  - bind history
  - message search by message ID or destination
  - CSV export
- **Outages:** if ClickHouse is down, events spill to local disk (about 5 GB) and are replayed in order later.

### Panel screens
Dashboard (live), Gateways list, Gateway editor (Connection / Outcomes / DLR / Limits / MO), Gateway detail (live charts, sessions with kill, overrides, audit), Outcome profiles (with a 10,000-message preview), Error catalog, Defaults, Reports, Message search, Workers (with drain), Licence (upload, fingerprints, "Request licence" export, choose which gateways stay active), Settings & Audit.

### Config sync
1. The control plane writes to MariaDB, then publishes `{gateway_id, version, action}` on `fsg:cfg:<worker_id>`.
2. The worker pulls the change from the control plane's internal API.
3. As a safety net, workers re-sync fully on startup or reconnect, and every 60 s compare their config version.
4. Workers keep serving traffic with their cached config if the control plane, MariaDB, Redis or ClickHouse is down.

---

## Milestones (refine these in your plan)

1. **M0 – Scaffolding:** monorepo, docker-compose with a `bundled` profile, CI (lint and tests), MariaDB migrations, ClickHouse DDL.
2. **M1 – Worker core:**
   - SMPP 3.4 PDU codec
   - bind authentication, `submit_sm` → `submit_sm_resp`, `enquire_link`, `unbind`
   - multiple listeners from a static config file
3. **M2 – Hot path and benchmark:**
   - outcome engine (fixed, random, weighted, exact), token bucket, timing-wheel DLRs, DLR delivery and queueing
   - `loadgen/`: a Go open-loop, windowed SMPP client that records latency
   - **benchmark gate:** 25k TPS for 60 minutes and 50k TPS for 10 minutes, DLRs on, p99 `submit_sm_resp` under 5 ms
4. **M3 – Control plane:**
   - FastAPI, MariaDB models, admin auth with TOTP, audit log
   - gateway, profile and catalog CRUD with validation
   - three-layer port check, Redis sync, worker registry and heartbeats, scheduler
5. **M4 – Events and reports:** batched ClickHouse writer with a disk spill buffer, materialized views, report endpoints.
6. **M5 – Panel:** every screen listed above, plus live dashboard updates (Redis counters through the API).
7. **M6 – Advanced:** conditional rules, live overrides, response latency, `submit_multi`, `data_sm`, `query_sm`, `cancel_sm`, `replace_sm`, intermediate DLRs.
8. **M7 – MO generator:** worker generator, API, panel, `mo_events`.
9. **M8 – Licensing:** licence CLI, fingerprinting, gateway and TPS enforcement with worker rebalancing, expiry handling, Licence screen.
10. **M9 – Hardening and acceptance:**
    - resilience tests (stop MariaDB, Redis and ClickHouse in turn; restart a worker)
    - `host-tune.sh`
    - the full acceptance checklist from the design document
    - a README with install, upgrade and runbook

---

## Acceptance (summary; the full list is in the design document)

- Create, edit and delete work from the panel, and a listener is up or down within 1 s. Port conflicts are blocked with a clear message. The IP is filled in automatically.
- Binds succeed only with the gateway's credentials. Allowed bind types and max binds are enforced.
- Fixed profiles return exact values. Weighted probabilistic mode is within ±0.5 percentage points over 1M messages. Exact mode is exact in every block of 10,000.
- DLRs are sent only when all three conditions hold, with timing within ±20 ms. TPS overflow gets `ESME_RTHROTTLED`.
- Vendor command status codes work only in 0x400–0x4FF. Custom DLR errors appear in `err:` and the TLV.
- MO rate is within ±2%. Send test MO works.
- Licence: an 11th gateway is blocked without a licence. The TPS cap holds across workers. A licence on the wrong machine is rejected. At expiry, gateways are suspended and TPS is capped.
- 25k TPS for 60 minutes with zero unexpected errors. Traffic keeps flowing while each store is down. No events are lost after a 10-minute ClickHouse outage.
- `docker compose up` on a clean host with existing MariaDB, Redis and ClickHouse works first time.

**Start with the plan (step 1 under "How to work"). Write no code yet.**
