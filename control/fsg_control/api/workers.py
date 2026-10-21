import json

from fastapi import APIRouter, HTTPException, Request
from redis.exceptions import RedisError

router = APIRouter(tags=["workers"])


@router.get("/workers")
def list_workers(request: Request) -> list[dict]:
    """Workers with a live heartbeat in Redis (key fsg:worker:<id>:hb)."""
    rdb = request.app.state.stores.redis
    try:
        keys = sorted(rdb.scan_iter(match="fsg:worker:*:hb", count=100))
        beats = rdb.mget(keys) if keys else []
    except RedisError as exc:
        raise HTTPException(status_code=503, detail="redis unavailable") from exc
    return sorted(
        (json.loads(b) for b in beats if b),
        key=lambda b: b["worker_id"],
    )
