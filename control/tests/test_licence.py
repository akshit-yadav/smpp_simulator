import base64
import json
from datetime import UTC, datetime, timedelta

import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.hazmat.primitives.serialization import Encoding, PublicFormat

from psg_control import licence
from psg_control.models import Gateway, LicenceRow
from tests.conftest import INTERNAL_TOKEN, needs_stores

pytestmark = needs_stores

GW = "/api/v1/gateways"
KEY = Ed25519PrivateKey.generate()


@pytest.fixture(autouse=True)
def test_key(monkeypatch):
    """Licences in these tests are signed with a throwaway key."""
    pub = KEY.public_key().public_bytes(Encoding.Raw, PublicFormat.Raw)
    monkeypatch.setattr(licence, "PUBLIC_KEY", base64.b64encode(pub).decode())


def make(key=KEY, **over) -> str:
    now = datetime.now(UTC)
    fields = {
        "licence_id": "HWPL-T-1",
        "licensee": "Test Co",
        "issued_at": (now - timedelta(days=1)).isoformat(),
        "expires_at": (now + timedelta(days=365)).isoformat(),
        "max_gateways": 12,
        "max_tps": 30000,
        "machines": [{"machine_id": "mid-1", "mac": "mac-1", "label": "worker-1"}],
        "features": [],
    } | over
    payload = json.dumps(fields).encode()
    return json.dumps(
        {
            "format": "psg-licence-1",
            "payload": base64.b64encode(payload).decode(),
            "signature": base64.b64encode(key.sign(payload)).decode(),
        }
    )


def new_gw(i: int) -> dict:
    return {"name": f"g{i}", "port": 20000 + i, "system_id": f"s{i}", "password": "p"}


def internal(client) -> dict:
    return client.get(
        "/internal/v1/workers/1/config", headers={"Authorization": f"Bearer {INTERNAL_TOKEN}"}
    ).json()


def tick(client):
    licence.tick(client.app.state.stores, client.app.state.licence_ticker)


def statuses(client) -> dict:
    return {g["name"]: g["status"] for g in client.get(GW).json()}


def test_free_tier_blocks_the_11th_gateway(admin, worker):
    s = admin.get("/api/v1/licence").json()
    assert (s["status"], s["max_gateways"], s["max_tps"]) == ("free", 10, 1000)
    assert s["workers"][0]["machine_id"] == "mid-1"
    for i in range(1, 11):
        assert admin.post(GW, json=new_gw(i)).status_code == 201
    r = admin.post(GW, json=new_gw(11))
    assert r.status_code == 409 and "allows 10 gateways (free tier)" in r.json()["detail"]


@pytest.mark.parametrize(
    ("text", "error"),
    [
        ("hello", "not a PSG licence"),
        (make(key=Ed25519PrivateKey.generate()), "signature is invalid"),
        (make(expires_at="2020-01-01T00:00:00Z"), "expired"),
        (make(machines=[{"machine_id": "x", "mac": "y"}]), "another machine"),
    ],
)
def test_bad_licences_are_refused(admin, worker, text, error):
    r = admin.put("/api/v1/licence", json={"licence": text})
    assert r.status_code == 422 and error in r.json()["detail"], r.text
    assert admin.get("/api/v1/licence").json()["status"] == "free"


def test_upload_suspend_keep_restore(admin, worker, rdb):
    version = internal(admin)["version"]
    text = make()
    s = admin.put("/api/v1/licence", json={"licence": text}).json()
    assert s["status"] == "licensed" and s["max_gateways"] == 12 and s["max_tps"] == 30000
    assert s["licence"]["licensee"] == "Test Co" and s["workers"][0]["match"] == "full"
    cfg = internal(admin)
    assert cfg["version"] > version and cfg["licence"] == {"file": text, "workers": 1}

    for i in range(1, 13):
        assert admin.post(GW, json=new_gw(i)).status_code == 201
    admin.patch(f"{GW}/12", json={"status": "disabled"})

    # Removing the licence: the free tier keeps the oldest 10.
    s = admin.delete("/api/v1/licence").json()
    assert s["status"] == "free" and [g["name"] for g in s["suspended"]] == ["g11", "g12"]
    st = statuses(admin)
    assert st["g10"] == "active" and st["g11"] == st["g12"] == "suspended"
    cfg_status = {g["name"]: g["status"] for g in internal(admin)["gateways"]}
    assert cfg_status["g11"] == "suspended"  # the worker stops it and unbinds its sessions
    assert admin.post(GW, json=new_gw(13)).status_code == 409
    r = admin.patch(f"{GW}/11", json={"status": "active"})
    assert r.status_code == 409 and "Licence page" in r.json()["detail"]

    # The admin picks g11 and g12 to stay: two of the oldest give way.
    keep = [11, 12, 1, 2, 3, 4, 5, 6, 7, 8]
    s = admin.put("/api/v1/licence/keep", json={"gateway_ids": keep}).json()
    st = statuses(admin)
    assert st["g11"] == "active" and st["g12"] == "disabled"  # back to what it was
    assert st["g9"] == st["g10"] == "suspended"
    assert (
        admin.put("/api/v1/licence/keep", json={"gateway_ids": list(range(1, 12))}).status_code
        == 422
    )

    # A new licence restores everything at once.
    s = admin.put("/api/v1/licence", json={"licence": make(licence_id="HWPL-T-2")}).json()
    assert s["suspended"] == [] and set(statuses(admin).values()) == {"active", "disabled"}

    actions = {a["action"] for a in admin.get("/api/v1/audit").json()}
    assert {
        "licence.upload",
        "licence.remove",
        "licence.keep",
        "licence.suspend",
        "licence.restore",
    } <= actions


def test_expiry_warning_clock_and_partial_match(admin, worker, client):
    admin.put(
        "/api/v1/licence",
        json={"licence": make(expires_at=(datetime.now(UTC) + timedelta(days=5)).isoformat())},
    )
    s = admin.get("/api/v1/licence").json()
    assert s["status"] == "licensed" and s["days_left"] == 4 and "expires in 4 days" in s["warning"]

    from sqlalchemy.orm import Session

    with Session(client.app.state.stores.db) as db:
        db.get(LicenceRow, 1).clock_high_water = datetime.now() + timedelta(days=2)
        db.commit()
    s = admin.get("/api/v1/licence").json()
    assert s["status"] == "expired" and "clock went back" in s["reason"] and s["max_tps"] == 1000

    with Session(client.app.state.stores.db) as db:
        db.get(LicenceRow, 1).clock_high_water = None
        db.commit()
    admin.put(
        "/api/v1/licence",
        json={"licence": make(machines=[{"machine_id": "mid-1", "mac": "other", "label": "w"}])},
    )
    s = admin.get("/api/v1/licence").json()
    assert s["status"] == "licensed" and "only partly match" in s["warning"]


def test_tps_shares(admin, worker, rdb):
    assert licence.shares(1000, {1: 0, 2: 0}) == {1: 500, 2: 500}
    assert licence.shares(1000, {1: 300, 2: 100}) == {1: 700, 2: 300}  # 100 floor each + demand
    assert licence.shares(1000, {}) == {}

    admin.put("/api/v1/licence", json={"licence": make(max_tps=20000)})
    tick(admin)
    share = json.loads(rdb.get("psg:licshare:1"))
    assert share["tps"] == 20000 and share["max_tps"] == 20000  # the only worker gets it all


def test_licence_request_export(admin, worker):
    r = admin.get("/api/v1/licence/request")
    assert "attachment" in r.headers["content-disposition"]
    body = r.json()
    assert body["format"] == "psg-licence-request-1"
    assert body["machines"] == [
        {"label": "worker-1", "ip": "10.0.0.1", "machine_id": "mid-1", "mac": "mac-1"}
    ]


def test_gateway_counts(client, admin, worker):
    from sqlalchemy.orm import Session

    for i in range(1, 4):
        admin.post(GW, json=new_gw(i))
    with Session(client.app.state.stores.db) as db:
        db.get(Gateway, 3).status = "suspended"
        db.commit()
        assert licence.licensed_count(db) == 2


def test_licence_signed_by_licgen_verifies_in_python():
    """tests/fixtures holds a licence signed by tools/licgen (Go) with a test key."""
    from pathlib import Path

    data = Path(__file__).parent / "fixtures"
    lic = licence.parse(
        (data / "licence-signed-by-go.json").read_text(),
        public_key=(data / "licence-test-key.pub").read_text().strip(),
    )
    assert (lic.licence_id, lic.max_gateways, lic.max_tps) == ("HWPL-TEST-FIXTURE", 7, 1234)
    assert lic.expires_at == datetime(2099, 1, 1, tzinfo=UTC) and lic.features == ["mo"]
    assert licence.match(lic, "mid-1", "mac-1") == "full"
    with pytest.raises(licence.LicenceError, match="signature"):
        licence.parse((data / "licence-signed-by-go.json").read_text())  # not PSG's real key


def test_install_from_file(admin, worker, tmp_path):
    stores = admin.app.state.stores
    assert licence.install_from_file(stores, str(tmp_path / "missing.psglic"), False) is None
    path = tmp_path / "licence.psglic"
    path.write_text(make(licence_id="HWPL-FILE"))
    assert licence.install_from_file(stores, str(path), False) == "HWPL-FILE"
    assert admin.get("/api/v1/licence").json()["licence"]["uploaded_by"] == "file"
    # Never over a licence that is already installed (e.g. a newer one from the panel).
    path.write_text(make(licence_id="HWPL-OLDER"))
    assert licence.install_from_file(stores, str(path), False) is None
    bad = tmp_path / "bad.psglic"
    bad.write_text("not a licence")
    admin.delete("/api/v1/licence")
    assert licence.install_from_file(stores, str(bad), False) is None
