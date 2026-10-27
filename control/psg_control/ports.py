"""Three-layer port validation for gateways.

1. The port is inside the worker's range and not excluded.
2. A Redis SET NX lock (against concurrent requests) plus the MariaDB
   UNIQUE(worker_id, port) constraint.
3. The worker test-binds the port on its host.
"""

from collections.abc import Iterator
from contextlib import contextmanager

from fastapi import HTTPException
from redis.exceptions import RedisError
from sqlalchemy import select
from sqlalchemy.orm import Session

from psg_control import sync
from psg_control.models import Gateway, Worker
from psg_control.stores import Stores

LOCK_TTL_S = 30


def in_use_message(port: int) -> str:
    return f"Port {port} is already in use. Please choose another port."


def used_ports(db: Session, worker_id: int, exclude_gateway_id: int | None = None) -> set[int]:
    q = select(Gateway.port).where(Gateway.worker_id == worker_id)
    if exclude_gateway_id is not None:
        q = q.where(Gateway.id != exclude_gateway_id)
    return set(db.scalars(q))


def next_free_port(
    db: Session, worker: Worker, start: int | None = None, exclude_gateway_id: int | None = None
) -> int | None:
    """First port at or after start (wrapping) that is in range, not excluded and unused."""
    taken = used_ports(db, worker.id, exclude_gateway_id) | set(worker.port_exclude or [])
    lo, hi = worker.port_min, worker.port_max
    start = min(max(start or lo, lo), hi)
    for port in [*range(start, hi + 1), *range(lo, start)]:
        if port not in taken:
            return port
    return None


def conflict(db: Session, worker: Worker, port: int, gateway_id: int | None) -> HTTPException:
    return HTTPException(
        status_code=409,
        detail={
            "message": in_use_message(port),
            "suggested_port": next_free_port(db, worker, port + 1, gateway_id),
        },
    )


def check_range(db: Session, worker: Worker, port: int, gateway_id: int | None) -> None:
    """Layer 1."""
    reason = None
    if not worker.port_min <= port <= worker.port_max:
        reason = (
            f"Port {port} is outside {worker.name}'s range {worker.port_min}-{worker.port_max}."
        )
    elif port in (worker.port_exclude or []):
        reason = f"Port {port} is excluded on {worker.name}."
    if reason:
        raise HTTPException(
            status_code=422,
            detail={
                "message": reason,
                "suggested_port": next_free_port(db, worker, port, gateway_id),
            },
        )


@contextmanager
def port_lock(
    stores: Stores, worker_id: int, port: int, on_conflict: HTTPException
) -> Iterator[None]:
    """Layer 2a: a short Redis lock so two requests cannot claim one port at once.
    If Redis is down the MariaDB unique constraint still protects the port."""
    key = f"psg:portlock:{worker_id}:{port}"
    redis_ok = True
    try:
        locked = bool(stores.redis.set(key, "1", nx=True, ex=LOCK_TTL_S))  # None if held
    except RedisError:
        locked, redis_ok = False, False
    if redis_ok and not locked:
        raise on_conflict
    try:
        yield
    finally:
        if locked:
            try:
                stores.redis.delete(key)
            except RedisError:
                pass


def validate(
    db: Session, stores: Stores, worker: Worker, port: int, gateway_id: int | None, timeout: float
) -> list[str]:
    """Layers 1, 2b and 3. Call inside port_lock. Returns warnings (e.g. the
    worker could not verify the port)."""
    check_range(db, worker, port, gateway_id)
    if port in used_ports(db, worker.id, gateway_id):
        raise conflict(db, worker, port, gateway_id)
    ok, reason = sync.port_check(stores, worker.id, port, timeout)
    if ok is False:
        raise conflict(db, worker, port, gateway_id)
    if ok is None:
        return [f"Port {port} could not be verified on {worker.name}: {reason}."]
    return []
