"""Background jobs run inside the control plane: ClickHouse schema, worker
registry, and every 5 s the licence (gateway limit, TPS shares, clock)."""

import asyncio
import json
import logging

from redis.exceptions import RedisError
from sqlalchemy.exc import SQLAlchemyError
from sqlalchemy.orm import Session

from psg_control import clickhouse, licence
from psg_control.api.workers import upsert_from_heartbeat
from psg_control.auth import utcnow
from psg_control.settings import Settings
from psg_control.stores import Stores

log = logging.getLogger(__name__)

REGISTRY_INTERVAL_S = 5


def sync_worker_registry(stores: Stores) -> int:
    """Registers workers and refreshes their IP, port range and last_seen from
    their heartbeats. Returns the number of live workers."""
    keys = list(stores.redis.scan_iter(match="psg:worker:*:hb", count=100))
    beats = [json.loads(b) for b in stores.redis.mget(keys) if b] if keys else []
    now = utcnow()
    with Session(stores.db) as db:
        for beat in beats:
            if upsert_from_heartbeat(db, beat, now):
                log.info("worker %s registered (ip %s)", beat["worker_id"], beat.get("ip"))
        db.commit()
    return len(beats)


async def run(stores: Stores, ticker: licence.Ticker, settings: Settings) -> None:
    failing = False
    schema_ready, schema_warned = False, False
    while True:
        if not schema_ready:
            # Workers spill events to disk until the tables exist, so keep trying.
            try:
                await asyncio.to_thread(clickhouse.migrate, stores.clickhouse)
                schema_ready = True
            except clickhouse.ClickHouseError as exc:
                if not schema_warned:
                    log.warning("clickhouse schema not applied yet, retrying: %s", exc)
                schema_warned = True
        try:
            await asyncio.to_thread(sync_worker_registry, stores)
            await asyncio.to_thread(licence.tick, stores, ticker, settings.licence_require_both)
            if failing:
                log.info("worker registry and licence checks recovered")
            failing = False
        except (RedisError, SQLAlchemyError) as exc:
            if not failing:
                log.warning("worker registry and licence checks failing: %s", type(exc).__name__)
            failing = True
        await asyncio.sleep(REGISTRY_INTERVAL_S)
