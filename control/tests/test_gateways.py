import time

from tests.conftest import INTERNAL_TOKEN, FakeWorker, needs_stores

pytestmark = needs_stores

GW = "/api/v1/gateways"


def new_gw(**over):
    body = {"name": "vendor-a", "port": 20001, "system_id": "vendora", "password": "secret1"}
    return body | over


def internal_config(client, worker_id=1):
    r = client.get(
        f"/internal/v1/workers/{worker_id}/config",
        headers={"Authorization": f"Bearer {INTERNAL_TOKEN}"},
    )
    assert r.status_code == 200, r.text
    return r.json()


def wait_events(worker, n):
    deadline = time.time() + 2
    while len(worker.events) < n and time.time() < deadline:
        time.sleep(0.01)
    return worker.events


def test_create_gateway_end_to_end(admin, worker):
    r = admin.post(GW, json=new_gw())
    assert r.status_code == 201, r.text
    gw = r.json()
    assert gw["ip"] == "10.0.0.1" and gw["worker_id"] == 1  # filled in from the worker
    assert gw["password"] == "********" and gw["warnings"] == []
    assert gw["settings"]["max_binds"] is None  # follows the default
    assert gw["effective"]["max_binds"] == 10 and gw["effective"]["dlr_timing"] == "random"

    events = wait_events(worker, 1)
    assert events[0]["gateway_id"] == gw["id"] and events[0]["action"] == "create"

    cfg = internal_config(admin)
    assert cfg["version"] == events[0]["version"]
    g = cfg["gateways"][0]
    assert g["password"] == "secret1" and g["system_id"] == "vendora" and g["port"] == 20001
    assert g["dlr"] == {
        "enabled": True,
        "timing": "random",
        "delay_ms": 1000,
        "min_ms": 500,
        "max_ms": 2000,
        "id_format": "hex",
        "date_seconds": False,
        "include_text": True,
        "include_tlvs": True,
    }
    assert (
        g["outcome"]["mode"] == "fixed" and g["outcome"]["outcomes"][0]["dlr_status"] == "DELIVRD"
    )

    audit = admin.get("/api/v1/audit", params={"action": "gateway.create"}).json()[0]
    assert audit["details"]["name"] == "vendor-a" and "password" not in audit["details"]
    assert admin.post(f"{GW}/{gw['id']}/reveal-password").json() == {"password": "secret1"}


def test_port_validation_layers(admin, worker):
    assert admin.post(GW, json=new_gw()).status_code == 201

    # Layer 1: outside range / excluded, with a suggestion.
    r = admin.post(GW, json=new_gw(name="b", port=21000))
    assert r.status_code == 422 and "outside" in r.json()["detail"]["message"]
    r = admin.post(GW, json=new_gw(name="b", port=20050))
    assert r.status_code == 422 and "excluded" in r.json()["detail"]["message"]

    # Layer 2: already used by a gateway.
    r = admin.post(GW, json=new_gw(name="b"))
    assert r.status_code == 409
    assert r.json()["detail"] == {
        "message": "Port 20001 is already in use. Please choose another port.",
        "suggested_port": 20002,
    }

    # Layer 2: another request holds the port lock.
    admin.app.state.stores.redis.set("psg:portlock:1:20007", "1", ex=30)
    assert admin.post(GW, json=new_gw(name="b", port=20007)).status_code == 409

    # Layer 3: something else on the worker host uses the port.
    worker.busy.add(20003)
    r = admin.post(GW, json=new_gw(name="b", port=20003))
    assert r.status_code == 409 and r.json()["detail"]["suggested_port"] == 20004

    check = admin.get("/api/v1/ports/check", params={"worker_id": 1, "port": 20003}).json()
    assert check["ok"] is False and "already in use" in check["message"]
    nxt = admin.get("/api/v1/workers/1/ports/next-free", params={"start": 20001}).json()
    assert nxt["port"] == 20002 and nxt["used"] == 1


def test_offline_worker_gives_warning(admin, worker):
    worker.stop()
    time.sleep(0.1)
    r = admin.post(GW, json=new_gw())
    assert r.status_code == 201
    assert "could not be verified" in r.json()["warnings"][0]


def test_field_validation(admin, worker):
    for bad in (
        {"system_id": "abcdefghijklmnop"},
        {"system_id": "has space"},
        {"password": "123456789"},
        {"bind_types": ["tx", "tx"]},
        {"dlr_timing": "sometimes"},
        {"tps_limit": -1},
        {"unknown_field": 1},
    ):
        assert admin.post(GW, json=new_gw(**bad)).status_code == 422, bad
    r = admin.post(GW, json=new_gw(dlr_min_ms=5000))  # above the default max of 2000
    assert r.status_code == 422 and "DLR delay range" in r.json()["detail"]
    assert admin.post(GW, json=new_gw()).status_code == 201
    assert admin.post(GW, json=new_gw(port=20002)).status_code == 409  # name taken


def test_update_defaults_and_versions(admin, worker):
    gw = admin.post(GW, json=new_gw(max_binds=3)).json()
    assert gw["effective"]["max_binds"] == 3
    v1 = internal_config(admin)["version"]

    r = admin.patch(
        f"{GW}/{gw['id']}", json={"max_binds": None, "tps_limit": 50, "password": "newpw"}
    )
    assert r.status_code == 200
    g = r.json()
    assert g["settings"]["max_binds"] is None and g["effective"]["max_binds"] == 10
    assert g["effective"]["tps_limit"] == 50 and g["version"] == 2

    d = admin.get("/api/v1/defaults").json()
    d["max_binds"] = 25
    d.pop("updated_at")
    assert admin.put("/api/v1/defaults", json=d).status_code == 200
    assert admin.get(f"{GW}/{gw['id']}").json()["effective"]["max_binds"] == 25

    cfg = internal_config(admin)
    assert cfg["version"] > v1
    assert cfg["gateways"][0]["max_binds"] == 25 and cfg["gateways"][0]["password"] == "newpw"
    assert cfg["gateways"][0]["tps"] == {"limit": 50, "scope": "bind"}

    diff = admin.get("/api/v1/audit", params={"action": "gateway.update"}).json()[0]["details"]
    assert diff["max_binds"] == [3, None] and diff["password"] == "*****"


def test_custom_outcome_profile(admin, worker):
    custom = {
        "name": "ignored",
        "mode": "weighted",
        "distribution": "exact",
        "entries": [
            {"name": "ok", "dlr_status": "DELIVRD", "weight_bp": 9000},
            {"name": "template", "dlr_status": "REJECTD", "dlr_error": "T01", "weight_bp": 900},
            {"name": "vendor reject", "command_status": 0x401, "weight_bp": 100},
        ],
    }
    # Vendor code 0x401 must exist in the catalog first.
    r = admin.post(GW, json=new_gw(custom_outcome=custom))
    assert r.status_code == 422 and "0x401" in r.json()["detail"]
    assert (
        admin.post(
            "/api/v1/catalog/command-statuses", json={"code": 0x401, "name": "VENDOR_BLOCKED"}
        ).status_code
        == 201
    )

    gw = admin.post(GW, json=new_gw(custom_outcome=custom)).json()
    assert gw["custom_outcome"] is True
    out = internal_config(admin)["gateways"][0]["outcome"]
    assert out["distribution"] == "exact" and len(out["outcomes"]) == 3
    t01 = out["outcomes"][1]
    assert t01["dlr_error"] == "T01" and t01["dlr_text"] == "Template Mismatch"
    assert "dlr_status" not in out["outcomes"][2]

    # Custom profiles are hidden from the shared list and removed with the gateway.
    assert all(p["owner_gateway_id"] is None for p in admin.get("/api/v1/profiles").json())
    assert len(admin.get("/api/v1/profiles", params={"include_custom": True}).json()) == 3
    assert admin.delete(f"{GW}/{gw['id']}").status_code == 204
    assert len(admin.get("/api/v1/profiles", params={"include_custom": True}).json()) == 2


def test_move_disable_delete(admin, rdb, worker):
    w2 = FakeWorker(rdb, 2)
    from psg_control.scheduler import sync_worker_registry

    sync_worker_registry(admin.app.state.stores)
    gw = admin.post(GW, json=new_gw(worker_id=1)).json()

    r = admin.patch(f"{GW}/{gw['id']}", json={"worker_id": 2})
    assert r.status_code == 200 and r.json()["ip"] == "10.0.0.2"
    assert internal_config(admin, 1)["gateways"] == []
    assert internal_config(admin, 2)["gateways"][0]["id"] == gw["id"]

    assert (
        admin.patch(f"{GW}/{gw['id']}", json={"status": "disabled"}).json()["status"] == "disabled"
    )
    assert internal_config(admin, 2)["gateways"][0]["status"] == "disabled"

    rdb.xadd(f"psg:dlrq:{gw['id']}", {"j": "{}"})
    assert admin.delete(f"{GW}/{gw['id']}").status_code == 204
    assert internal_config(admin, 2)["gateways"] == []
    assert not rdb.exists(f"psg:dlrq:{gw['id']}")  # its queued DLRs go with it
    assert admin.get(f"{GW}/{gw['id']}").status_code == 404
    w2.stop()


def test_draining_worker_takes_no_new_gateways(admin, worker):
    assert admin.patch("/api/v1/workers/1", json={"status": "draining"}).status_code == 200
    r = admin.post(GW, json=new_gw(worker_id=1))
    assert r.status_code == 422 and "draining" in r.json()["detail"]


def test_runtime_status_from_worker(admin, rdb, worker):
    gw = admin.post(GW, json=new_gw()).json()
    rdb.hset("psg:gwstatus:1", str(gw["id"]), '{"state": "listening", "port": 20001}')
    assert admin.get(f"{GW}/{gw['id']}").json()["runtime"]["state"] == "listening"
