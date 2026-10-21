"""Connections to MariaDB, Redis and ClickHouse, plus readiness checks."""

import logging

import httpx
import redis
from sqlalchemy import Engine, create_engine, text

from psg_control.settings import Settings

log = logging.getLogger(__name__)

CHECK_TIMEOUT_S = 2


class Stores:
    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.db: Engine = create_engine(
            settings.mariadb_url,
            pool_pre_ping=True,
            pool_recycle=3600,
            connect_args={"connect_timeout": CHECK_TIMEOUT_S},
        )
        self.redis = redis.Redis(
            host=settings.redis_host,
            port=settings.redis_port,
            password=settings.redis_password.get_secret_value() or None,
            db=settings.redis_db,
            socket_timeout=CHECK_TIMEOUT_S,
            socket_connect_timeout=CHECK_TIMEOUT_S,
            decode_responses=True,
        )
        self.clickhouse_url = f"http://{settings.clickhouse_host}:{settings.clickhouse_http_port}"

    def close(self) -> None:
        self.db.dispose()
        self.redis.close()

    def check_all(self) -> dict[str, str]:
        return {
            "mariadb": self._check("mariadb", self._check_mariadb),
            "redis": self._check("redis", self.redis.ping),
            "clickhouse": self._check("clickhouse", self._check_clickhouse),
        }

    def _check_mariadb(self) -> None:
        with self.db.connect() as conn:
            conn.execute(text("SELECT 1"))

    def _check_clickhouse(self) -> None:
        resp = httpx.get(f"{self.clickhouse_url}/ping", timeout=CHECK_TIMEOUT_S)
        resp.raise_for_status()

    @staticmethod
    def _check(name: str, fn) -> str:
        try:
            fn()
        except Exception as exc:  # noqa: BLE001 - report any failure as "down"
            log.warning("%s unreachable: %s", name, type(exc).__name__)
            return f"down: {type(exc).__name__}"
        return "ok"
