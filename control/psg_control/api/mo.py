"""MO generator per gateway (M7): settings, start/stop and "Send test MO".

Settings live in MariaDB and reach the worker with the gateway's config. Start
bumps run_id, so the worker starts a new run (counting from zero); a worker
restart in the middle of a run resumes it.
"""

from datetime import UTC

from fastapi import APIRouter, HTTPException, Request
from sqlalchemy.orm import Session

from psg_control import audit, sync
from psg_control.api.deps import DB, Admin, StoresDep
from psg_control.api.gateways import get_gateway
from psg_control.models import Gateway, MOGenerator
from psg_control.schemas import MOOut, MOSettings, MOStatus, MOTestIn
from psg_control.stores import Stores

router = APIRouter(tags=["mo"])

SUGGESTED = MOSettings(
    sources={"kind": "prefix", "prefix": "9198", "digits": 8},
    destinations=["56161"],
    templates=[{"text": "STOP {rand:4}", "weight": 1}],
)


def status(stores: Stores, gw: Gateway) -> MOStatus | None:
    stats = sync.live_json(stores, f"psg:stats:{gw.worker_id}") or {}
    mo = (stats.get("gateways") or {}).get(str(gw.id), {}).get("mo")
    return MOStatus(**mo) if mo else None


def out(stores: Stores, gw: Gateway, mo: MOGenerator | None) -> MOOut:
    if mo is None:
        return MOOut(**SUGGESTED.model_dump(), configured=False, enabled=False, run_id=0)
    settings = MOSettings(
        mode=mo.mode,
        rate=mo.rate,
        burst=mo.burst,
        start_at=mo.start_at,
        stop_after_s=mo.stop_after_s,
        stop_after_count=mo.stop_after_count,
        sources=mo.sources,
        destinations=mo.destinations,
        dest_order=mo.dest_order,
        templates=mo.templates,
        keywords=mo.keywords,
        encoding=mo.encoding,
        long_pct=mo.long_pct,
        no_receiver=mo.no_receiver,
    )
    return MOOut(
        **settings.model_dump(),
        configured=True,
        enabled=mo.enabled,
        run_id=mo.run_id,
        updated_at=mo.updated_at,
        status=status(stores, gw),
    )


def push(db: Session, stores: Stores, gw: Gateway, request: Request, admin, action: str, details):
    """Commits the change with its audit row and tells the worker."""
    changes = sync.Changes()
    changes.bump(db, {gw.worker_id}, gw.id, "update")
    audit.record(db, request, admin.username, action, "gateway", gw.id, details)
    db.commit()
    changes.publish(stores)


@router.get("/gateways/{gateway_id}/mo", response_model=MOOut)
def get_mo(gateway_id: int, db: DB, admin: Admin, stores: StoresDep):
    """The gateway's MO settings and live status. Never saved yet: suggested values."""
    gw = get_gateway(db, gateway_id)
    return out(stores, gw, db.get(MOGenerator, gw.id))


@router.put("/gateways/{gateway_id}/mo", response_model=MOOut)
def save_mo(
    gateway_id: int, body: MOSettings, request: Request, db: DB, admin: Admin, stores: StoresDep
):
    """Saves the settings. A running generator picks them up within a second."""
    gw = get_gateway(db, gateway_id)
    mo = db.get(MOGenerator, gw.id) or MOGenerator(gateway_id=gw.id, enabled=False, run_id=0)
    values = body.model_dump()
    if (t := body.start_at) is not None:  # stored as naive UTC; a naive input is UTC
        values["start_at"] = (t.astimezone(UTC) if t.tzinfo else t).replace(tzinfo=None)
    for k, v in values.items():
        setattr(mo, k, v)
    db.add(mo)
    push(db, stores, gw, request, admin, "mo.update", body.model_dump(mode="json"))
    return out(stores, gw, mo)


@router.post("/gateways/{gateway_id}/mo/start", response_model=MOOut)
def start_mo(gateway_id: int, request: Request, db: DB, admin: Admin, stores: StoresDep):
    """Starts a new run (from zero), at start_at if that is set and in the future.
    A duration limit counts from when the run actually starts."""
    gw = get_gateway(db, gateway_id)
    mo = db.get(MOGenerator, gw.id)
    if mo is None:
        raise HTTPException(status_code=409, detail="Save the MO settings first.")
    if gw.status != "active":
        raise HTTPException(status_code=409, detail=f"The gateway is {gw.status}; start it first.")
    mo.enabled, mo.run_id = True, mo.run_id + 1
    push(db, stores, gw, request, admin, "mo.start", {"run_id": mo.run_id})
    return out(stores, gw, mo)


@router.post("/gateways/{gateway_id}/mo/stop", response_model=MOOut)
def stop_mo(gateway_id: int, request: Request, db: DB, admin: Admin, stores: StoresDep):
    gw = get_gateway(db, gateway_id)
    mo = db.get(MOGenerator, gw.id)
    if mo is None or not mo.enabled:
        raise HTTPException(status_code=409, detail="The MO generator is not running.")
    mo.enabled = False
    push(db, stores, gw, request, admin, "mo.stop", {"run_id": mo.run_id})
    return out(stores, gw, mo)


@router.post("/gateways/{gateway_id}/mo/test")
def test_mo(
    gateway_id: int, body: MOTestIn, request: Request, db: DB, admin: Admin, stores: StoresDep
):
    """Sends one MO now to a bound RX/TRX session (never queued)."""
    gw = get_gateway(db, gateway_id)
    ok, reason, reply = sync.command_reply(
        stores,
        gw.worker_id,
        {"type": "send_mo", "gateway": gw.id} | body.model_dump(),
        request.app.state.settings.port_check_timeout_s,
    )
    if ok is None:
        raise HTTPException(status_code=503, detail=f"Could not reach the worker: {reason}.")
    if not ok:
        raise HTTPException(status_code=409, detail=f"Not sent: {reason}.")
    audit.record(db, request, admin.username, "mo.test", "gateway", gw.id, body.model_dump())
    db.commit()
    return {"message_id": reply.get("message_id", "")}
