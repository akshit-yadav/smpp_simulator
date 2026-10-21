"""Background jobs run inside the control plane."""

import asyncio
import json
import logging

from redis.exceptions import RedisError
from sqlalchemy.exc import SQLAlchemyError
from sqlalchemy.orm import Session

from psg_control.api.workers import upsert_from_heartbeat
from psg_control.auth import utcnow
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


async def run(stores: Stores) -> None:
    failing = False
    while True:
        try:
            await asyncio.to_thread(sync_worker_registry, stores)
            if failing:
                log.info("worker registry sync recovered")
            failing = False
        except (RedisError, SQLAlchemyError) as exc:
            if not failing:
                log.warning("worker registry sync failing: %s", type(exc).__name__)
            failing = True
        await asyncio.sleep(REGISTRY_INTERVAL_S)
