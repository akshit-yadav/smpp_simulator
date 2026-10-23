from concurrent.futures import ThreadPoolExecutor

from tests.conftest import INTERNAL_TOKEN, needs_stores

pytestmark = needs_stores


def test_command_status_catalog(admin):
    codes = admin.get("/api/v1/catalog/command-statuses").json()
    assert len(codes) == 48 and codes[0]["name"] == "ESME_ROK"
    throttled = next(c for c in codes if c["code"] == 0x58)
    assert throttled["hex"] == "0x00000058" and throttled["system"]

    url = "/api/v1/catalog/command-statuses"
    assert admin.post(url, json={"code": 0x3FF, "name": "X"}).status_code == 422
    assert admin.post(url, json={"code": 0x500, "name": "X"}).status_code == 422
    assert admin.post(url, json={"code": 0x400, "name": "VENDOR_A"}).status_code == 201
    assert admin.post(url, json={"code": 0x400, "name": "VENDOR_A"}).status_code == 409
    assert admin.patch(f"{url}/88", json={"name": "NOPE"}).status_code == 409  # SMPP 3.4 read-only
    assert admin.patch(f"{url}/1024", json={"name": "VENDOR_B"}).json()["name"] == "VENDOR_B"
    assert admin.delete(f"{url}/1024").status_code == 204


def test_dlr_error_catalog(admin):
    url = "/api/v1/catalog/dlr-errors"
    errors = {e["code"]: e for e in admin.get(url).json()}
    assert errors["000"]["system"] and errors["001"]["network_error_code"] == 1
    assert not errors["901"]["system"] and errors["901"]["label"] == "Template Mismatch"

    body = {
        "code": "77",
        "label": "Spam filter",
        "suggested_status": "REJECTD",
        "network_error_type": 3,
        "network_error_code": 99,
        "dlr_text": "Blocked",
    }
    assert admin.post(url, json=body).status_code == 201
    assert "077" in {e["code"] for e in admin.get(url).json()}  # numeric, padded to 3 digits
    for bad in ("T02", "1234", "7a", ""):
        assert admin.post(url, json=body | {"code": bad}).status_code == 422, bad
    assert (
        admin.post(url, json=body | {"code": "Z98", "network_error_code": None}).status_code == 422
    )
    assert admin.patch(f"{url}/001", json={"label": "x"}).status_code == 409
    assert admin.delete(f"{url}/077").status_code == 204
    statuses = admin.get("/api/v1/catalog/dlr-statuses").json()
    assert {"status": "DELIVRD", "message_state": 2} in statuses


def rule(**over):
    body = {
        "name": "mix",
        "mode": "weighted",
        "entries": [
            {"name": "ok", "dlr_status": "DELIVRD", "weight_bp": 7000},
            {"name": "fail", "dlr_status": "UNDELIV", "dlr_error": "001", "weight_bp": 2500},
            {"name": "throttle", "command_status": 0x58, "weight_bp": 500},
        ],
    }
    return body | over


def test_rule_crud_and_validation(admin):
    url = "/api/v1/dlr-rules"
    bad = rule(entries=[{"dlr_status": "DELIVRD", "weight_bp": 5000}])
    r = admin.post(url, json=bad)
    assert r.status_code == 422 and "100%" in r.text
    assert admin.post(url, json=rule(entries=[{"command_status": 0}])).status_code == 422
    r = admin.post(
        url,
        json=rule(entries=[{"dlr_status": "DELIVRD", "dlr_error": "999", "weight_bp": 10000}]),
    )
    assert r.status_code == 422 and "999" in r.json()["detail"]
    # Shares are whole percentages.
    halves = [
        {"dlr_status": "DELIVRD", "weight_bp": 5050},
        {"dlr_status": "DELIVRD", "weight_bp": 4950},
    ]
    r = admin.post(url, json=rule(entries=halves))
    assert r.status_code == 422 and "50.5% must be a whole percentage" in r.text
    # An intermediate status as the rule's result would leave no final DLR.
    for interim in ("ENROUTE", "ACCEPTD"):
        r = admin.post(url, json=rule(entries=[{"dlr_status": interim, "weight_bp": 10000}]))
        assert r.status_code == 422 and "intermediate status" in r.text, interim

    p = admin.post(url, json=rule()).json()
    assert [e["name"] for e in p["entries"]] == ["ok", "fail", "throttle"]
    assert p["entries"][2]["dlr_status"] is None  # rejected outcomes carry no DLR fields
    assert admin.post(url, json=rule()).status_code == 409

    # Fixed mode uses exactly one entry, and has nothing to seed.
    r = admin.put(f"{url}/{p['id']}", json=rule(name="mix2", mode="fixed"))
    assert r.status_code == 422 and "exactly one entry" in r.text
    one = rule(name="mix2", mode="fixed", seed=5, entries=[{"dlr_status": "DELIVRD"}])
    updated = admin.put(f"{url}/{p['id']}", json=one).json()
    assert updated["mode"] == "fixed" and len(updated["entries"]) == 1 and updated["seed"] is None

    # Only weighted rules keep shares, so none come back stale on switching to weighted.
    rnd = admin.put(f"{url}/{p['id']}", json=rule(name="mix2", mode="random")).json()
    assert [e["weight_bp"] for e in rnd["entries"]] == [0, 0, 0]


def test_concurrent_rule_saves_never_merge_entries(admin):
    """Two admins saving one rule at once: the last save wins, whole. Its
    entries are never added to the other save's (weights totalling 200%)."""
    url = "/api/v1/dlr-rules"
    p = admin.post(url, json=rule()).json()
    bodies = [
        rule(
            entries=[{"dlr_status": "DELIVRD", "weight_bp": 10000 - 100 * i}]
            + [{"dlr_status": "UNDELIV", "dlr_error": "001", "weight_bp": 100 * i}]
        )
        for i in range(1, 9)
    ]
    with ThreadPoolExecutor(len(bodies)) as pool:
        codes = list(pool.map(lambda b: admin.put(f"{url}/{p['id']}", json=b).status_code, bodies))
    assert set(codes) == {200}, codes
    saved = admin.get(f"{url}/{p['id']}").json()
    assert len(saved["entries"]) == 2 and sum(e["weight_bp"] for e in saved["entries"]) == 10000


def test_rule_in_use_cannot_be_deleted(admin, worker):
    p = admin.post("/api/v1/dlr-rules", json=rule()).json()
    gw = admin.post(
        "/api/v1/gateways",
        json={
            "name": "g",
            "port": 20001,
            "system_id": "g",
            "password": "p",
            "dlr_rule_id": p["id"],
        },
    ).json()
    r = admin.delete(f"/api/v1/dlr-rules/{p['id']}")
    assert r.status_code == 409 and "g" in r.json()["detail"]
    assert admin.delete("/api/v1/dlr-rules/1").status_code == 409  # used by Gateway Defaults

    # Editing a rule reaches the worker.
    admin.put(f"/api/v1/dlr-rules/{p['id']}", json=rule(mode="random"))
    cfg = admin.get(
        "/internal/v1/workers/1/config", headers={"Authorization": f"Bearer {INTERNAL_TOKEN}"}
    ).json()
    assert cfg["gateways"][0]["outcome"]["mode"] == "random"
    admin.delete(f"/api/v1/gateways/{gw['id']}")
    assert admin.delete(f"/api/v1/dlr-rules/{p['id']}").status_code == 204


def test_preview(admin):
    exact = admin.post(
        "/api/v1/dlr-rules/preview", json={"rule": rule(distribution="exact")}
    ).json()
    assert [e["count"] for e in exact["entries"]] == [7000, 2500, 500]
    assert exact["accepted"] == 9500 and exact["dlr_status"] == {"DELIVRD": 7000, "UNDELIV": 2500}

    prob = admin.post(
        "/api/v1/dlr-rules/preview", json={"rule": rule(seed=7), "messages": 100000}
    ).json()
    for e in prob["entries"]:
        assert abs(e["actual_pct"] - e["configured_pct"]) < 0.5
    again = admin.post(
        "/api/v1/dlr-rules/preview", json={"rule": rule(seed=7), "messages": 100000}
    ).json()
    assert again == prob  # the seed makes it repeatable

    seeded = admin.post("/api/v1/dlr-rules/preview", json={"rule_id": 2}).json()
    assert seeded["messages"] == 10000


def test_internal_api_auth(client):
    url = "/internal/v1/workers/9/config"
    assert client.get(url).status_code == 401
    assert client.get(url, headers={"Authorization": "Bearer nope"}).status_code == 401
    r = client.get(url, headers={"Authorization": f"Bearer {INTERNAL_TOKEN}"})
    assert r.json() == {"worker_id": 9, "version": 0, "gateways": []}


def test_worker_registry(admin, worker):
    w = admin.get("/api/v1/workers").json()[0]
    assert w["id"] == 1 and w["ip"] == "10.0.0.1" and w["online"]
    assert (w["port_min"], w["port_max"], w["port_exclude"]) == (20000, 20099, [20050])
    assert admin.patch("/api/v1/workers/1", json={"name": "lab-1"}).json()["name"] == "lab-1"
    assert admin.delete("/api/v1/workers/1").status_code == 409  # online
