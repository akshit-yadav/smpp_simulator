#!/usr/bin/env bash
# Resilience test (M9). Starts an isolated PSG stack with its own bundled
# MariaDB, Redis and ClickHouse next to any running one, then checks that:
#
#   1. SMPP traffic flows with zero errors and every DLR delivered while
#      MariaDB, Redis and ClickHouse are each stopped in turn,
#   2. no events are lost: ClickHouse ends up with exactly one submit_events
#      and one acknowledged dlr_events row per message (spill and replay),
#   3. a worker restarted while the control plane is down serves its cached
#      gateways.
#
#   deploy/resilience-test.sh [--tps N] [--ch-outage SECONDS] [--keep] [--force]
#
# The acceptance run is --tps 25000 --ch-outage 600 (25k TPS, a 10-minute
# ClickHouse outage), on a dedicated host.
#
# Needs Docker Compose v2, curl, python3, bin/psg-loadgen (make bin) and a
# .env (make env). Uses compose project "psgr", control HTTP 18090, worker
# metrics 19118, SMPP ports 21000-21099 and store ports 13307, 16380, 18124
# and 19001. Every container is memory-limited, and the test refuses to start
# with less than 3 GB of memory available (--force overrides) so it cannot
# starve a shared host. --keep leaves the stack running afterwards.
set -euo pipefail
cd "$(dirname "$0")/.."

TPS=1000
CH_OUTAGE=60
KEEP=0
FORCE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --tps) TPS="$2"; shift 2 ;;
    --ch-outage) CH_OUTAGE="$2"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    --force) FORCE=1; shift ;;
    *) echo "usage: $0 [--tps N] [--ch-outage SECONDS] [--keep] [--force]" >&2; exit 2 ;;
  esac
done

[ -f .env ] || { echo "no .env: run make env" >&2; exit 2; }
[ -x bin/psg-loadgen ] || { echo "no bin/psg-loadgen: run make bin" >&2; exit 2; }
avail=$(awk '/MemAvailable/ {print int($2/1024)}' /proc/meminfo)
if [ "$avail" -lt 3000 ] && [ "$FORCE" = 0 ]; then
  echo "only ${avail} MB of memory available; need 3000 (--force to run anyway)" >&2
  exit 2
fi

WORK=$(mktemp -d)
ENVF=$WORK/env
OVR=$WORK/override.yml
COOKIES=$WORK/cookies
HTTP=http://127.0.0.1:18090/api/v1
GW_PORT=21001

# The compose file interpolates store ports and image tags from --env-file;
# control and worker read .env, so the override sets their ports explicitly.
grep -vE '^(PSG_VERSION|PSG_HTTP_PORT|PSG_MARIADB_PORT|PSG_REDIS_PORT|PSG_CLICKHOUSE_HTTP_PORT|PSG_CLICKHOUSE_NATIVE_PORT)=' .env >"$ENVF"
cat >>"$ENVF" <<'EOF'
PSG_VERSION=resilience
PSG_HTTP_PORT=18090
PSG_MARIADB_PORT=13307
PSG_REDIS_PORT=16380
PSG_CLICKHOUSE_HTTP_PORT=18124
PSG_CLICKHOUSE_NATIVE_PORT=19001
EOF
cat >"$OVR" <<'EOF'
services:
  control:
    environment:
      PSG_HTTP_PORT: "18090"
      PSG_MARIADB_PORT: "13307"
      PSG_REDIS_PORT: "16380"
      PSG_CLICKHOUSE_HTTP_PORT: "18124"
    mem_limit: 512m
  worker:
    environment:
      PSG_CONTROL_URL: http://127.0.0.1:18090
      PSG_WORKER_METRICS_ADDR: 127.0.0.1:19118
      PSG_PORT_RANGE: 21000-21099
      PSG_REDIS_PORT: "16380"
      PSG_CLICKHOUSE_HTTP_PORT: "18124"
    mem_limit: 512m
  tester:
    environment:
      PSG_CONTROL_URL: http://127.0.0.1:18090
  mariadb:
    mem_limit: 768m
  redis:
    mem_limit: 256m
  clickhouse:
    mem_limit: 1536m
EOF
DC=(docker compose -p psgr --env-file "$ENVF" -f docker-compose.yml -f "$OVR" --profile bundled)
envval() { grep -E "^$1=" .env | tail -n1 | cut -d= -f2-; }
jget() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
log() { echo "[$(date -u +%H:%M:%S)] $*"; }
FAILED=0
check() { # check NAME CONDITION-RESULT(0/1) DETAIL
  if [ "$2" = 0 ]; then echo "  PASS  $1  ($3)"; else echo "  FAIL  $1  ($3)"; FAILED=1; fi
}
wait_for() { # wait_for SECONDS DESCRIPTION COMMAND...
  local deadline=$((SECONDS + $1)) what=$2; shift 2
  until "$@" >/dev/null 2>&1; do
    [ $SECONDS -lt $deadline ] || { log "timed out waiting for $what"; return 1; }
    sleep 2
  done
}
cleanup() {
  if [ "$KEEP" = 1 ]; then
    log "stack left running: ${DC[*]}"
  else
    log "removing the test stack"
    "${DC[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

log "building and starting the test stack (project psgr)"
"${DC[@]}" up -d --build --quiet-pull 2>&1 | tail -n 3
wait_for 300 "control plane" curl -sf http://127.0.0.1:18090/healthz
wait_for 120 "worker" curl -sf http://127.0.0.1:19118/healthz

curl -sf -c "$COOKIES" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$(envval PSG_ADMIN_USERNAME)\",\"password\":\"$(envval PSG_ADMIN_PASSWORD)\"}" \
  "$HTTP/auth/login" >/dev/null
api() { curl -sf -b "$COOKIES" -H 'Content-Type: application/json' "$@"; }
worker_online() { api "$HTTP/workers" | jget 'all(w["online"] for w in d) and len(d) > 0' | grep -q True; }
wait_for 60 "worker registration" worker_online

GW_ID=$(api -X POST "$HTTP/gateways" -d "{\"name\":\"resilience\",\"port\":$GW_PORT,\"system_id\":\"resil\",\"password\":\"resil123\",\"dlr_timing\":\"fixed\",\"dlr_delay_ms\":200}" | jget 'd["id"]')
listening() { api "$HTTP/gateways/$GW_ID" | jget 'd["runtime"]["state"]' | grep -q listening; }
wait_for 30 "gateway listening" listening
log "gateway $GW_ID listening on $GW_PORT; licence: $(api "$HTTP/licence" | jget 'd["status"] + ", " + str(d["max_tps"]) + " TPS"')"

# ---- phase 1: stores go down one at a time under load ----------------------------
DURATION=$((130 + CH_OUTAGE + 40))
log "phase 1: ${TPS} TPS for ${DURATION} s; MariaDB, Redis and ClickHouse stop in turn"
bin/psg-loadgen -addr 127.0.0.1:$GW_PORT -system-id resil -password resil123 -binds 4 \
  -tps "$TPS" -duration ${DURATION}s -report 30s -drain 20s -out "$WORK/phase1.json" >"$WORK/phase1.log" 2>&1 &
LOADGEN=$!
outage() { # outage START_S LENGTH_S SERVICE
  sleep "$1"
  log "  stopping $3 for $2 s"
  "${DC[@]}" stop "$3" >/dev/null 2>&1
  sleep "$2"
  "${DC[@]}" start "$3" >/dev/null 2>&1
  log "  $3 started again"
}
(outage 20 30 mariadb; outage 10 30 redis; outage 10 "$CH_OUTAGE" clickhouse) &
CHAOS=$!
wait $LOADGEN || true
wait $CHAOS || true
cat "$WORK/phase1.log" | tail -n 12

read -r SENT OK UNANSWERED UNEXPECTED DLRS MISSING < <(jget '" ".join(str(d[k]) for k in ("sent","ok","unanswered","unexpected","dlrs","dlrs_missing"))' <"$WORK/phase1.json")

CH_USER=$(envval PSG_CLICKHOUSE_USER)
CH_PASS=$(envval PSG_CLICKHOUSE_PASSWORD)
chq() { curl -sf -H "X-ClickHouse-User: $CH_USER" -H "X-ClickHouse-Key: $CH_PASS" "http://127.0.0.1:18124/?database=$(envval PSG_CLICKHOUSE_DATABASE)" --data-binary "$1"; }
events_caught_up() {
  [ "$(chq "SELECT count() FROM submit_events WHERE gateway_id = $GW_ID")" = "$OK" ] &&
    [ "$(chq "SELECT count() FROM dlr_events WHERE gateway_id = $GW_ID AND result = 'acknowledged'")" = "$DLRS" ]
}
log "waiting for spilled events to be replayed into ClickHouse"
wait_for $((180 + CH_OUTAGE / 2)) "events" events_caught_up || true
SUBMIT_EVENTS=$(chq "SELECT count() FROM submit_events WHERE gateway_id = $GW_ID" || echo "?")
DLR_EVENTS=$(chq "SELECT count() FROM dlr_events WHERE gateway_id = $GW_ID AND result = 'acknowledged'" || echo "?")
SPILLED=$(curl -s http://127.0.0.1:19118/metrics | awk '/^psg_events_total.*result="spilled"/ {s += $2} END {print s+0}')

# ---- phase 2: worker restart while the control plane is down --------------------
log "phase 2: stop the control plane, restart the worker, send traffic"
"${DC[@]}" stop control >/dev/null 2>&1
"${DC[@]}" restart worker >/dev/null 2>&1
wait_for 60 "worker after restart" curl -sf http://127.0.0.1:19118/healthz
smpp_up() { (exec 3<>/dev/tcp/127.0.0.1/$GW_PORT) 2>/dev/null; }
wait_for 30 "gateway port after restart" smpp_up
bin/psg-loadgen -addr 127.0.0.1:$GW_PORT -system-id resil -password resil123 -binds 2 \
  -tps 200 -duration 15s -report 15s -drain 5s -out "$WORK/phase2.json" >"$WORK/phase2.log" 2>&1 || true
"${DC[@]}" start control >/dev/null 2>&1
read -r P2_SENT P2_OK P2_BAD P2_MISSING < <(jget '" ".join(str(x) for x in (d["sent"], d["ok"], d["unanswered"] + d["unexpected"], d["dlrs_missing"]))' <"$WORK/phase2.json")

echo
echo "Resilience test results (${TPS} TPS):"
check "traffic through store outages" "$([ "$SENT" -gt 0 ] && [ "$OK" = "$SENT" ] && [ "$UNANSWERED" = 0 ] && [ "$UNEXPECTED" = 0 ] && echo 0 || echo 1)" \
  "sent $SENT, ok $OK, unanswered $UNANSWERED, unexpected $UNEXPECTED"
check "every DLR delivered" "$([ "$MISSING" = 0 ] && [ "$DLRS" = "$OK" ] && echo 0 || echo 1)" "DLRs $DLRS, missing $MISSING"
check "no submit events lost" "$([ "$SUBMIT_EVENTS" = "$OK" ] && echo 0 || echo 1)" "submit_events $SUBMIT_EVENTS for $OK messages"
check "no DLR events lost" "$([ "$DLR_EVENTS" = "$DLRS" ] && echo 0 || echo 1)" "acknowledged dlr_events $DLR_EVENTS for $DLRS DLRs"
check "events spilled while ClickHouse was down" "$([ "$SPILLED" -gt 0 ] && echo 0 || echo 1)" "$SPILLED rows spilled and replayed"
check "worker restart with control plane down" "$([ "$P2_SENT" -gt 0 ] && [ "$P2_OK" = "$P2_SENT" ] && [ "$P2_BAD" = 0 ] && [ "$P2_MISSING" = 0 ] && echo 0 || echo 1)" \
  "sent $P2_SENT, ok $P2_OK, errors $P2_BAD, DLRs missing $P2_MISSING"
exit $FAILED
