"""Reports and message search, from the ClickHouse events workers write.

Roll-ups (per minute) back the reports and are kept 180 days; raw events back
bind history and message search and are kept 3 days. Every endpoint takes
from/to (default: the last 24 hours) and format=json|csv. Times in JSON are
Unix milliseconds; CSV has ISO 8601 UTC.
"""

import csv
import io
from datetime import UTC, datetime, timedelta
from typing import Annotated, Literal

from fastapi import APIRouter, HTTPException, Query
from fastapi.responses import Response
from sqlalchemy import select

from psg_control import resolve
from psg_control.api.deps import DB, Admin, StoresDep
from psg_control.clickhouse import ClickHouse, ClickHouseError
from psg_control.models import DLRRule, Gateway

router = APIRouter(tags=["reports"])

Format = Literal["json", "csv"]
IncludeSimulator = Annotated[
    bool, Query(description="Include traffic from PSG's own client simulator (psg-tester)")
]
GatewayIDs = Annotated[list[int] | None, Query(description="Repeat to filter by several")]

STEPS_S = (60, 300, 900, 3600, 6 * 3600, 86400)
MAX_POINTS = 360
SUBMIT_RESULTS = ("accepted", "rejected", "throttled", "invalid", "licence_throttled")
DLR_RESULTS = ("acknowledged", "retried", "failed", "queued", "expired")
MO_RESULTS = DLR_RESULTS + ("dropped",)
# Roll-up rows whose minute overlaps [from, to).
MINUTES = "minute >= toStartOfMinute({from:DateTime64(3)}) AND minute < {to:DateTime64(3)}"
DELAY_BUCKETS_MS = (0, 100, 250, 500, 1000, 2000, 5000, 10000, 20000, 30000, 60000, 120000)


# ---- helpers -------------------------------------------------------------------------


def time_range(start: datetime | None, end: datetime | None) -> tuple[datetime, datetime]:
    """from/to as aware UTC; naive values are taken as UTC."""
    end = _utc(end) if end else datetime.now(UTC)
    start = _utc(start) if start else end - timedelta(hours=24)
    if start >= end:
        raise HTTPException(status_code=422, detail="'from' must be before 'to'.")
    return start, end


def _utc(t: datetime) -> datetime:
    return t.replace(tzinfo=UTC) if t.tzinfo is None else t.astimezone(UTC)


def simulator_filter(include: bool, column: str = "simulator") -> str:
    """Simulator traffic is left out of reports unless asked for."""
    return "" if include else f"AND {column} = 0"


def gateway_filter(ids: list[int] | None, params: dict) -> str:
    """A constant SQL fragment; the IDs go in as a parameter."""
    if not ids:
        return ""
    params["gw"] = ids
    return "AND gateway_id IN {gw:Array(UInt32)}"


def run(ch: ClickHouse, sql: str, params: dict) -> list[dict]:
    try:
        return ch.rows(sql, params)
    except ClickHouseError as exc:
        raise HTTPException(status_code=503, detail=f"Reports are unavailable: {exc}") from exc


def gateway_names(db) -> dict[int, str]:
    return dict(db.execute(select(Gateway.id, Gateway.name)).all())


def respond(rows: list[dict], fmt: Format, name: str, body: dict | None = None):
    if fmt == "csv":
        return csv_response(rows, name)
    return (body or {}) | {"rows": rows}


TIME_FIELDS = {"t", "ts", "submit_ts"}


def csv_response(rows: list[dict], name: str) -> Response:
    buf = io.StringIO()
    if rows:
        w = csv.DictWriter(buf, fieldnames=list(rows[0]))
        w.writeheader()
        for row in rows:
            w.writerow({k: _csv_value(k, v) for k, v in row.items()})
    stamp = datetime.now(UTC).strftime("%Y%m%d-%H%M%S")
    return Response(
        buf.getvalue(),
        media_type="text/csv",
        headers={"Content-Disposition": f'attachment; filename="psg-{name}-{stamp}.csv"'},
    )


def _csv_value(key: str, v):
    if key in TIME_FIELDS and isinstance(v, int):
        return datetime.fromtimestamp(v / 1000, UTC).isoformat(timespec="milliseconds")
    if isinstance(v, str) and v[:1] in ("=", "+", "-", "@", "\t", "\r"):
        return "'" + v  # addresses come from SMPP clients: no spreadsheet formulas
    return v


def pick_step(start: datetime, end: datetime) -> int:
    span = (end - start).total_seconds()
    return next((s for s in STEPS_S if span / s <= MAX_POINTS), STEPS_S[-1])


# ---- reports -------------------------------------------------------------------------


@router.get("/reports/traffic")
def traffic(
    db: DB,
    admin: Admin,
    stores: StoresDep,
    start: Annotated[datetime | None, Query(alias="from")] = None,
    end: Annotated[datetime | None, Query(alias="to")] = None,
    gateway_id: GatewayIDs = None,
    step: Annotated[int | None, Query(ge=60, le=86400)] = None,
    include_simulator: IncludeSimulator = False,
    format: Format = "json",
):
    """submit_sm by result and DLRs by result, per gateway and time step.
    Throttled vs licence_throttled is the throttling report."""
    start, end = time_range(start, end)
    step = step or pick_step(start, end)
    params = {"from": start, "to": end, "step": step}
    gw = gateway_filter(gateway_id, params)
    bucket = "toUnixTimestamp(toStartOfInterval(minute, toIntervalSecond({step:UInt32}))) * 1000"
    where = f"{MINUTES} {gw} {simulator_filter(include_simulator)}"
    submits = run(
        stores.clickhouse,
        f"SELECT {bucket} AS t, gateway_id, "
        + ", ".join(f"sumIf(messages, result = '{r}') AS {r}" for r in SUBMIT_RESULTS)
        + f" FROM submit_minute WHERE {where} GROUP BY t, gateway_id",
        params,
    )
    dlrs = run(
        stores.clickhouse,
        f"SELECT {bucket} AS t, gateway_id, "
        + ", ".join(f"sumIf(dlrs, result = '{r}') AS dlr_{r}" for r in DLR_RESULTS)
        + f" FROM dlr_minute WHERE {where} GROUP BY t, gateway_id",
        params,
    )
    names = gateway_names(db)
    zero = {r: 0 for r in SUBMIT_RESULTS} | {f"dlr_{r}": 0 for r in DLR_RESULTS}
    merged: dict[tuple[int, int], dict] = {}
    for row in submits + dlrs:
        key = (row["t"], row["gateway_id"])
        if key not in merged:
            merged[key] = {"t": row["t"], "gateway_id": row["gateway_id"]}
            merged[key]["gateway"] = names.get(row["gateway_id"])
            merged[key] |= zero
        merged[key] |= row
    rows = [merged[k] for k in sorted(merged)]
    return respond(rows, format, "traffic", {"from": start, "to": end, "step": step})


@router.get("/reports/mo")
def mo_traffic(
    db: DB,
    admin: Admin,
    stores: StoresDep,
    start: Annotated[datetime | None, Query(alias="from")] = None,
    end: Annotated[datetime | None, Query(alias="to")] = None,
    gateway_id: GatewayIDs = None,
    step: Annotated[int | None, Query(ge=60, le=86400)] = None,
    include_simulator: IncludeSimulator = False,
    format: Format = "json",
):
    """MO deliver_sm by result, per gateway and time step. Each part of a long
    MO counts once; `test` counts those sent with "Send test MO"."""
    start, end = time_range(start, end)
    step = step or pick_step(start, end)
    params = {"from": start, "to": end, "step": step}
    gw = gateway_filter(gateway_id, params)
    rows = run(
        stores.clickhouse,
        "SELECT toUnixTimestamp(toStartOfInterval(minute, toIntervalSecond({step:UInt32}))) * 1000"
        " AS t, gateway_id, "
        + ", ".join(f"sumIf(messages, result = '{r}') AS {r}" for r in MO_RESULTS)
        + ", sumIf(messages, test = 1) AS test"
        + f" FROM mo_minute WHERE {MINUTES} {gw} {simulator_filter(include_simulator)}"
        + " GROUP BY t, gateway_id ORDER BY t, gateway_id",
        params,
    )
    names = gateway_names(db)
    rows = [
        {"t": r["t"], "gateway_id": r["gateway_id"], "gateway": names.get(r["gateway_id"])} | r
        for r in rows
    ]
    return respond(rows, format, "mo", {"from": start, "to": end, "step": step})


@router.get("/reports/outcomes")
def outcomes(
    db: DB,
    admin: Admin,
    stores: StoresDep,
    gateway_id: int,
    start: Annotated[datetime | None, Query(alias="from")] = None,
    end: Annotated[datetime | None, Query(alias="to")] = None,
    include_simulator: IncludeSimulator = False,
    format: Format = "json",
):
    """One gateway's DLR rule mix: the share each entry actually got against
    its configured share. Messages that never reached the rule (throttled,
    invalid, bound as receiver) are counted apart."""
    gw = db.get(Gateway, gateway_id)
    if gw is None:
        raise HTTPException(status_code=404, detail="Gateway not found.")
    start, end = time_range(start, end)
    actual = run(
        stores.clickhouse,
        "SELECT outcome, toString(result) AS result, command_status, sum(messages) AS messages "
        "FROM submit_minute WHERE gateway_id = {gw:UInt32} "
        f"AND {MINUTES} {simulator_filter(include_simulator)} "
        "GROUP BY outcome, result, command_status",
        {"gw": gateway_id, "from": start, "to": end},
    )
    rule = db.get(DLRRule, resolve.effective(gw, resolve.defaults(db))["dlr_rule_id"])
    rows = mix_rows(rule, actual)
    picked = sum(r["actual"] for r in rows)
    for r in rows:
        r["actual_pct"] = round(100 * r["actual"] / picked, 3) if picked else None
    not_picked = {r: 0 for r in ("throttled", "invalid", "licence_throttled", "receiver_only")}
    for a in actual:
        if a["result"] in not_picked:
            not_picked[a["result"]] += a["messages"]
        elif a["result"] == "rejected" and a["outcome"] == "" and a["command_status"] == 4:
            not_picked["receiver_only"] += a["messages"]  # submit_sm on an RX bind
    body = {
        "gateway_id": gw.id,
        "rule": {"id": rule.id, "name": rule.name, "mode": rule.mode},
        "picked": picked,
        "not_picked": not_picked,
    }
    return respond(rows, format, f"rule-mix-{gw.id}", body)


def mix_rows(rule: DLRRule, actual: list[dict]) -> list[dict]:
    """Rule entries with configured and actual counts, matched on (name,
    command_status); outcomes no longer in the rule are listed after them."""
    n = len(rule.entries)
    rows, index = [], {}
    for i, e in enumerate(rule.entries):
        if rule.mode == "weighted":
            pct = e.weight_bp / 100
        elif rule.mode == "random":
            pct = 100 / n
        else:  # fixed: the first entry
            pct = 100.0 if i == 0 else 0.0
        index[(e.name, e.command_status)] = len(rows)
        rows.append(
            {
                "name": e.name,
                "command_status": e.command_status,
                "dlr_status": e.dlr_status if e.command_status == 0 else None,
                "dlr_error": e.dlr_error if e.command_status == 0 else None,
                "configured_pct": round(pct, 3),
                "actual": 0,
            }
        )
    for a in actual:
        if a["result"] not in ("accepted", "rejected") or (
            a["outcome"] == "" and a["command_status"] == 4 and ("", 4) not in index
        ):
            continue
        key = (a["outcome"], a["command_status"])
        if key not in index:
            index[key] = len(rows)
            rows.append(
                {
                    "name": a["outcome"],
                    "command_status": a["command_status"],
                    "dlr_status": None,
                    "dlr_error": None,
                    "configured_pct": None,  # not in the current rule
                    "actual": 0,
                }
            )
        rows[index[key]]["actual"] += a["messages"]
    return rows


@router.get("/reports/dlr-delay")
def dlr_delay(
    db: DB,
    admin: Admin,
    stores: StoresDep,
    start: Annotated[datetime | None, Query(alias="from")] = None,
    end: Annotated[datetime | None, Query(alias="to")] = None,
    gateway_id: GatewayIDs = None,
    include_simulator: IncludeSimulator = False,
    format: Format = "json",
):
    """Acknowledged final DLRs (intermediate ENROUTE/ACCEPTD receipts left out)
    by delay from submit_sm to deliver_sm, in buckets
    (from_ms inclusive, to_ms exclusive; the last bucket is open-ended)."""
    start, end = time_range(start, end)
    params = {"from": start, "to": end}
    gw = gateway_filter(gateway_id, params)
    got = run(
        stores.clickhouse,
        "SELECT delay_bucket, sum(dlrs) AS dlrs, sum(delay_ms_sum) AS delay_ms_sum "
        "FROM dlr_minute WHERE result = 'acknowledged' "
        "AND dlr_status NOT IN ('ENROUTE', 'ACCEPTD') "
        f"AND {MINUTES} {gw} {simulator_filter(include_simulator)} "
        "GROUP BY delay_bucket",
        params,
    )
    by_bucket = {g["delay_bucket"]: g["dlrs"] for g in got}
    bounds = sorted(set(DELAY_BUCKETS_MS) | set(by_bucket))
    rows = [
        {"from_ms": b, "to_ms": bounds[i + 1] if i + 1 < len(bounds) else None}
        | {"dlrs": by_bucket.get(b, 0)}
        for i, b in enumerate(bounds)
    ]
    total = sum(g["dlrs"] for g in got)
    avg = round(sum(g["delay_ms_sum"] for g in got) / total) if total else None
    return respond(rows, format, "dlr-delay", {"dlrs": total, "avg_ms": avg})


@router.get("/reports/binds")
def binds(
    db: DB,
    admin: Admin,
    stores: StoresDep,
    start: Annotated[datetime | None, Query(alias="from")] = None,
    end: Annotated[datetime | None, Query(alias="to")] = None,
    gateway_id: GatewayIDs = None,
    limit: Annotated[int, Query(ge=1, le=10000)] = 1000,
    include_simulator: IncludeSimulator = False,
    format: Format = "json",
):
    """Binds, failed binds, closes and admin kills, newest first (3 days kept)."""
    start, end = time_range(start, end)
    params = {"from": start, "to": end, "limit": limit}
    gw = gateway_filter(gateway_id, params)
    rows = run(
        stores.clickhouse,
        "SELECT toUnixTimestamp64Milli(ts) AS ts, gateway_id, worker_id, session_id, "
        "toString(event) AS event, bind_type, system_id, remote, status, submits "
        "FROM session_events AS e WHERE e.ts >= {from:DateTime64(3)} AND e.ts < {to:DateTime64(3)} "
        f"{gw} {simulator_filter(include_simulator, 'e.simulator')} "
        "ORDER BY e.ts DESC LIMIT {limit:UInt32}",
        params,
    )
    names = gateway_names(db)
    rows = [{"ts": r["ts"], "gateway": names.get(r["gateway_id"])} | r for r in rows]
    return respond(rows, format, "binds", {"truncated": len(rows) == limit})


# ---- message search ------------------------------------------------------------------


def id_candidates(text: str) -> list[int]:
    """A message ID as the client saw it: hex (submit_sm_resp, DLR id: by
    default) or decimal (DLR id: in decimal format). Digits-only input could
    be either, so both are searched."""
    out = set()
    for base in (16, 10):
        try:
            v = int(text, base)
        except ValueError:
            continue
        if 0 < v < 1 << 64:
            out.add(v)
    return sorted(out)


# Raw-event queries select ts and message_id converted under their own names.
# In ClickHouse a SELECT alias shadows the column in WHERE, so filters use the
# table alias (e.ts, e.message_id) to reach the stored values.
SUBMIT_COLUMNS = (
    "toUnixTimestamp64Milli(ts) AS ts, worker_id, gateway_id, session_id, system_id, "
    "toString(message_id) AS message_id, source_addr, dest_addr, toString(result) AS result, "
    "command_status, outcome, dlr_status, dlr_error, registered_delivery, data_coding, sm_length, "
    "simulator"
)
MO_COLUMNS = (
    "toUnixTimestamp64Milli(ts) AS ts, worker_id, gateway_id, session_id, "
    "toString(message_id) AS message_id, source_addr, dest_addr, toString(result) AS result, "
    "test, encoding, part, parts, sm_length, text, attempt, resp_ms, command_status, simulator"
)
DLR_COLUMNS = (
    "toUnixTimestamp64Milli(ts) AS ts, worker_id, gateway_id, session_id, "
    "toString(message_id) AS message_id, source_addr, dest_addr, toString(result) AS result, "
    "dlr_status, dlr_error, attempt, toUnixTimestamp64Milli(submit_ts) AS submit_ts, "
    "delay_ms, resp_ms, command_status, simulator"
)


@router.get("/messages")
def search_messages(
    db: DB,
    admin: Admin,
    stores: StoresDep,
    message_id: str | None = None,
    destination: str | None = None,
    start: Annotated[datetime | None, Query(alias="from")] = None,
    end: Annotated[datetime | None, Query(alias="to")] = None,
    limit: Annotated[int, Query(ge=1, le=5000)] = 500,
    format: Format = "json",
):
    """Submit and DLR events of one message (by message ID) or of every
    message to a destination, newest first, plus MO events of that ID or with
    that number as subscriber or short code. Raw events are kept 3 days, so
    from defaults to 3 days ago."""
    if bool(message_id) == bool(destination):
        raise HTTPException(status_code=422, detail="Give either message_id or destination.")
    end = _utc(end) if end else datetime.now(UTC)
    start, end = time_range(start or end - timedelta(days=3), end)
    params = {"from": start, "to": end, "limit": limit}
    if message_id:
        ids = id_candidates(message_id.strip())
        if not ids:
            raise HTTPException(status_code=422, detail="Not a message ID (hex or decimal).")
        params["ids"] = ids
        match = mo_match = "e.message_id IN {ids:Array(UInt64)}"
    else:
        params["dst"] = destination.strip()
        match = "e.dest_addr = {dst:String}"
        mo_match = "(e.source_addr = {dst:String} OR e.dest_addr = {dst:String})"
    when = "e.ts >= {from:DateTime64(3)} AND e.ts < {to:DateTime64(3)}"
    where = f"{when} AND {match}"
    order = "ORDER BY e.ts DESC LIMIT {limit:UInt32}"
    ch = stores.clickhouse
    submits = run(
        ch, f"SELECT {SUBMIT_COLUMNS} FROM submit_events AS e WHERE {where} {order}", params
    )
    dlrs = run(ch, f"SELECT {DLR_COLUMNS} FROM dlr_events AS e WHERE {where} {order}", params)
    mos = run(
        ch, f"SELECT {MO_COLUMNS} FROM mo_events AS e WHERE {when} AND {mo_match} {order}", params
    )
    names = gateway_names(db)
    for r in submits + dlrs + mos:
        mid = int(r["message_id"])
        r["message_id"] = format_id(mid)
        r["message_id_dec"] = str(mid) if mid else ""
        r["gateway"] = names.get(r["gateway_id"])
    if format == "csv":
        rows = (
            [{"event": "submit"} | r for r in submits]
            + [{"event": "dlr"} | r for r in dlrs]
            + [{"event": "mo"} | r for r in mos]
        )
        keys = list(dict.fromkeys(k for r in rows for k in r))
        rows = sorted(({k: r.get(k, "") for k in keys} for r in rows), key=lambda r: -r["ts"])
        return csv_response(rows, "messages")
    return {
        "submits": submits,
        "dlrs": dlrs,
        "mos": mos,
        "truncated": limit in (len(submits), len(dlrs), len(mos)),
    }


def format_id(mid: int) -> str:
    return format(mid, "x") if mid else ""
