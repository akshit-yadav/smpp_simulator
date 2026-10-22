# Acceptance checklist

Each acceptance item from the [build brief](build-prompt.md), how it is verified and where
it stands. "Test" names point to automated tests that CI runs on every push. Scripts are
in [`deploy/`](../deploy). Last updated with M9 (2026-10-22).

**Status:** ✅ verified · 🟡 verified at reduced scale or duration; the full run needs a dedicated host · 🕓 deferred by decision

| # | Acceptance item | How it is verified | Status |
|---|---|---|---|
| 1 | Create, edit and delete from the panel; the listener is up or down within 1 s | `test_create_gateway_end_to_end`, `test_move_disable_delete` (control); `TestLiveConfigChanges` (worker); config is pushed over Redis pub/sub and pulled at once | ✅ |
| 2 | Port conflicts blocked with a clear message; IP filled in automatically | `test_port_validation_layers` (range/exclusion, Redis lock + unique key, worker test-bind, next free port); `TestPortConflict` | ✅ |
| 3 | Binds only with the gateway's credentials; bind types and max binds enforced | `TestBindAuthentication` | ✅ |
| 4 | Fixed returns exact values; weighted probabilistic within ±0.5 pp over 1M; exact mode exact in every block of 10,000 | `TestFixed`, `TestWeightedProbabilisticWithinHalfPoint`, `TestExactEveryBlock`, `TestSeedRepeatable` | ✅ |
| 5 | DLRs only when all three conditions hold; timing within ±20 ms; TPS overflow gets `ESME_RTHROTTLED` | `TestRegisteredDeliveryRules`, `TestSubmitAndDLRTiming` (300 ms ±20 ms), `TestTPSLimitThrottles`; timing wheel `TestFiresOnTimeAcrossAllLevels` | ✅ |
| 5a | Intermediate DLRs (M6): `ENROUTE`/`ACCEPTD` before the final DLR when requested or forced | `TestIntermediateDLRs`, `test_intermediate_dlr_settings` | ✅ |
| 6 | Vendor command statuses only in 0x400–0x4FF; custom DLR errors in `err:` and the TLV | `test_command_status_catalog`, `test_dlr_error_catalog`, `TestRegisteredDeliveryRules` (custom `err:` + `network_error_code`) | ✅ |
| 7 | MO rate within ±2%; Send test MO works | `TestMOGenerator` (500/s for 2 s within 2%, bursts, long UCS2 parts, queue/drop, test MO), `TestMORunResumesAfterRestart`, `test_send_test_mo` | ✅ |
| 8a | Licence: an 11th gateway is blocked without a licence | `test_free_tier_blocks_the_11th_gateway`; worker backstop `TestLicenceLimits` | ✅ |
| 8b | The TPS cap holds across workers | Worker bucket at its share, never above the cap it verified (`TestEnforcer`, `TestBucketCapsRate`, `TestLicenceLimits`); 2,000 TPS offered on the free tier gave 1,000/s accepted; share split by demand (`test_tps_shares`). Two workers on separate hosts not yet run | 🟡 |
| 8c | A licence for another machine is rejected | `test_bad_licences_are_refused`, `TestCheck`, `TestEnforcer` | ✅ |
| 8d | At expiry gateways are suspended and TPS is capped; a new licence restores them | `test_upload_suspend_keep_restore`, `test_expiry_warning_clock_and_partial_match` (incl. clock rollback > 24 h) | ✅ |
| 9 | 25k TPS for 60 minutes with zero unexpected errors | 30 s runs at 25k (and 50k) TPS with zero errors, p99 `submit_sm_resp` 0.5–0.8 ms (README → Benchmarks). The 60-minute run needs a dedicated 8 vCPU host; see the runbook | 🟡 |
| 10a | Traffic keeps flowing while each store is down | `deploy/resilience-test.sh`: MariaDB, Redis and ClickHouse stopped in turn under load; 229,999 messages, zero errors, every DLR delivered (2026-10-22, 1,000 TPS) | ✅ |
| 10b | No events are lost after a 10-minute ClickHouse outage | Same script: exact submit/DLR event counts after a 60 s outage (129,993 rows spilled and replayed); spill and replay order and dedup in `TestSpillAndReplayInOrder`, `TestClickHouseRoundTrip`. Full run: `deploy/resilience-test.sh --tps 25000 --ch-outage 600` on a dedicated host (needs ~2.5 GB of spill) | 🟡 |
| 10c | Workers keep serving with cached config while the control plane is down | `deploy/resilience-test.sh` phase 2 (worker restarted with the control plane stopped); `internal/control` tests | ✅ |
| 11 | `docker compose up` on a clean host works first time | The resilience test brings up a clean bundled stack from scratch on every run. Existing (non-bundled) stores: documented in the README, not yet run on a second host | 🟡 |
| — | `submit_multi`, `data_sm`, `query_sm`, `cancel_sm`, `replace_sm`; conditional rules; live overrides; response latency | Moved to a later phase on 2026-10-22 | 🕓 |
| — | Concatenated `submit_sm` (UDH/SAR) counted per part, with an option to give all parts the same outcome | Each part is already one `submit_sm` and one message; the "same outcome for all parts" option belongs to the deferred rules work | 🕓 |

## Before calling the brief's benchmark gate done

On a dedicated 8 vCPU host (not shared with the platform under test):

1. `sudo deploy/host-tune.sh --apply`
2. Bring PSG up with a licence for that host (`max_tps` ≥ 50,000).
3. `make bench GW=127.0.0.1:<port> SYS=<system_id> PW=<password> TPS=25000 DUR=60m`, then `TPS=50000 DUR=10m`, with DLRs on. Pass: zero unexpected errors, p99 `submit_sm_resp` under 5 ms, DLRs 100%.
4. `deploy/resilience-test.sh --tps 25000 --ch-outage 600`
