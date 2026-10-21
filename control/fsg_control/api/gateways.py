from contextlib import nullcontext

from fastapi import APIRouter, HTTPException, Request
from sqlalchemy import delete, func, or_, select
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import Session

from fsg_control import audit, ports, resolve, sync
from fsg_control.api.deps import DB, Admin, CipherDep, StoresDep
from fsg_control.api.profiles import apply as apply_profile
from fsg_control.models import SETTING_COLUMNS, Defaults, Gateway, OutcomeProfile, Worker
from fsg_control.schemas import (
    GatewayCreate,
    GatewayOut,
    GatewayUpdate,
    ProfileIn,
    ProfileOut,
    Settings,
)
from fsg_control.stores import Stores

router = APIRouter(tags=["gateways"])

SIMPLE_FIELDS = ("name", "system_id", "status", "notes")


# ---- helpers -------------------------------------------------------------------------


def pick_worker(db: Session, worker_id: int | None) -> Worker:
    """The requested worker, or the only active one if none was given."""
    if worker_id is None:
        active = list(db.scalars(select(Worker).where(Worker.status == "active")))
        if len(active) != 1:
            raise HTTPException(
                status_code=422,
                detail="worker_id is required"
                + (" (no worker has registered yet)" if not active else " (several workers)"),
            )
        return active[0]
    worker = db.get(Worker, worker_id)
    if worker is None:
        raise HTTPException(status_code=422, detail=f"Worker {worker_id} does not exist.")
    if worker.status != "active":
        raise HTTPException(status_code=422, detail=f"Worker {worker.name} is draining.")
    return worker


def check_effective(eff: dict) -> None:
    if eff["dlr_min_ms"] > eff["dlr_max_ms"]:
        raise HTTPException(
            status_code=422,
            detail=f"DLR delay range is invalid: {eff['dlr_min_ms']} > {eff['dlr_max_ms']} ms.",
        )


def check_profile_choice(db: Session, profile_id: int | None, gateway_id: int | None) -> None:
    if profile_id is None:
        return
    p = db.get(OutcomeProfile, profile_id)
    if p is None or (p.owner_gateway_id is not None and p.owner_gateway_id != gateway_id):
        raise HTTPException(status_code=422, detail="outcome_profile_id must be a shared profile.")


def custom_profile_name(gw: Gateway) -> str:
    return f"custom: gateway {gw.id}"


def set_custom_outcome(db: Session, gw: Gateway, body: ProfileIn | None) -> None:
    """Creates, replaces or removes the gateway's own profile."""
    owned = db.scalar(select(OutcomeProfile).where(OutcomeProfile.owner_gateway_id == gw.id))
    if body is None:
        if owned is not None:
            if gw.outcome_profile_id == owned.id:
                gw.outcome_profile_id = None
            db.flush()
            db.delete(owned)
        return
    body = body.model_copy(update={"name": custom_profile_name(gw)})
    if owned is None:
        owned = OutcomeProfile(owner_gateway_id=gw.id)
        apply_profile(db, owned, body)  # fill it before add, so autoflush never sees it half-built
        db.add(owned)
    else:
        apply_profile(db, owned, body)
    db.flush()
    gw.outcome_profile_id = owned.id


def snapshot(gw: Gateway) -> dict:
    return {
        "name": gw.name,
        "worker_id": gw.worker_id,
        "port": gw.port,
        "system_id": gw.system_id,
        "status": gw.status,
        "notes": gw.notes,
        **resolve.raw_settings(gw),
    }


def to_out(gw: Gateway, d: Defaults, db: Session, runtime: dict | None, warnings=()) -> GatewayOut:
    custom = db.scalar(select(OutcomeProfile.id).where(OutcomeProfile.owner_gateway_id == gw.id))
    return GatewayOut(
        id=gw.id,
        name=gw.name,
        worker_id=gw.worker_id,
        worker_name=gw.worker.name,
        ip=gw.worker.ip,
        port=gw.port,
        system_id=gw.system_id,
        status=gw.status,
        notes=gw.notes,
        version=gw.version,
        settings=Settings(**resolve.raw_settings(gw)),
        effective=resolve.effective(gw, d),
        custom_outcome=custom is not None,
        runtime=runtime,
        warnings=list(warnings),
        created_at=gw.created_at,
        updated_at=gw.updated_at,
    )


def get_gateway(db: Session, gateway_id: int) -> Gateway:
    gw = db.get(Gateway, gateway_id)
    if gw is None:
        raise HTTPException(status_code=404, detail="Gateway not found.")
    return gw


def name_conflict(db: Session, name: str, exclude_id: int | None = None) -> None:
    q = select(Gateway.id).where(Gateway.name == name)
    if exclude_id is not None:
        q = q.where(Gateway.id != exclude_id)
    if db.scalar(q) is not None:
        raise HTTPException(status_code=409, detail=f"A gateway named {name!r} already exists.")


def commit_or_conflict(db: Session, worker: Worker, port: int, gateway_id: int | None) -> None:
    try:
        db.commit()
    except IntegrityError as exc:
        db.rollback()
        if "uq_gateway_worker_port" in str(exc.orig):
            raise ports.conflict(db, worker, port, gateway_id) from exc
        raise HTTPException(status_code=409, detail="Gateway name already exists.") from exc


def runtime_for(stores: Stores, gw: Gateway) -> dict | None:
    return sync.runtime_status(stores, gw.worker_id).get(gw.id)


# ---- endpoints ---------------------------------------------------------------------------


@router.get("/gateways", response_model=list[GatewayOut])
def list_gateways(
    db: DB,
    admin: Admin,
    stores: StoresDep,
    worker_id: int | None = None,
    status: str | None = None,
    q: str | None = None,
):
    stmt = select(Gateway).order_by(Gateway.name)
    if worker_id is not None:
        stmt = stmt.where(Gateway.worker_id == worker_id)
    if status:
        stmt = stmt.where(Gateway.status == status)
    if q:
        like = f"%{q}%"
        stmt = stmt.where(or_(Gateway.name.like(like), Gateway.system_id.like(like)))
    d = resolve.defaults(db)
    gws = list(db.scalars(stmt))
    runtime = {wid: sync.runtime_status(stores, wid) for wid in {g.worker_id for g in gws}}
    return [to_out(g, d, db, runtime[g.worker_id].get(g.id)) for g in gws]


@router.get("/gateways/{gateway_id}", response_model=GatewayOut)
def read_gateway(gateway_id: int, db: DB, admin: Admin, stores: StoresDep):
    gw = get_gateway(db, gateway_id)
    return to_out(gw, resolve.defaults(db), db, runtime_for(stores, gw))


@router.post("/gateways", response_model=GatewayOut, status_code=201)
def create_gateway(
    body: GatewayCreate,
    request: Request,
    db: DB,
    admin: Admin,
    stores: StoresDep,
    cipher: CipherDep,
):
    if body.custom_outcome is not None and body.outcome_profile_id is not None:
        raise HTTPException(
            status_code=422, detail="Give outcome_profile_id or custom_outcome, not both."
        )
    worker = pick_worker(db, body.worker_id)
    name_conflict(db, body.name)
    check_profile_choice(db, body.outcome_profile_id, None)
    raw = body.model_dump(include=set(SETTING_COLUMNS))
    check_effective(resolve.effective(raw, resolve.defaults(db)))

    timeout = request.app.state.settings.port_check_timeout_s
    with ports.port_lock(stores, worker.id, body.port, ports.conflict(db, worker, body.port, None)):
        warnings = ports.validate(db, stores, worker, body.port, None, timeout)
        gw = Gateway(
            name=body.name,
            worker_id=worker.id,
            port=body.port,
            system_id=body.system_id,
            password_enc=cipher.encrypt(body.password),
            status=body.status,
            notes=body.notes,
            version=1,
            **raw,
        )
        db.add(gw)
        try:
            db.flush()
        except IntegrityError as exc:
            db.rollback()
            raise ports.conflict(db, worker, body.port, None) from exc
        if body.custom_outcome is not None:
            set_custom_outcome(db, gw, body.custom_outcome)
        changes = sync.Changes()
        changes.bump(db, {worker.id}, gw.id, "create")
        audit.record(
            db,
            request,
            admin.username,
            "gateway.create",
            "gateway",
            gw.id,
            body.model_dump(exclude={"password"}) | {"worker_id": worker.id},
        )
        commit_or_conflict(db, worker, body.port, None)
    changes.publish(stores)
    db.refresh(gw)
    return to_out(gw, resolve.defaults(db), db, None, warnings)


@router.patch("/gateways/{gateway_id}", response_model=GatewayOut)
def update_gateway(
    gateway_id: int,
    body: GatewayUpdate,
    request: Request,
    db: DB,
    admin: Admin,
    stores: StoresDep,
    cipher: CipherDep,
):
    """Partial update. A setting sent as null goes back to following the default."""
    gw = get_gateway(db, gateway_id)
    fields = body.model_fields_set
    if "custom_outcome" in fields and body.custom_outcome is not None and body.outcome_profile_id:
        raise HTTPException(
            status_code=422, detail="Give outcome_profile_id or custom_outcome, not both."
        )
    for required in ("name", "port", "system_id", "password", "status"):
        if required in fields and getattr(body, required) is None:
            raise HTTPException(status_code=422, detail=f"{required} cannot be null.")
    before = snapshot(gw)
    old_worker_id = gw.worker_id

    worker = gw.worker
    if "worker_id" in fields and body.worker_id is not None and body.worker_id != gw.worker_id:
        worker = pick_worker(db, body.worker_id)
    port = body.port if "port" in fields else gw.port
    moved = worker.id != gw.worker_id or port != gw.port

    if "name" in fields:
        name_conflict(db, body.name, gw.id)
    if "outcome_profile_id" in fields:
        check_profile_choice(db, body.outcome_profile_id, gw.id)
    for f in SIMPLE_FIELDS:
        if f in fields:
            setattr(gw, f, getattr(body, f))
    if "password" in fields:
        gw.password_enc = cipher.encrypt(body.password)
    for c in SETTING_COLUMNS:
        if c in fields:
            setattr(gw, c, getattr(body, c))
    if "custom_outcome" in fields:
        set_custom_outcome(db, gw, body.custom_outcome)
    check_effective(resolve.effective(gw, resolve.defaults(db)))
    gw.version += 1

    warnings: list[str] = []
    changes = sync.Changes()
    lock = (
        ports.port_lock(stores, worker.id, port, ports.conflict(db, worker, port, gw.id))
        if moved
        else nullcontext()
    )
    with lock:
        if moved:
            warnings = ports.validate(
                db, stores, worker, port, gw.id, request.app.state.settings.port_check_timeout_s
            )
            gw.worker_id, gw.port = worker.id, port
        changes.bump(db, {old_worker_id, worker.id}, gw.id, "update")
        details = audit.diff(before, snapshot(gw))
        if "password" in fields:
            details["password"] = ["*****", "*****"]
        if "custom_outcome" in fields:
            details["custom_outcome"] = (
                body.custom_outcome.model_dump() if body.custom_outcome else None
            )
        audit.record(db, request, admin.username, "gateway.update", "gateway", gw.id, details)
        commit_or_conflict(db, worker, port, gw.id)
    changes.publish(stores)
    db.refresh(gw)
    return to_out(gw, resolve.defaults(db), db, runtime_for(stores, gw), warnings)


@router.delete("/gateways/{gateway_id}", status_code=204)
def delete_gateway(gateway_id: int, request: Request, db: DB, admin: Admin, stores: StoresDep):
    gw = get_gateway(db, gateway_id)
    changes = sync.Changes()
    changes.bump(db, {gw.worker_id}, gw.id, "delete")
    audit.record(db, request, admin.username, "gateway.delete", "gateway", gw.id, snapshot(gw))
    gw.outcome_profile_id = None
    db.flush()
    db.execute(delete(OutcomeProfile).where(OutcomeProfile.owner_gateway_id == gw.id))
    db.delete(gw)
    db.commit()
    changes.publish(stores)
    sync.forget_gateway(stores, gateway_id)


@router.post("/gateways/{gateway_id}/reveal-password")
def reveal_password(gateway_id: int, request: Request, db: DB, admin: Admin, cipher: CipherDep):
    """Returns the plain password (audited)."""
    gw = get_gateway(db, gateway_id)
    audit.record(db, request, admin.username, "gateway.reveal_password", "gateway", gw.id)
    db.commit()
    return {"password": cipher.decrypt(gw.password_enc)}


@router.get("/gateways/{gateway_id}/profile")
def gateway_profile(gateway_id: int, db: DB, admin: Admin):
    """The outcome profile this gateway effectively uses."""
    gw = get_gateway(db, gateway_id)
    pid = resolve.effective(gw, resolve.defaults(db))["outcome_profile_id"]
    return ProfileOut.model_validate(db.get(OutcomeProfile, pid))


@router.get("/ports/check")
def check_port(
    worker_id: int,
    port: int,
    request: Request,
    db: DB,
    admin: Admin,
    stores: StoresDep,
    gateway_id: int | None = None,
):
    """Runs the three port checks without saving anything."""
    worker = db.get(Worker, worker_id)
    if worker is None:
        raise HTTPException(status_code=404, detail="Worker not found.")
    try:
        warnings = ports.validate(
            db, stores, worker, port, gateway_id, request.app.state.settings.port_check_timeout_s
        )
    except HTTPException as exc:
        return {"ok": False, **exc.detail}
    return {"ok": True, "message": f"Port {port} is free.", "warnings": warnings}


@router.get("/workers/{worker_id}/ports/next-free")
def next_free(worker_id: int, db: DB, admin: Admin, start: int | None = None):
    worker = db.get(Worker, worker_id)
    if worker is None:
        raise HTTPException(status_code=404, detail="Worker not found.")
    used = db.scalar(
        select(func.count()).select_from(Gateway).where(Gateway.worker_id == worker_id)
    )
    return {"port": ports.next_free_port(db, worker, start), "used": used}
