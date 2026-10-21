"""Live views for the panel: counters and sessions published by workers
every second (Redis keys fsg:stats:<worker_id> and fsg:sessions:<worker_id>)."""

from fastapi import APIRouter, HTTPException, Request
from sqlalchemy import select

from fsg_control import audit, sync
from fsg_control.api.deps import DB, Admin, StoresDep
from fsg_control.auth import utcnow
from fsg_control.models import Gateway, Worker

router = APIRouter(tags=["live"])


@router.get("/live")
def live(db: DB, admin: Admin, stores: StoresDep):
    """Cumulative counters per gateway, plus each worker's DLR backlog.
    Poll it and diff consecutive answers to get rates."""
    workers, gateways = [], {}
    for w in db.scalars(select(Worker).order_by(Worker.id)):
        stats = sync.live_json(stores, f"fsg:stats:{w.id}") or {}
        workers.append(
            {
                "id": w.id,
                "name": w.name,
                "online": sync.worker_online(stores, w.id),
                "ts": stats.get("ts"),
                "dlr_pending": stats.get("dlr_pending", 0),
                "dlr_queued": stats.get("dlr_queued", 0),
            }
        )
        for gid, snap in (stats.get("gateways") or {}).items():
            gateways[gid] = snap | {"ts": stats["ts"], "worker_id": w.id}
    return {"ts": int(utcnow().timestamp() * 1000), "workers": workers, "gateways": gateways}


def _gateway(db, gateway_id: int) -> Gateway:
    gw = db.get(Gateway, gateway_id)
    if gw is None:
        raise HTTPException(status_code=404, detail="Gateway not found.")
    return gw


@router.get("/gateways/{gateway_id}/sessions")
def gateway_sessions(gateway_id: int, db: DB, admin: Admin, stores: StoresDep):
    """Bound SMPP sessions of a gateway, as last reported by its worker."""
    gw = _gateway(db, gateway_id)
    sessions = sync.live_json(stores, f"fsg:sessions:{gw.worker_id}") or []
    return [s for s in sessions if s["gateway_id"] == gw.id]


@router.delete("/gateways/{gateway_id}/sessions/{session_id}", status_code=204)
def kill_session(
    gateway_id: int, session_id: int, request: Request, db: DB, admin: Admin, stores: StoresDep
):
    """Unbinds and disconnects one session."""
    gw = _gateway(db, gateway_id)
    ok, reason = sync.command(
        stores,
        gw.worker_id,
        {"type": "kill_session", "session": session_id},
        request.app.state.settings.port_check_timeout_s,
    )
    if ok is None:
        raise HTTPException(status_code=503, detail=f"Could not reach the worker: {reason}.")
    if not ok:
        raise HTTPException(status_code=404, detail="Session not found (already closed?).")
    audit.record(
        db, request, admin.username, "session.kill", "gateway", gw.id, {"session": session_id}
    )
    db.commit()
