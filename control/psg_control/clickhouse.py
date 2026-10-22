"""ClickHouse over HTTP: the event schema (control/clickhouse/*.sql) and report queries.

Queries use server-side parameters ({name:Type} in the SQL, param_name in the
URL), so values are never pasted into SQL.
"""

import json
import logging
import re
from datetime import UTC, datetime
from pathlib import Path

import httpx

from psg_control.settings import Settings

log = logging.getLogger(__name__)

DDL_DIR = Path(__file__).resolve().parents[1] / "clickhouse"
QUERY_TIMEOUT_S = 30

SCHEMA_TABLE = """
CREATE TABLE IF NOT EXISTS psg_schema
(
    version    UInt32,
    name       String,
    applied_at DateTime('UTC') DEFAULT now()
)
ENGINE = MergeTree
ORDER BY version
"""


class ClickHouseError(Exception):
    """ClickHouse is unreachable or rejected a query."""


class ClickHouse:
    def __init__(self, settings: Settings) -> None:
        self.url = f"http://{settings.clickhouse_host}:{settings.clickhouse_http_port}/"
        self.database = settings.clickhouse_database
        self.headers = {"X-ClickHouse-User": settings.clickhouse_user}
        if password := settings.clickhouse_password.get_secret_value():
            self.headers["X-ClickHouse-Key"] = password
        self.client = httpx.Client(headers=self.headers, timeout=QUERY_TIMEOUT_S)

    def close(self) -> None:
        self.client.close()

    def execute(self, sql: str, params: dict | None = None, database: bool = True) -> str:
        query = {f"param_{k}": _param(v) for k, v in (params or {}).items()}
        if database:
            query["database"] = self.database
        try:
            resp = self.client.post(self.url, params=query, content=sql.encode())
        except httpx.HTTPError as exc:
            raise ClickHouseError(f"ClickHouse unreachable ({type(exc).__name__})") from exc
        if resp.status_code != 200:
            raise ClickHouseError(resp.text.strip()[:500])
        return resp.text

    def rows(self, sql: str, params: dict | None = None) -> list[dict]:
        """Runs a SELECT and returns its rows as dicts (64-bit integers as numbers;
        select message IDs with toString())."""
        out = self.execute(
            f"{sql}\nFORMAT JSON SETTINGS output_format_json_quote_64bit_integers = 0", params
        )
        return json.loads(out)["data"]


def _param(v) -> str:
    if isinstance(v, datetime):
        return v.astimezone(UTC).strftime("%Y-%m-%d %H:%M:%S.%f")[:-3]
    if isinstance(v, list | tuple):
        return "[" + ",".join(str(int(x)) for x in v) + "]"
    return str(v)


def ddl_files() -> list[tuple[int, Path]]:
    files = []
    for path in sorted(DDL_DIR.glob("*.sql")):
        m = re.match(r"(\d+)_", path.name)
        if m:
            files.append((int(m.group(1)), path))
    return files


def statements(sql: str) -> list[str]:
    sql = re.sub(r"--[^\n]*", "", sql)
    return [s.strip() for s in sql.split(";") if s.strip()]


def migrate(ch: ClickHouse) -> list[str]:
    """Creates the database if needed and applies DDL files newer than the
    recorded schema version. Returns the names of the files applied."""
    try:
        ch.execute(f"CREATE DATABASE IF NOT EXISTS `{ch.database}`", database=False)
    except ClickHouseError as exc:
        # The user may lack CREATE DATABASE while the database already exists.
        log.info("could not create ClickHouse database %s: %s", ch.database, exc)
    ch.execute(SCHEMA_TABLE)
    current = int(ch.execute("SELECT max(version) FROM psg_schema").strip() or 0)
    applied = []
    for version, path in ddl_files():
        if version <= current:
            continue
        for stmt in statements(path.read_text()):
            ch.execute(stmt)
        ch.execute(
            "INSERT INTO psg_schema (version, name) VALUES ({v:UInt32}, {n:String})",
            {"v": version, "n": path.name},
        )
        applied.append(path.name)
        log.info("applied ClickHouse schema %s", path.name)
    return applied
