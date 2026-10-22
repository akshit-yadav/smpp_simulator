import json

from tests.conftest import INTERNAL_TOKEN, needs_stores

pytestmark = needs_stores

GW = "/api/v1/gateways"


def new_gw(**over):
    return {"name": "vendor-a", "port": 20001, "system_id": "vendora", "password": "secret1"} | over


def mo_settings(**over):
    return {
        "mode": "rate",
        "rate": 50,
        "stop_after_count": 1000,
        "sources": {"kind": "range", "range_from": "0919800000000", "range_to": "0919800009999"},
        "destinations": ["56161", "56162"],
        "templates": [
            {"text": "STOP {rand:4}", "weight": 3},
            {"text": "{keyword} {seq}", "weight": 1},
        ],
        "keywords": ["HELP"],
        "encoding": "gsm7",
        "long_pct": 10,
        "no_receiver": "drop",
    } | over


def worker_mo(client):
    r = client.get(
        "/internal/v1/workers/1/config", headers={"Authorization": f"Bearer {INTERNAL_TOKEN}"}
    )
    return r.json()["gateways"][0].get("mo")


def test_mo_settings_start_stop(admin, worker, rdb):
    gid = admin.post(GW, json=new_gw()).json()["id"]
    url = f"{GW}/{gid}/mo"

    first = admin.get(url).json()
    assert first["configured"] is False and first["enabled"] is False
    assert worker_mo(admin) is None
    assert admin.post(f"{url}/start").status_code == 409  # nothing saved yet

    for bad in (
        mo_settings(sources={"kind": "range", "range_from": "9", "range_to": "1"}),
        mo_settings(sources={"kind": "prefix", "prefix": "9198"}),
        mo_settings(sources={"kind": "list"}),
        mo_settings(destinations=[]),
        mo_settings(templates=[]),
        mo_settings(rate=0),
        mo_settings(long_pct=101),
    ):
        assert admin.put(url, json=bad).status_code == 422, bad

    saved = admin.put(url, json=mo_settings(start_at="2026-10-22T10:00:00+05:30")).json()
    assert saved["configured"] and not saved["enabled"] and saved["run_id"] == 0
    mo = worker_mo(admin)
    assert mo["sources"] == {
        "kind": "range",
        "list": [],
        "from": "0919800000000",
        "to": "0919800009999",
        "prefix": "",
        "digits": 0,
    }
    assert mo["start_at"] == "2026-10-22T04:30:00Z"  # stored and sent as UTC
    assert mo["templates"][0] == {"text": "STOP {rand:4}", "weight": 3}
    assert (mo["enabled"], mo["no_receiver"], mo["stop_after_count"]) == (False, "drop", 1000)

    started = admin.post(f"{url}/start").json()
    assert started["enabled"] and started["run_id"] == 1
    assert worker_mo(admin)["run_id"] == 1
    assert admin.post(f"{url}/stop").json()["enabled"] is False
    assert admin.post(f"{url}/stop").status_code == 409
    assert admin.post(f"{url}/start").json()["run_id"] == 2  # every start is a new run

    rdb.set(
        "psg:stats:1",
        json.dumps(
            {
                "ts": 1,
                "gateways": {
                    str(gid): {
                        "mo": {
                            "state": "running",
                            "run_id": 2,
                            "run_sent": 40,
                            "sent": 140,
                            "acknowledged": 139,
                            "retried": 0,
                            "failed": 0,
                            "queued": 0,
                            "expired": 0,
                            "dropped": 1,
                        }
                    }
                },
            }
        ),
    )
    status = admin.get(url).json()["status"]
    assert status["state"] == "running" and status["run_sent"] == 40

    actions = [a["action"] for a in admin.get("/api/v1/audit", params={"action": "mo."}).json()]
    assert actions == ["mo.start", "mo.stop", "mo.start", "mo.update"]

    admin.patch(f"{GW}/{gid}", json={"status": "disabled"})
    assert admin.post(f"{url}/start").status_code == 409  # gateway not active

    rdb.hset(f"psg:moprog:{gid}", "run", 2)
    assert admin.delete(f"{GW}/{gid}").status_code == 204
    assert not rdb.exists(f"psg:moprog:{gid}")


def test_send_test_mo(admin, worker):
    gid = admin.post(GW, json=new_gw()).json()["id"]
    url = f"{GW}/{gid}/mo/test"
    body = {"source": "919800000001", "destination": "56161", "text": "HELLO", "encoding": "ucs2"}
    r = admin.post(url, json=body)
    assert r.status_code == 200 and r.json() == {"message_id": "abc123"}
    assert worker.mos[-1] | {"id": None} == {"type": "send_mo", "gateway": gid, "id": None} | body

    r = admin.post(url, json=body | {"text": "no receiver"})
    assert r.status_code == 409 and "no RX or TRX session" in r.json()["detail"]
    assert admin.post(url, json=body | {"source": "abc"}).status_code == 422
