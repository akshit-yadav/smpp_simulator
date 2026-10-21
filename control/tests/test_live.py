import json

from tests.conftest import needs_stores

pytestmark = needs_stores


def test_live_stats_and_sessions(admin, rdb, worker):
    gw = admin.post(
        "/api/v1/gateways",
        json={"name": "g", "port": 20001, "system_id": "g", "password": "p"},
    ).json()
    rdb.set(
        "psg:stats:1",
        json.dumps(
            {
                "ts": 1700000000000,
                "dlr_pending": 12,
                "dlr_queued": 3,
                "gateways": {
                    str(gw["id"]): {"accepted": 100, "throttled": 5, "sessions": {"trx": 2}}
                },
            }
        ),
    )
    rdb.set(
        "psg:sessions:1",
        json.dumps(
            [
                {
                    "id": 5,
                    "gateway_id": gw["id"],
                    "bind_type": "trx",
                    "system_id": "g",
                    "remote": "1.2.3.4:5",
                },
                {
                    "id": 6,
                    "gateway_id": 999,
                    "bind_type": "rx",
                    "system_id": "x",
                    "remote": "1.2.3.4:6",
                },
            ]
        ),
    )

    live = admin.get("/api/v1/live").json()
    assert live["workers"][0] | {"ts": None} == {
        "id": 1,
        "name": "worker-1",
        "online": True,
        "ts": None,
        "dlr_pending": 12,
        "dlr_queued": 3,
    }
    g = live["gateways"][str(gw["id"])]
    assert g["accepted"] == 100 and g["worker_id"] == 1 and g["ts"] == 1700000000000

    sessions = admin.get(f"/api/v1/gateways/{gw['id']}/sessions").json()
    assert [s["id"] for s in sessions] == [5]

    assert admin.delete(f"/api/v1/gateways/{gw['id']}/sessions/5").status_code == 204
    assert admin.delete(f"/api/v1/gateways/{gw['id']}/sessions/7").status_code == 404
    assert worker.killed == [5, 7]
    assert admin.get("/api/v1/audit", params={"action": "session.kill"}).json()[0]["details"] == {
        "session": 5
    }


def test_kill_session_worker_offline(admin, rdb, worker):
    gw = admin.post(
        "/api/v1/gateways",
        json={"name": "g", "port": 20001, "system_id": "g", "password": "p"},
    ).json()
    worker.stop()
    import time

    time.sleep(0.1)
    r = admin.delete(f"/api/v1/gateways/{gw['id']}/sessions/5")
    assert r.status_code == 503 and "offline" in r.json()["detail"]
