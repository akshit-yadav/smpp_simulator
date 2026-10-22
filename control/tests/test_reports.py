"""Reports and message search over events inserted the way workers write them
(raw tables; the materialized views fill the per-minute roll-ups)."""

import json
from datetime import UTC, datetime, timedelta

import pytest

from psg_control.clickhouse import migrate
from tests.conftest import needs_clickhouse, needs_stores

GW = "/api/v1/gateways"


def new_gw(**over):
    return {"name": "vendor-a", "port": 20001, "system_id": "vendora", "password": "secret1"} | over


def insert(ch, table, rows):
    ch.execute(f"INSERT INTO {table} FORMAT JSONEachRow\n" + "\n".join(map(json.dumps, rows)))


def at(base: datetime, seconds: float) -> str:
    return (base + timedelta(seconds=seconds)).strftime("%Y-%m-%d %H:%M:%S.%f")[:-3]


def ms(t: datetime) -> int:
    return int(t.timestamp() * 1000)


@pytest.fixture
def base():
    """A whole minute half an hour ago: inside the default range and the 3-day TTL."""
    return (datetime.now(UTC) - timedelta(minutes=30)).replace(second=0, microsecond=0)


def window(base):
    return {
        "from": (base - timedelta(minutes=5)).isoformat(),
        "to": (base + timedelta(minutes=20)).isoformat(),
    }


@needs_clickhouse
def test_schema_is_applied_once(ch):
    assert migrate(ch) == []
    names = ch.execute("SELECT name FROM psg_schema ORDER BY version").split()
    assert names == ["001_events.sql", "002_mo.sql"]


@needs_clickhouse
def test_traffic(admin, ch, worker, base):
    gw = admin.post(GW, json=new_gw()).json()
    gid = gw["id"]
    insert(
        ch,
        "submit_events",
        [
            {"ts": at(base, 1), "gateway_id": gid, "result": "accepted", "message_id": 1},
            {"ts": at(base, 2), "gateway_id": gid, "result": "accepted", "message_id": 2},
            {"ts": at(base, 70), "gateway_id": gid, "result": "throttled"},
            {"ts": at(base, 3), "gateway_id": 999, "result": "invalid"},
        ],
    )
    insert(ch, "dlr_events", [{"ts": at(base, 5), "gateway_id": gid, "result": "acknowledged"}])

    r = admin.get("/api/v1/reports/traffic", params=window(base) | {"gateway_id": gid, "step": 60})
    rows = r.json()["rows"]
    assert [(x["t"], x["accepted"], x["throttled"], x["dlr_acknowledged"]) for x in rows] == [
        (ms(base), 2, 0, 1),
        (ms(base) + 60_000, 0, 1, 0),
    ]
    assert rows[0]["gateway"] == "vendor-a"

    every = admin.get("/api/v1/reports/traffic", params=window(base)).json()
    assert every["step"] == 60  # 25 minutes fit in minute steps
    assert {x["gateway_id"] for x in every["rows"]} == {gid, 999}
    assert next(x for x in every["rows"] if x["gateway_id"] == 999)["gateway"] is None  # deleted

    r = admin.get(
        "/api/v1/reports/traffic", params=window(base) | {"gateway_id": gid, "format": "csv"}
    )
    assert r.headers["content-type"].startswith("text/csv")
    assert "attachment" in r.headers["content-disposition"]
    header, first = r.text.splitlines()[:2]
    assert header.startswith("t,gateway_id,gateway,accepted,rejected,throttled")
    assert first.startswith(base.isoformat(timespec="milliseconds") + f",{gid},vendor-a,2,")

    assert (
        admin.get(
            "/api/v1/reports/traffic", params={"from": "2026-01-02", "to": "2026-01-01"}
        ).status_code
        == 422
    )


@needs_clickhouse
def test_rule_mix_actual_vs_configured(admin, ch, worker, base):
    rule = admin.post(
        "/api/v1/dlr-rules",
        json={
            "name": "mix",
            "mode": "weighted",
            "entries": [
                {"name": "ok", "dlr_status": "DELIVRD", "weight_bp": 7000},
                {"name": "fail", "dlr_status": "UNDELIV", "dlr_error": "001", "weight_bp": 2500},
                {"name": "throttle", "command_status": 0x58, "weight_bp": 500},
            ],
        },
    ).json()
    gid = admin.post(GW, json=new_gw(dlr_rule_id=rule["id"])).json()["id"]

    def ev(n, **kw):
        return [{"ts": at(base, i), "gateway_id": gid} | kw for i in range(n)]

    insert(
        ch,
        "submit_events",
        ev(7, result="accepted", outcome="ok")
        + ev(2, result="accepted", outcome="fail")
        + ev(1, result="rejected", outcome="throttle", command_status=0x58)
        + ev(1, result="accepted", outcome="old")  # entry since removed from the rule
        + ev(3, result="throttled", command_status=0x58)
        + ev(1, result="rejected", command_status=4),  # submit_sm on an RX bind
    )
    r = admin.get("/api/v1/reports/outcomes", params=window(base) | {"gateway_id": gid}).json()
    assert r["rule"] == {"id": rule["id"], "name": "mix", "mode": "weighted"}
    assert r["picked"] == 11
    assert {x["name"]: (x["configured_pct"], x["actual"]) for x in r["rows"]} == {
        "ok": (70.0, 7),
        "fail": (25.0, 2),
        "throttle": (5.0, 1),
        "old": (None, 1),
    }
    assert r["rows"][0]["actual_pct"] == pytest.approx(100 * 7 / 11, abs=0.001)
    assert r["not_picked"] == {
        "throttled": 3,
        "invalid": 0,
        "licence_throttled": 0,
        "receiver_only": 1,
    }
    assert admin.get("/api/v1/reports/outcomes", params={"gateway_id": 999}).status_code == 404


@needs_clickhouse
def test_dlr_delay_distribution(admin, ch, base):
    insert(
        ch,
        "dlr_events",
        [
            {"ts": at(base, 1), "gateway_id": 1, "result": "acknowledged", "delay_ms": d}
            for d in (50, 1500, 1999, 400_000)
        ]
        + [{"ts": at(base, 1), "gateway_id": 1, "result": "retried", "delay_ms": 3000}]
        + [
            {
                "ts": at(base, 1),
                "gateway_id": 1,
                "result": "acknowledged",
                "dlr_status": "ENROUTE",
                "delay_ms": 10,
            }
        ],
    )
    r = admin.get("/api/v1/reports/dlr-delay", params=window(base)).json()
    assert r["dlrs"] == 4 and r["avg_ms"] == round((50 + 1500 + 1999 + 400_000) / 4)
    assert {x["from_ms"]: x["dlrs"] for x in r["rows"] if x["dlrs"]} == {0: 1, 1000: 2, 300_000: 1}
    assert r["rows"][0] == {"from_ms": 0, "to_ms": 100, "dlrs": 1}
    assert r["rows"][-1]["to_ms"] is None


@needs_clickhouse
def test_bind_history(admin, ch, worker, base):
    gid = admin.post(GW, json=new_gw()).json()["id"]
    insert(
        ch,
        "session_events",
        [
            {
                "ts": at(base, 1),
                "gateway_id": gid,
                "session_id": 1,
                "event": "bind_failed",
                "status": 14,
            },
            {
                "ts": at(base, 2),
                "gateway_id": gid,
                "session_id": 2,
                "event": "bound",
                "bind_type": "trx",
            },
            {
                "ts": at(base, 9),
                "gateway_id": gid,
                "session_id": 2,
                "event": "closed",
                "submits": 42,
            },
            {"ts": at(base, 5), "gateway_id": 999, "session_id": 3, "event": "killed"},
        ],
    )
    rows = admin.get("/api/v1/reports/binds", params=window(base) | {"gateway_id": gid}).json()[
        "rows"
    ]
    assert [(x["event"], x["status"], x["submits"]) for x in rows] == [
        ("closed", 0, 42),
        ("bound", 0, 0),
        ("bind_failed", 14, 0),
    ]
    assert rows[0]["gateway"] == "vendor-a" and rows[0]["ts"] == ms(base) + 9000
    two = admin.get("/api/v1/reports/binds", params=window(base) | {"limit": 2}).json()
    assert len(two["rows"]) == 2 and two["truncated"]


@needs_clickhouse
def test_message_search(admin, ch, base):
    big = 0x0100000123456789  # worker 1, as the client saw it in submit_sm_resp
    insert(
        ch,
        "submit_events",
        [
            {
                "ts": at(base, 1),
                "gateway_id": 1,
                "message_id": big,
                "dest_addr": "919800000001",
                "result": "accepted",
            },
            {
                "ts": at(base, 2),
                "gateway_id": 1,
                "message_id": 0x12345,
                "dest_addr": "919800000002",
                "result": "accepted",
            },
            {
                "ts": at(base, 3),
                "gateway_id": 1,
                "message_id": 12345,
                "dest_addr": "919800000002",
                "result": "accepted",
            },
            {
                "ts": at(base, 4),
                "gateway_id": 1,
                "source_addr": "=HYPERLINK(1)",
                "dest_addr": "919800000003",
                "result": "throttled",
            },
        ],
    )
    insert(
        ch,
        "dlr_events",
        [
            {
                "ts": at(base, 2),
                "gateway_id": 1,
                "message_id": big,
                "dest_addr": "919800000001",
                "result": "acknowledged",
                "submit_ts": at(base, 1),
            }
        ],
    )

    r = admin.get("/api/v1/messages", params={"message_id": "100000123456789"}).json()
    assert [s["message_id"] for s in r["submits"]] == ["100000123456789"]
    assert r["submits"][0]["message_id_dec"] == str(big)  # past 2^53: a string, not a JS number
    assert [(d["result"], d["submit_ts"]) for d in r["dlrs"]] == [("acknowledged", ms(base) + 1000)]

    # Digits only: could be hex (default DLR id format) or decimal, so both match.
    both = admin.get("/api/v1/messages", params={"message_id": "12345"}).json()
    assert sorted(s["message_id"] for s in both["submits"]) == ["12345", "3039"]

    dest = admin.get("/api/v1/messages", params={"destination": "919800000001"}).json()
    assert len(dest["submits"]) == 1 and len(dest["dlrs"]) == 1 and not dest["truncated"]

    r = admin.get("/api/v1/messages", params={"destination": "919800000003", "format": "csv"})
    assert "'=HYPERLINK(1)" in r.text  # no spreadsheet formulas from client data
    assert r.text.splitlines()[0].startswith("event,ts,")

    assert admin.get("/api/v1/messages").status_code == 422
    assert (
        admin.get("/api/v1/messages", params={"message_id": "1", "destination": "2"}).status_code
        == 422
    )
    assert admin.get("/api/v1/messages", params={"message_id": "xyz"}).status_code == 422


@needs_clickhouse
def test_mo_report_and_search(admin, ch, base):
    insert(
        ch,
        "mo_events",
        [
            {
                "ts": at(base, 1),
                "gateway_id": 1,
                "message_id": 0xA1,
                "source_addr": "919800000009",
                "dest_addr": "56161",
                "result": "acknowledged",
            },
            {
                "ts": at(base, 2),
                "gateway_id": 1,
                "message_id": 0xA2,
                "source_addr": "919800000009",
                "dest_addr": "56161",
                "result": "acknowledged",
                "test": 1,
            },
            {
                "ts": at(base, 3),
                "gateway_id": 1,
                "message_id": 0xA3,
                "source_addr": "919800000008",
                "dest_addr": "56161",
                "result": "dropped",
            },
        ],
    )
    rows = admin.get("/api/v1/reports/mo", params=window(base) | {"step": 60}).json()["rows"]
    assert [(r["acknowledged"], r["dropped"], r["test"]) for r in rows] == [(2, 1, 1)]

    by_number = admin.get("/api/v1/messages", params={"destination": "919800000009"}).json()
    assert sorted(m["message_id"] for m in by_number["mos"]) == ["a1", "a2"]
    by_code = admin.get("/api/v1/messages", params={"destination": "56161"}).json()
    assert len(by_code["mos"]) == 3
    by_id = admin.get("/api/v1/messages", params={"message_id": "a3"}).json()
    assert [m["result"] for m in by_id["mos"]] == ["dropped"]


@needs_stores
def test_reports_unavailable_without_clickhouse(admin):
    # No `ch` fixture: the test's ClickHouse database does not exist (or no server).
    r = admin.get("/api/v1/reports/traffic")
    assert r.status_code == 503 and r.json()["detail"].startswith("Reports are unavailable")


@needs_stores
def test_reports_need_login(client):
    assert client.get("/api/v1/reports/traffic").status_code == 401
    assert client.get("/api/v1/messages", params={"destination": "1"}).status_code == 401
