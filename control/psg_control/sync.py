"""Config sync to workers and worker commands over Redis.

1. The API writes MariaDB and bumps the affected workers' config_version.
2. After commit, it publishes {gateway_id, version, action} on psg:cfg:<worker_id>.
3. The worker pulls its full config from the internal API (and re-checks the
   version every 60 s, so a lost publish only delays a change).
"""

import json
import logging
import uuid
from dataclasses import dataclass, field

from redis.exceptions import RedisError
from sqlalchemy import select, update
from sqlalchemy.orm import Session

from psg_control.models import Worker
from psg_control.stores import Stores

log = logging.getLogger(__name__)


def cfg_channel(worker_id: int) -> str:
    return f"psg:cfg:{worker_id}"


def cmd_channel(worker_id: int) -> str:
    return f"psg:cmd:{worker_id}"


def heartbeat_key(worker_id: int) -> str:
    return f"psg:worker:{worker_id}:hb"


def gwstatus_key(worker_id: int) -> str:
    return f"psg:gwstatus:{worker_id}"


@dataclass
class Changes:
    """Config changes collected during a request, published after commit."""

    events: list[tuple[int, int | None, str]] = field(default_factory=list)
    versions: dict[int, int] = field(default_factory=dict)

    def bump(self, db: Session, worker_ids: set[int] | None, gateway_id: int | None, action: str):
        """Bumps config_version for the given workers (None = all) in this transaction."""
        stmt = update(Worker).values(config_version=Worker.config_version + 1)
        if worker_ids is not None:
            if not worker_ids:
                return
            stmt = stmt.where(Worker.id.in_(worker_ids))
        db.execute(stmt)
        q = select(Worker.id, Worker.config_version)
        if worker_ids is not None:
            q = q.where(Worker.id.in_(worker_ids))
        for wid, version in db.execute(q):
            self.versions[wid] = version
            self.events.append((wid, gateway_id, action))

    def publish(self, stores: Stores) -> None:
        for wid, gateway_id, action in self.events:
            msg = json.dumps(
                {"gateway_id": gateway_id, "version": self.versions[wid], "action": action}
            )
            try:
                stores.redis.publish(cfg_channel(wid), msg)
            except RedisError as exc:
                # Workers re-check their version every 60 s, so this only delays the change.
                log.warning("config publish to worker %s failed: %s", wid, type(exc).__name__)


def worker_online(stores: Stores, worker_id: int) -> bool:
    try:
        return bool(stores.redis.exists(heartbeat_key(worker_id)))
    except RedisError:
        return False


def command(stores: Stores, worker_id: int, cmd: dict, timeout: float) -> tuple[bool | None, str]:
    """Sends a command to a worker and waits for its reply. Returns (True, ""),
    (False, reason), or (None, reason) if the worker did not answer."""
    rid = uuid.uuid4().hex
    try:
        receivers = stores.redis.publish(cmd_channel(worker_id), json.dumps(cmd | {"id": rid}))
        if receivers == 0:
            return None, "worker is offline"
        reply = stores.redis.blpop([f"psg:reply:{rid}"], timeout=timeout)
    except RedisError as exc:
        return None, f"redis unavailable ({type(exc).__name__})"
    if reply is None:
        return None, "worker did not answer"
    data = json.loads(reply[1])
    return bool(data.get("ok")), data.get("error", "")


def port_check(
    stores: Stores, worker_id: int, port: int, timeout: float
) -> tuple[bool | None, str]:
    """Asks the worker to test-bind a port."""
    return command(stores, worker_id, {"type": "port_check", "port": port}, timeout)


def live_json(stores: Stores, key: str):
    try:
        raw = stores.redis.get(key)
    except RedisError:
        return None
    return json.loads(raw) if raw else None


def runtime_status(stores: Stores, worker_id: int) -> dict[int, dict]:
    """Per-gateway listener state as reported by the worker."""
    try:
        raw = stores.redis.hgetall(gwstatus_key(worker_id))
    except RedisError:
        return {}
    return {int(k): json.loads(v) for k, v in raw.items()}


def forget_gateway(stores: Stores, gateway_id: int) -> None:
    """Drops a deleted gateway's persisted DLR queue (best effort)."""
    try:
        stores.redis.unlink(f"psg:dlrh:{gateway_id}")
    except RedisError as exc:
        log.warning("could not delete DLR queue of gateway %s: %s", gateway_id, type(exc).__name__)
