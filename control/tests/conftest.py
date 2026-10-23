"""API tests run against real MariaDB and Redis, and ClickHouse for reports:

    PSG_TEST_MARIADB_URL=mysql+pymysql://psg:psgtest@127.0.0.1:13399
    PSG_TEST_REDIS_ADDR=127.0.0.1:16399
    PSG_TEST_CLICKHOUSE_URL=http://127.0.0.1:18199

Each test gets a freshly migrated database (created and dropped per session),
a flushed Redis database 14 and, if it asks for `ch`, its own ClickHouse
database.
"""

import base64
import json
import os
import secrets
import threading
from pathlib import Path
from urllib.parse import urlsplit

import pytest
import redis
from alembic import command
from alembic.config import Config
from fastapi.testclient import TestClient
from sqlalchemy import create_engine, make_url, text

from psg_control.main import create_app
from psg_control.settings import Settings

MARIADB_URL = os.environ.get("PSG_TEST_MARIADB_URL")
REDIS_ADDR = os.environ.get("PSG_TEST_REDIS_ADDR")
CLICKHOUSE_URL = os.environ.get("PSG_TEST_CLICKHOUSE_URL")
ADMIN_PASSWORD = "correct-horse-battery"
INTERNAL_TOKEN = "test-internal-token"
TESTER_TOKEN = "test-tester-token"
REDIS_DB = 14

needs_stores = pytest.mark.skipif(
    not (MARIADB_URL and REDIS_ADDR), reason="PSG_TEST_MARIADB_URL / PSG_TEST_REDIS_ADDR not set"
)
needs_clickhouse = pytest.mark.skipif(
    not (MARIADB_URL and REDIS_ADDR and CLICKHOUSE_URL),
    reason="PSG_TEST_MARIADB_URL / PSG_TEST_REDIS_ADDR / PSG_TEST_CLICKHOUSE_URL not set",
)


@pytest.fixture(scope="session")
def server_engine():
    engine = create_engine(MARIADB_URL, isolation_level="AUTOCOMMIT")
    yield engine
    engine.dispose()


@pytest.fixture
def database(server_engine):
    """A fresh, migrated database for one test."""
    name = f"psg_test_{secrets.token_hex(4)}"
    with server_engine.connect() as conn:
        conn.execute(text(f"CREATE DATABASE {name} CHARACTER SET utf8mb4"))
    url = make_url(MARIADB_URL).set(database=name)
    engine = create_engine(url)
    cfg = Config(str(Path(__file__).parents[1] / "alembic.ini"))
    cfg.set_main_option("script_location", str(Path(__file__).parents[1] / "migrations"))
    with engine.begin() as conn:
        cfg.attributes["connection"] = conn
        command.upgrade(cfg, "head")
    engine.dispose()
    yield url
    with server_engine.connect() as conn:
        conn.execute(text(f"DROP DATABASE {name}"))


@pytest.fixture
def rdb():
    host, port = REDIS_ADDR.split(":")
    r = redis.Redis(host=host, port=int(port), db=REDIS_DB, decode_responses=True)
    r.flushdb()
    yield r
    r.flushdb()
    r.close()


@pytest.fixture
def settings(database, rdb) -> Settings:
    host, port = REDIS_ADDR.split(":")
    ch = urlsplit(CLICKHOUSE_URL or "http://127.0.0.1:1")
    return Settings(
        clickhouse_host=ch.hostname,
        clickhouse_http_port=ch.port,
        clickhouse_database=database.database,  # created by the ch fixture
        mariadb_host=database.host,
        mariadb_port=database.port,
        mariadb_user=database.username,
        mariadb_password=database.password,
        mariadb_database=database.database,
        redis_host=host,
        redis_port=int(port),
        redis_db=REDIS_DB,
        secret_key=base64.b64encode(secrets.token_bytes(32)).decode(),
        internal_token=INTERNAL_TOKEN,
        tester_token=TESTER_TOKEN,
        admin_username="admin",
        admin_password=ADMIN_PASSWORD,
        port_check_timeout_s=1.0,
        live_cache_s=0,  # tests change worker data between polls
        lockout_minutes=5,
    )


@pytest.fixture
def client(settings):
    with TestClient(create_app(settings, run_scheduler=False)) as c:
        yield c


@pytest.fixture
def ch(client):
    """The test's ClickHouse database, with the event schema applied."""
    from psg_control.clickhouse import migrate

    stores = client.app.state.stores
    migrate(stores.clickhouse)
    yield stores.clickhouse
    stores.clickhouse.execute(
        f"DROP DATABASE IF EXISTS `{stores.clickhouse.database}`", database=False
    )


@pytest.fixture
def admin(client):
    """A client logged in as the admin."""
    r = client.post("/api/v1/auth/login", json={"username": "admin", "password": ADMIN_PASSWORD})
    assert r.status_code == 200, r.text
    return client


class FakeWorker:
    """Stands in for psg-worker: heartbeat, port_check replies, config events."""

    def __init__(self, rdb, worker_id: int, busy_ports=(), exclude=(20050,)):
        self.rdb, self.id, self.busy = rdb, worker_id, set(busy_ports)
        self.events: list[dict] = []
        self.killed: list[int] = []
        self.mos: list[dict] = []
        rdb.set(
            f"psg:worker:{worker_id}:hb",
            json.dumps(
                {
                    "worker_id": worker_id,
                    "ip": f"10.0.0.{worker_id}",
                    "version": "test",
                    "port_range": "20000-20099",
                    "port_exclude": list(exclude),
                    "machine_id": f"mid-{worker_id}",
                    "mac": f"mac-{worker_id}",
                }
            ),
        )
        self.pubsub = rdb.pubsub()
        self.pubsub.subscribe(f"psg:cmd:{worker_id}", f"psg:cfg:{worker_id}")
        self.thread = threading.Thread(target=self._loop, daemon=True)
        self.thread.start()

    def _loop(self):
        try:
            self._serve()
        except (ValueError, redis.ConnectionError):
            pass  # stop() closed the connection

    def _serve(self):
        for msg in self.pubsub.listen():
            if msg["type"] != "message":
                continue
            data = json.loads(msg["data"])
            if msg["channel"].startswith("psg:cfg:"):
                self.events.append(data)
            elif data.get("type") == "port_check":
                self._reply(data["id"], data["port"] not in self.busy, "address in use")
            elif data.get("type") == "kill_session":
                self.killed.append(data["session"])
                self._reply(data["id"], data["session"] == 5, "session not found")
            elif data.get("type") == "send_mo":
                self.mos.append(data)
                ok = data["text"] != "no receiver"
                self._reply(
                    data["id"], ok, "no RX or TRX session is bound to this gateway", "abc123"
                )

    def _reply(self, rid, ok, error, message_id=None):
        key = f"psg:reply:{rid}"
        reply = {"ok": ok, "error": "" if ok else error}
        if ok and message_id:
            reply["message_id"] = message_id
        self.rdb.rpush(key, json.dumps(reply))
        self.rdb.expire(key, 30)

    def stop(self):
        self.pubsub.close()


@pytest.fixture
def worker(rdb, client):
    """Worker 1, registered the way the scheduler does it."""
    from psg_control.scheduler import sync_worker_registry

    w = FakeWorker(rdb, 1)
    sync_worker_registry(client.app.state.stores)
    yield w
    w.stop()
