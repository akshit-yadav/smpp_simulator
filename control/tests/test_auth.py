import pyotp
import pytest

from tests.conftest import ADMIN_PASSWORD, needs_stores

pytestmark = needs_stores

LOGIN = "/api/v1/auth/login"


def login(client, password=ADMIN_PASSWORD, code=None):
    body = {"username": "admin", "password": password}
    if code:
        body["totp_code"] = code
    return client.post(LOGIN, json=body)


def test_login_logout_and_me(client):
    assert client.get("/api/v1/auth/me").status_code == 401
    assert login(client, "wrong").status_code == 401
    r = login(client)
    assert r.status_code == 200 and r.json() == {"username": "admin", "totp_enabled": False}
    cookie = r.headers["set-cookie"].lower()
    assert "httponly" in cookie and "samesite=strict" in cookie
    assert client.get("/api/v1/auth/me").json()["username"] == "admin"
    assert client.post("/api/v1/auth/logout").status_code == 204
    assert client.get("/api/v1/auth/me").status_code == 401


def test_everything_needs_login(client):
    for path in (
        "/api/v1/gateways",
        "/api/v1/workers",
        "/api/v1/dlr-rules",
        "/api/v1/audit",
        "/api/v1/defaults",
        "/api/v1/catalog/dlr-errors",
    ):
        assert client.get(path).status_code == 401, path


def test_lockout_after_repeated_failures(client):
    for _ in range(5):
        assert login(client, "wrong").status_code == 401
    r = login(client)
    assert r.status_code == 401 and "Too many" in r.json()["detail"]


def test_totp_enrolment_and_login(admin):
    r = admin.post("/api/v1/auth/totp/setup", json={"password": ADMIN_PASSWORD})
    assert r.status_code == 200
    totp = pyotp.TOTP(r.json()["secret"])
    assert r.json()["otpauth_uri"].startswith("otpauth://totp/")
    assert admin.post("/api/v1/auth/totp/enable", json={"code": "000000"}).status_code == 422
    assert admin.post("/api/v1/auth/totp/enable", json={"code": totp.now()}).json()["totp_enabled"]

    admin.post("/api/v1/auth/logout")
    r = login(admin)
    assert r.status_code == 401 and r.json()["totp_required"] is True
    code = totp.now()
    assert login(admin, code=code).status_code == 200
    # A code works only once.
    admin.post("/api/v1/auth/logout")
    assert login(admin, code=code).status_code == 401


def test_password_change_ends_other_sessions(client, settings):
    from fastapi.testclient import TestClient

    assert login(client).status_code == 200
    other = TestClient(client.app)
    assert login(other).status_code == 200
    r = client.post(
        "/api/v1/auth/password",
        json={"current_password": ADMIN_PASSWORD, "new_password": "a-much-better-one"},
    )
    assert r.status_code == 204
    assert client.get("/api/v1/auth/me").status_code == 200
    assert other.get("/api/v1/auth/me").status_code == 401
    assert login(other, "a-much-better-one").status_code == 200


@pytest.mark.parametrize("new", ["short"])
def test_password_policy(admin, new):
    r = admin.post(
        "/api/v1/auth/password", json={"current_password": ADMIN_PASSWORD, "new_password": new}
    )
    assert r.status_code == 422


def test_login_is_audited(admin):
    actions = [a["action"] for a in admin.get("/api/v1/audit").json()]
    assert "auth.login" in actions and "admin.bootstrap" in actions
