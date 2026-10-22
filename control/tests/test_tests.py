"""SMPP client simulator: the admin API and the tester agent's API."""

import json
from datetime import timedelta

from sqlalchemy.orm import Session

from psg_control.api.tests import expire_stale
from psg_control.auth import utcnow
from psg_control.models import TestRun
from tests.conftest import INTERNAL_TOKEN, TESTER_TOKEN, needs_stores

pytestmark = needs_stores

GW = "/api/v1/gateways"
AGENT = {"Authorization": f"Bearer {TESTER_TOKEN}"}


def listening_gateway(admin, rdb, i=1, **over):
    gw = admin.post(
        GW, json={"name": f"g{i}", "port": 20000 + i, "system_id": f"s{i}", "password": "pw"} | over
    ).json()
    rdb.hset(
        "psg:gwstatus:1", str(gw["id"]), json.dumps({"state": "listening", "port": gw["port"]})
    )
    return gw


def heartbeat(client, tester_id=1, **over):
    body = {"name": "lab-tester", "ip": "10.0.0.9", "version": "t", "mem_available_mb": 4000} | over
    r = client.post(f"/internal/v1/testers/{tester_id}/heartbeat", json=body, headers=AGENT)
    assert r.status_code == 204, r.text


def test_full_run_through_the_tester_api(admin, rdb, worker):
    gw = listening_gateway(admin, rdb)
    assert (
        admin.post(
            "/api/v1/tests", json={"gateway_ids": [gw["id"]], "tps": 100, "duration_s": 5}
        ).status_code
        == 422
    )  # no tester
    heartbeat(admin)
    testers = admin.get("/api/v1/testers").json()
    assert testers[0]["online"] and testers[0]["name"] == "lab-tester"

    spec = {
        "gateway_ids": [gw["id"]],
        "sessions": 2,
        "tps": 100,
        "duration_s": 5,
        "dlr": "intermediate",
        "net_delay": {"min_ms": 10, "max_ms": 40},
        "resp_delay": {"min_ms": 200, "max_ms": 200},
    }
    run = admin.post("/api/v1/tests", json=spec).json()
    assert run["status"] == "queued" and run["warnings"] == []
    assert admin.post("/api/v1/tests", json=spec).status_code == 409  # one at a time per tester

    job = admin.get("/internal/v1/testers/1/job", headers=AGENT).json()
    assert job["run_id"] == run["id"] and job["registered_delivery"] == 0x11
    assert job["targets"] == [
        {
            "gateway_id": gw["id"],
            "name": "g1",
            "addr": "10.0.0.1:20001",
            "system_id": "s1",
            "password": "pw",
            "sessions": 2,
            "bind_type": "trx",
        }
    ]
    assert job["net_delay"] == {"min_ms": 10, "max_ms": 40}
    assert admin.get("/internal/v1/testers/1/job", headers=AGENT).status_code == 204

    progress = {"progress": {"phase": "sending", "last": {"t": 1, "sent": 100}, "sent": 100}}
    assert admin.post(
        f"/internal/v1/tests/{run['id']}/progress", json=progress, headers=AGENT
    ).json() == {"stop": False}
    errors = [{"kind": "rejected", "status": "ESME_RSUBMITFAIL"}, {"kind": "timeout"}]
    admin.post(f"/internal/v1/tests/{run['id']}/errors", json=errors, headers=AGENT)
    live = admin.get(f"/api/v1/tests/{run['id']}").json()
    assert live["status"] == "running" and live["progress"]["progress"]["sent"] == 100
    assert live["timeline"] == [{"t": 1, "sent": 100}]

    result = {"sent": 500, "ok": 498, "errors": 2, "achieved_tps": 100.0, "stopped": ""}
    assert (
        admin.post(
            f"/internal/v1/tests/{run['id']}/result", json={"result": result}, headers=AGENT
        ).status_code
        == 204
    )
    done = admin.get(f"/api/v1/tests/{run['id']}").json()
    assert done["status"] == "done" and done["result"]["ok"] == 498 and done["progress"] is None
    errs = admin.get(f"/api/v1/tests/{run['id']}/errors").json()
    assert [e["kind"] for e in errs["errors"]] == ["rejected", "timeout"]
    assert 86000 < errs["expires_in_s"] <= 86400  # kept for a day
    assert not rdb.exists(f"psg:testprog:{run['id']}")

    assert admin.get("/api/v1/tests").json()[0]["summary"]["sent"] == 500
    assert admin.delete(f"/api/v1/tests/{run['id']}").status_code == 204
    assert not rdb.exists(f"psg:testerr:{run['id']}")
    actions = {a["action"] for a in admin.get("/api/v1/audit", params={"action": "test."}).json()}
    assert actions == {"test.start", "test.delete"}


def test_validation(admin, rdb, worker):
    heartbeat(admin)
    gw = listening_gateway(admin, rdb, max_binds=3, bind_types=["trx"])
    base = {"gateway_ids": [gw["id"]], "tps": 100, "duration_s": 5}
    for bad, word in (
        (base | {"sessions": 4}, "allows 3 binds"),
        (base | {"bind_type": "tx_rx", "sessions": 1}, "does not allow rx, tx"),
        (base | {"tester_id": 9}, "not online"),
    ):
        r = admin.post("/api/v1/tests", json=bad)
        assert r.status_code == 422 and word in r.json()["detail"], r.text
    off = listening_gateway(admin, rdb, 2)
    rdb.hdel("psg:gwstatus:1", str(off["id"]))
    r = admin.post("/api/v1/tests", json=base | {"gateway_ids": [off["id"]]})
    assert r.status_code == 422 and "not listening" in r.json()["detail"]
    for bad in (
        base | {"tps": 0},
        base | {"duration_s": 7201},
        base | {"net_delay": {"min_ms": 50, "max_ms": 10}},
        base | {"ramp_up_s": 10},
        base | {"gateway_ids": []},
    ):
        assert admin.post("/api/v1/tests", json=bad).status_code == 422, bad

    r = admin.post("/api/v1/tests", json=base | {"tps": 5000, "sessions": 1}).json()
    assert "above the licence cap of 1000" in r["warnings"][0]  # free tier


def test_stop_and_drop_errors(admin, rdb, worker):
    heartbeat(admin)
    gw = listening_gateway(admin, rdb)
    run = admin.post(
        "/api/v1/tests",
        json={"gateway_ids": [gw["id"]], "tps": 10, "duration_s": 60, "keep_errors": False},
    ).json()
    admin.get("/internal/v1/testers/1/job", headers=AGENT)
    assert admin.post(f"/api/v1/tests/{run['id']}/stop").json()["stop_requested"]
    r = admin.post(f"/internal/v1/tests/{run['id']}/progress", json={"progress": {}}, headers=AGENT)
    assert r.json() == {"stop": True}  # the tester learns with its next progress report
    admin.post(f"/internal/v1/tests/{run['id']}/errors", json=[{"kind": "timeout"}], headers=AGENT)
    admin.post(
        f"/internal/v1/tests/{run['id']}/result",
        json={"result": {"sent": 1, "stopped": "stopped"}},
        headers=AGENT,
    )
    done = admin.get(f"/api/v1/tests/{run['id']}").json()
    assert done["status"] == "stopped"
    assert admin.get(f"/api/v1/tests/{run['id']}/errors").json()["errors"] == []  # not kept
    assert admin.post(f"/api/v1/tests/{run['id']}/stop").status_code == 409

    queued = admin.post(
        "/api/v1/tests", json={"gateway_ids": [gw["id"]], "tps": 10, "duration_s": 60}
    ).json()
    assert admin.post(f"/api/v1/tests/{queued['id']}/stop").json()["status"] == "stopped"


def test_silent_tester_fails_its_run(client, admin, rdb, worker):
    heartbeat(admin)
    gw = listening_gateway(admin, rdb)
    run = admin.post(
        "/api/v1/tests", json={"gateway_ids": [gw["id"]], "tps": 10, "duration_s": 60}
    ).json()
    admin.get("/internal/v1/testers/1/job", headers=AGENT)
    stores = client.app.state.stores
    with Session(stores.db) as db:
        db.get(TestRun, run["id"]).started_at = utcnow() - timedelta(minutes=2)
        db.commit()
        expire_stale(db, stores)
    r = admin.get(f"/api/v1/tests/{run['id']}").json()
    assert r["status"] == "failed" and "stopped reporting" in r["error"]


def test_tokens_are_separate(client):
    worker_token = {"Authorization": f"Bearer {INTERNAL_TOKEN}"}
    assert client.get("/internal/v1/testers/1/job", headers=worker_token).status_code == 401
    assert client.get("/internal/v1/workers/1/config", headers=AGENT).status_code == 401
    assert client.get("/internal/v1/testers/1/job").status_code == 401
    assert client.get("/api/v1/tests").status_code == 401  # admin API needs a login
