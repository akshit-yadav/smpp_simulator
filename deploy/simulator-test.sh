#!/usr/bin/env bash
# End-to-end test of the SMPP client simulator (M10). Starts an isolated PSG
# stack (project "psgs", own ports, volumes and bundled stores, every
# container memory-limited) and checks that:
#
#   1. a test started through the API runs on the tester: 2 gateways x 4
#      sessions, network delay 20-50 ms each way, DLR response delay 200 ms,
#   2. the tester's result agrees with PSG's own counters, and response times
#      include both network delays,
#   3. Reports leave simulator traffic out unless asked (include_simulator),
#   4. the tester's temporary PDU trace is gone after the run,
#   5. a second tester that only reaches the control plane over HTTP (as on
#      another machine) registers and runs a test,
#   6. the workers' token is refused on the tester API,
#   7. the footer's PSG figures come from the stack's cgroup slice and match
#      docker stats.
#
#   deploy/simulator-test.sh [--tps N] [--duration S] [--keep] [--force]
#
# Needs Docker Compose v2, curl, python3 and a .env (make env). Uses compose
# project "psgs", control HTTP 18091, worker metrics 19119, SMPP ports
# 21100-21199 and store ports 13308, 16381, 18125 and 19002. Refuses to run
# with under 3 GB of memory available (--force overrides).
set -euo pipefail
cd "$(dirname "$0")/.."

TPS=1000
DURATION=60
KEEP=0
FORCE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --tps) TPS="$2"; shift 2 ;;
    --duration) DURATION="$2"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    --force) FORCE=1; shift ;;
    *) echo "usage: $0 [--tps N] [--duration S] [--keep] [--force]" >&2; exit 2 ;;
  esac
done

[ -f .env ] || { echo "no .env: run make env" >&2; exit 2; }
grep -q '^PSG_TESTER_TOKEN=.' .env || { echo ".env has no PSG_TESTER_TOKEN (see .env.example)" >&2; exit 2; }
avail=$(awk '/MemAvailable/ {print int($2/1024)}' /proc/meminfo)
if [ "$avail" -lt 3000 ] && [ "$FORCE" = 0 ]; then
  echo "only ${avail} MB of memory available; need 3000 (--force to run anyway)" >&2
  exit 2
fi

WORK=$(mktemp -d)
ENVF=$WORK/env
OVR=$WORK/override.yml
COOKIES=$WORK/cookies
HTTP=http://127.0.0.1:18091/api/v1
REMOTE=psgs-remote-tester

grep -vE '^(PSG_VERSION|PSG_HTTP_PORT|PSG_MARIADB_PORT|PSG_REDIS_PORT|PSG_CLICKHOUSE_HTTP_PORT|PSG_CLICKHOUSE_NATIVE_PORT)=' .env >"$ENVF"
cat >>"$ENVF" <<'EOF'
PSG_VERSION=simtest
PSG_HTTP_PORT=18091
PSG_MARIADB_PORT=13308
PSG_REDIS_PORT=16381
PSG_CLICKHOUSE_HTTP_PORT=18125
PSG_CLICKHOUSE_NATIVE_PORT=19002
EOF
cat >"$OVR" <<'EOF'
services:
  control:
    cgroup_parent: psgtest.slice  # keep test stacks out of the live PSG figure
    environment:
      PSG_HTTP_PORT: "18091"
      PSG_CGROUP_SLICE: psgtest.slice
      PSG_MARIADB_PORT: "13308"
      PSG_REDIS_PORT: "16381"
      PSG_CLICKHOUSE_HTTP_PORT: "18125"
    mem_limit: 512m
  worker:
    cgroup_parent: psgtest.slice  # keep test stacks out of the live PSG figure
    environment:
      PSG_CONTROL_URL: http://127.0.0.1:18091
      PSG_WORKER_METRICS_ADDR: 127.0.0.1:19119
      PSG_PORT_RANGE: 21100-21199
      PSG_REDIS_PORT: "16381"
      PSG_CLICKHOUSE_HTTP_PORT: "18125"
    mem_limit: 512m
  tester:
    cgroup_parent: psgtest.slice  # keep test stacks out of the live PSG figure
    environment:
      PSG_CONTROL_URL: http://127.0.0.1:18091
      PSG_TESTER_NAME: local-tester
  mariadb:
    cgroup_parent: psgtest.slice  # keep test stacks out of the live PSG figure
    mem_limit: 768m
  redis:
    cgroup_parent: psgtest.slice  # keep test stacks out of the live PSG figure
    mem_limit: 256m
  clickhouse:
    cgroup_parent: psgtest.slice  # keep test stacks out of the live PSG figure
    mem_limit: 1536m
EOF
DC=(docker compose -p psgs --env-file "$ENVF" -f docker-compose.yml -f "$OVR" --profile bundled)
envval() { grep -E "^$1=" .env | tail -n1 | cut -d= -f2-; }
jget() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
log() { echo "[$(date -u +%H:%M:%S)] $*"; }
FAILED=0
check() {
  if [ "$2" = 0 ]; then echo "  PASS  $1  ($3)"; else echo "  FAIL  $1  ($3)"; FAILED=1; fi
}
wait_for() {
  local deadline=$((SECONDS + $1)) what=$2; shift 2
  until "$@" >/dev/null 2>&1; do
    [ $SECONDS -lt $deadline ] || { log "timed out waiting for $what"; return 1; }
    sleep 2
  done
}
cleanup() {
  docker rm -f "$REMOTE" >/dev/null 2>&1 || true
  if [ "$KEEP" = 1 ]; then
    log "stack left running: ${DC[*]}"
  else
    log "removing the test stack"
    "${DC[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

log "building and starting the test stack (project psgs)"
"${DC[@]}" up -d --build --quiet-pull 2>&1 | tail -n 3
wait_for 300 "control plane" curl -sf http://127.0.0.1:18091/healthz
wait_for 120 "worker" curl -sf http://127.0.0.1:19119/healthz
curl -sf -c "$COOKIES" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$(envval PSG_ADMIN_USERNAME)\",\"password\":\"$(envval PSG_ADMIN_PASSWORD)\"}" \
  "$HTTP/auth/login" >/dev/null
api() { curl -sf -b "$COOKIES" -H 'Content-Type: application/json' "$@"; }
worker_online() { api "$HTTP/workers" | jget 'len(d) > 0 and all(w["online"] for w in d)' | grep -q True; }
tester_online() { api "$HTTP/testers" | jget "any(t['online'] and t['id'] == $1 for t in d)" | grep -q True; }
wait_for 60 "worker registration" worker_online
wait_for 60 "local tester" tester_online 1

GW=()
for i in 1 2; do
  id=$(api -X POST "$HTTP/gateways" -d "{\"name\":\"sim-$i\",\"port\":$((21100 + i)),\"system_id\":\"sim$i\",\"password\":\"simpw$i\",\"dlr_timing\":\"fixed\",\"dlr_delay_ms\":200,\"max_binds\":10}" | jget 'd["id"]')
  GW+=("$id")
  listening() { api "$HTTP/gateways/$1" | jget 'd["runtime"]["state"]' | grep -q listening; }
  wait_for 30 "gateway $id" listening "$id"
done
log "gateways ${GW[*]} listening; licence: $(api "$HTTP/licence" | jget 'd["status"]')"

run_test() { # run_test BODY TIMEOUT_S -> run id; waits until finished
  local id status deadline
  id=$(api -X POST "$HTTP/tests" -d "$1" | jget 'd["id"]')
  deadline=$((SECONDS + $2))
  while :; do
    status=$(api "$HTTP/tests/$id" | jget 'd["status"]')
    case "$status" in done | stopped | failed) break ;; esac
    [ $SECONDS -lt $deadline ] || { log "test $id still $status"; break; }
    sleep 3
  done
  echo "$id"
}

# ---- 1-4: a test through the API ----------------------------------------------
log "test 1: ${TPS} TPS for ${DURATION} s, 2 gateways x 4 sessions, network 20-50 ms, response delay 200 ms"
BODY=$(printf '{"tester_id":1,"gateway_ids":[%s,%s],"sessions":4,"tps":%s,"duration_s":%s,"dlr":"final","net_delay":{"min_ms":20,"max_ms":50},"resp_delay":{"min_ms":200,"max_ms":200},"keep_errors":true,"trace":true}' "${GW[0]}" "${GW[1]}" "$TPS" "$DURATION")
RUN=$(run_test "$BODY" $((DURATION + 120)))
api "$HTTP/tests/$RUN" >"$WORK/run.json"
read -r STATUS SENT OK UNANSWERED ERRORS DLRS MISSING P50 SESSIONS < <(jget '" ".join(str(x) for x in (d["status"], d["result"]["sent"], d["result"]["ok"], d["result"]["unanswered"], d["result"]["errors"], d["result"]["dlrs"], d["result"]["dlrs_missing"], d["result"]["latency"]["p50_us"], len(d["result"].get("per_session") or [])))' <"$WORK/run.json")
log "test $RUN: $STATUS, sent $SENT, ok $OK, DLRs $DLRS, p50 $((P50 / 1000)) ms, errors $ERRORS"

metric() { curl -s http://127.0.0.1:19119/metrics | awk -v re="$1" '$0 ~ re {s += $2} END {print s+0}'; }
PSG_ACCEPTED=$(metric '^psg_submit_total.*result="accepted"')
PSG_DLR_ACK=$(metric '^psg_dlr_total.*result="acknowledged"')
PSG_SESSIONS_MAX=$SESSIONS

traffic() { api "$HTTP/reports/traffic?from=$(date -u -d '-1 hour' +%FT%TZ)&to=$(date -u -d '+1 minute' +%FT%TZ)$1" | jget 'sum(r["accepted"] for r in d["rows"])'; }
events_in() { [ "$(traffic '&include_simulator=true')" = "$OK" ]; }
wait_for 120 "simulator events in ClickHouse" events_in || true
WITH_SIM=$(traffic '&include_simulator=true')
WITHOUT_SIM=$(traffic '')
TRACE_LEFT=$(docker cp psgs-tester-1:/tmp/psg-tester - 2>/dev/null | tar -t 2>/dev/null | grep -cv '/$' || true)
ERRLOG=$(api "$HTTP/tests/$RUN/errors" | jget 'len(d["errors"])')

# ---- footer usage figures --------------------------------------------------------
api "$HTTP/system/usage" >/dev/null # first sample
sleep 3
read -r U_AVAIL U_MEM U_CPU U_SRV < <(api "$HTTP/system/usage" | jget '" ".join(str(x) for x in (d["psg"]["available"], d["psg"]["mem_mb"], d["psg"]["cpu_pct"], d["server"]["mem_used_mb"]))')
STACK_MB=$(docker stats --no-stream --format '{{.Name}} {{.MemUsage}}' | awk '/^psgs-/ {v=$2; u=v; gsub(/[0-9.]/,"",u); gsub(/[A-Za-z]/,"",v); if (u=="GiB") v*=1024; else if (u=="KiB") v/=1024; s+=v} END {print int(s)}')
U_MS=$(curl -s -o /dev/null -w '%{time_total}' -b "$COOKIES" "$HTTP/system/usage" | python3 -c "import sys; print(round(float(sys.stdin.read()) * 1000))")

# ---- 5: a tester reaching the control plane over HTTP only ----------------------
log "test 2: a second tester with only the control plane URL and the tester token"
docker run -d --name "$REMOTE" --network host --memory 256m \
  -e PSG_CONTROL_URL=http://127.0.0.1:18091 -e PSG_TESTER_TOKEN="$(envval PSG_TESTER_TOKEN)" \
  -e PSG_TESTER_ID=2 -e PSG_TESTER_NAME=remote-tester psg-tester:simtest >/dev/null
wait_for 60 "remote tester" tester_online 2
RUN2=$(run_test "$(printf '{"tester_id":2,"gateway_ids":[%s],"sessions":2,"tps":200,"duration_s":10}' "${GW[0]}")" 90)
read -r R2_STATUS R2_SENT R2_OK < <(api "$HTTP/tests/$RUN2" | jget '" ".join(str(x) for x in (d["status"], d["result"]["sent"], d["result"]["ok"]))')

# ---- 6: token separation ------------------------------------------------------------
TOKEN_CODE=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(envval PSG_INTERNAL_TOKEN)" http://127.0.0.1:18091/internal/v1/testers/1/job)

EXPECTED=$(python3 -c "print(int($TPS * $DURATION))")
echo
echo "Simulator test results:"
check "test ran to completion" "$([ "$STATUS" = done ] && echo 0 || echo 1)" "status $STATUS"
check "offered load sent" "$(python3 -c "print(0 if abs($SENT - $EXPECTED) <= $EXPECTED * 0.02 else 1)")" "sent $SENT of $EXPECTED"
check "every submit_sm answered and every DLR received" "$([ "$OK" = "$SENT" ] && [ "$UNANSWERED" = 0 ] && [ "$DLRS" = "$OK" ] && [ "$MISSING" = 0 ] && [ "$ERRORS" = 0 ] && echo 0 || echo 1)" \
  "ok $OK, unanswered $UNANSWERED, DLRs $DLRS, missing $MISSING, errors $ERRORS (error log $ERRLOG)"
check "tester agrees with PSG's counters" "$([ "$PSG_ACCEPTED" = "$OK" ] && [ "$PSG_DLR_ACK" = "$DLRS" ] && echo 0 || echo 1)" \
  "PSG accepted $PSG_ACCEPTED, DLRs acknowledged $PSG_DLR_ACK"
check "network delay in response times" "$([ "$P50" -ge 40000 ] && echo 0 || echo 1)" "p50 $((P50 / 1000)) ms with 20-50 ms each way"
check "per-session results from the trace" "$([ "$PSG_SESSIONS_MAX" = 8 ] && echo 0 || echo 1)" "$PSG_SESSIONS_MAX sessions"
check "reports leave simulator traffic out" "$([ "$WITHOUT_SIM" = 0 ] && [ "$WITH_SIM" = "$OK" ] && echo 0 || echo 1)" \
  "default $WITHOUT_SIM, with include_simulator $WITH_SIM"
check "PDU trace deleted after the run" "$([ "$TRACE_LEFT" = 0 ] && echo 0 || echo 1)" "$TRACE_LEFT files left"
check "tester over HTTP only (remote setup)" "$([ "$R2_STATUS" = done ] && [ "$R2_OK" = "$R2_SENT" ] && [ "$R2_SENT" -gt 1900 ] && echo 0 || echo 1)" \
  "status $R2_STATUS, sent $R2_SENT, ok $R2_OK"
check "footer: PSG usage from its cgroup slice" "$([ "$U_AVAIL" = True ] && [ "$U_CPU" != None ] && python3 -c "import sys; sys.exit(0 if abs($U_MEM - $STACK_MB) <= max(150, $STACK_MB * 0.25) else 1)" && echo 0 || echo 1)" \
  "PSG $U_MEM MB (docker stats $STACK_MB MB), CPU $U_CPU%, server $U_SRV MB used, endpoint ${U_MS} ms"
check "workers' token refused on the tester API" "$([ "$TOKEN_CODE" = 401 ] && echo 0 || echo 1)" "HTTP $TOKEN_CODE"
exit $FAILED
