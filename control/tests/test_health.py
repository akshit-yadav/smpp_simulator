from fastapi.testclient import TestClient

from fsg_control.main import create_app
from fsg_control.settings import Settings


def make_client() -> TestClient:
    # Nothing listens on port 1, so every store check fails fast.
    settings = Settings(mariadb_port=1, redis_port=1, clickhouse_http_port=1)
    return TestClient(create_app(settings))


def test_healthz():
    with make_client() as client:
        resp = client.get("/healthz")
    assert resp.status_code == 200
    assert resp.json() == {"status": "ok"}


def test_readyz_reports_each_store_down():
    with make_client() as client:
        resp = client.get("/readyz")
    assert resp.status_code == 503
    body = resp.json()
    assert body["status"] == "degraded"
    assert set(body["checks"]) == {"mariadb", "redis", "clickhouse"}
    assert all(v.startswith("down") for v in body["checks"].values())


def test_workers_returns_503_when_redis_down():
    with make_client() as client:
        resp = client.get("/api/v1/workers")
    assert resp.status_code == 503
