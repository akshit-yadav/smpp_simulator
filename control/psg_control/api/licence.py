"""Licence (M8): upload, remove, status, licence request, and which gateways
stay active when there are more than the licence allows."""

from datetime import UTC, datetime

from fastapi import APIRouter, HTTPException, Request
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field
from sqlalchemy import func, select

from psg_control import audit, licence, sync
from psg_control.api.deps import DB, Admin, StoresDep
from psg_control.models import Gateway, Worker

router = APIRouter(tags=["licence"])


class LicenceIn(BaseModel):
    licence: str = Field(min_length=1, max_length=100_000)  # the licence file's text


class KeepIn(BaseModel):
    gateway_ids: list[int] = Field(max_length=100_000)


def summary(db, stores, request: Request) -> dict:
    settings = request.app.state.settings
    state = licence.evaluate(db, stores, datetime.now(UTC), settings.licence_require_both)
    lic = state.licence
    r = licence.row(db)
    suspended = db.scalars(
        select(Gateway).where(Gateway.status == "suspended").order_by(Gateway.id)
    )
    return {
        "status": state.status,
        "reason": state.reason,
        "warning": state.warning,
        "days_left": state.days_left,
        "max_gateways": state.max_gateways,
        "max_tps": state.max_tps,
        "free_gateways": licence.FREE_GATEWAYS,
        "free_tps": licence.FREE_TPS,
        "require_both": settings.licence_require_both,
        "gateways_used": licence.licensed_count(db),
        "suspended": [{"id": g.id, "name": g.name} for g in suspended],
        "kept": list(db.scalars(select(Gateway.id).where(Gateway.licence_keep.is_(True)))),
        "workers": state.workers,
        "licence": None
        if lic is None
        else {
            "licence_id": lic.licence_id,
            "licensee": lic.licensee,
            "issued_at": lic.issued_at,
            "expires_at": lic.expires_at,
            "max_gateways": lic.max_gateways,
            "max_tps": lic.max_tps,
            "machines": lic.machines,
            "features": lic.features,
            "uploaded_at": r.uploaded_at,
            "uploaded_by": r.uploaded_by,
        },
    }


def apply_now(db, stores, request: Request) -> None:
    """Tells every worker (the licence travels with their config) and enforces
    the new limits at once instead of on the next scheduler tick."""
    changes = sync.Changes()
    changes.bump(db, None, None, "licence")
    db.commit()
    changes.publish(stores)
    licence.tick(
        stores, request.app.state.licence_ticker, request.app.state.settings.licence_require_both
    )


@router.get("/licence")
def get_licence(request: Request, db: DB, admin: Admin, stores: StoresDep):
    """Limits in force, the licence, workers' fingerprints and match, usage."""
    return summary(db, stores, request)


@router.put("/licence")
def upload_licence(body: LicenceIn, request: Request, db: DB, admin: Admin, stores: StoresDep):
    """Installs a licence. Refused if it is not signed by HWPL, has expired, or
    matches none of the registered workers."""
    now = datetime.now(UTC)
    try:
        lic = licence.check_upload(
            body.licence.strip(), db, stores, now, request.app.state.settings.licence_require_both
        )
    except licence.LicenceError as exc:
        raise HTTPException(status_code=422, detail=str(exc)) from exc
    r = licence.row(db)
    r.raw, r.uploaded_at, r.uploaded_by = (
        body.licence.strip(),
        now.replace(tzinfo=None),
        admin.username,
    )
    audit.record(
        db,
        request,
        admin.username,
        "licence.upload",
        "licence",
        lic.licence_id,
        {
            "licensee": lic.licensee,
            "expires_at": lic.expires_at.isoformat(),
            "max_gateways": lic.max_gateways,
            "max_tps": lic.max_tps,
        },
    )
    apply_now(db, stores, request)
    return summary(db, stores, request)


@router.delete("/licence")
def remove_licence(request: Request, db: DB, admin: Admin, stores: StoresDep):
    """Removes the licence: the free tier applies (gateways over it are suspended)."""
    r = licence.row(db)
    if not r.raw:
        raise HTTPException(status_code=404, detail="No licence is installed.")
    r.raw = r.uploaded_at = r.uploaded_by = None
    audit.record(db, request, admin.username, "licence.remove", "licence")
    apply_now(db, stores, request)
    return summary(db, stores, request)


@router.get("/licence/request")
def licence_request(db: DB, admin: Admin):
    """What HWPL needs to issue a licence: every worker's fingerprint."""
    workers = db.scalars(select(Worker).order_by(Worker.id))
    body = {
        "format": "psg-licence-request-1",
        "generated_at": datetime.now(UTC).isoformat(timespec="seconds"),
        "gateways": db.scalar(select(func.count()).select_from(Gateway)),
        "machines": [
            {"label": w.name, "ip": w.ip, "machine_id": w.machine_id, "mac": w.mac}
            for w in workers
            if w.machine_id or w.mac
        ],
    }
    return JSONResponse(
        body, headers={"Content-Disposition": 'attachment; filename="psg-licence-request.json"'}
    )


@router.put("/licence/keep")
def keep_gateways(body: KeepIn, request: Request, db: DB, admin: Admin, stores: StoresDep):
    """Picks the gateways that stay active when there are more than the
    licence allows; the others are suspended (and these restored) at once."""
    state = licence.evaluate(
        db, stores, datetime.now(UTC), request.app.state.settings.licence_require_both
    )
    ids = set(body.gateway_ids)
    if len(ids) > state.max_gateways:
        raise HTTPException(
            status_code=422,
            detail=f"Pick at most {state.max_gateways} gateways (the current limit).",
        )
    known = set(db.scalars(select(Gateway.id).where(Gateway.id.in_(ids))))
    if known != ids:
        raise HTTPException(status_code=422, detail=f"Unknown gateways: {sorted(ids - known)}.")
    for gw in db.scalars(select(Gateway)):
        gw.licence_keep = gw.id in ids
    audit.record(db, request, admin.username, "licence.keep", "gateway", None, {"ids": sorted(ids)})
    db.commit()
    licence.tick(
        stores, request.app.state.licence_ticker, request.app.state.settings.licence_require_both
    )
    return summary(db, stores, request)


def check_capacity(db, stores, request: Request, extra: str) -> None:
    """Raises 409 if one more gateway would exceed the licence limit."""
    state = licence.evaluate(
        db, stores, datetime.now(UTC), request.app.state.settings.licence_require_both
    )
    if licence.licensed_count(db) >= state.max_gateways:
        tier = " (free tier)" if state.status != "licensed" else ""
        raise HTTPException(
            status_code=409,
            detail=f"The licence allows {state.max_gateways} gateways{tier}. {extra}",
        )
