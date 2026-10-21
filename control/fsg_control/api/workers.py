from datetime import datetime

from fastapi import APIRouter, HTTPException, Request
from sqlalchemy import func, select

from fsg_control import audit, sync
from fsg_control.api.deps import DB, Admin, StoresDep
from fsg_control.models import Gateway, Worker
from fsg_control.schemas import WorkerOut, WorkerPatch

router = APIRouter(prefix="/workers", tags=["workers"])


def to_out(w: Worker, stores, counts: dict[int, int]) -> WorkerOut:
    return WorkerOut(
        id=w.id,
        name=w.name,
        ip=w.ip,
        port_min=w.port_min,
        port_max=w.port_max,
        port_exclude=w.port_exclude or [],
        status=w.status,
        version=w.version,
        config_version=w.config_version,
        online=sync.worker_online(stores, w.id),
        last_seen=w.last_seen,
        gateways=counts.get(w.id, 0),
    )


def gateway_counts(db) -> dict[int, int]:
    return dict(
        db.execute(select(Gateway.worker_id, func.count()).group_by(Gateway.worker_id)).all()
    )


@router.get("", response_model=list[WorkerOut])
def list_workers(db: DB, admin: Admin, stores: StoresDep):
    """Workers register themselves through their Redis heartbeat."""
    counts = gateway_counts(db)
    return [to_out(w, stores, counts) for w in db.scalars(select(Worker).order_by(Worker.id))]


@router.patch("/{worker_id}", response_model=WorkerOut)
def update_worker(
    worker_id: int, body: WorkerPatch, request: Request, db: DB, admin: Admin, stores: StoresDep
):
    """Rename, or set status to draining (no new gateways) / active."""
    w = db.get(Worker, worker_id)
    if w is None:
        raise HTTPException(status_code=404, detail="Worker not found.")
    changes = body.model_dump(exclude_unset=True, exclude_none=True)
    before = {k: getattr(w, k) for k in changes}
    for k, v in changes.items():
        setattr(w, k, v)
    audit.record(
        db, request, admin.username, "worker.update", "worker", w.id, audit.diff(before, changes)
    )
    db.commit()
    return to_out(w, stores, gateway_counts(db))


@router.delete("/{worker_id}", status_code=204)
def delete_worker(worker_id: int, request: Request, db: DB, admin: Admin, stores: StoresDep):
    """Forget an offline worker that has no gateways."""
    w = db.get(Worker, worker_id)
    if w is None:
        raise HTTPException(status_code=404, detail="Worker not found.")
    if gateway_counts(db).get(w.id):
        raise HTTPException(status_code=409, detail="Move or delete the worker's gateways first.")
    if sync.worker_online(stores, w.id):
        raise HTTPException(status_code=409, detail="Worker is online; stop it first.")
    audit.record(db, request, admin.username, "worker.delete", "worker", w.id, {"name": w.name})
    db.delete(w)
    db.commit()


def upsert_from_heartbeat(db, beat: dict, now: datetime) -> bool:
    """Creates or refreshes a worker row from its heartbeat. Returns True if new."""
    wid = int(beat["worker_id"])
    lo, _, hi = str(beat.get("port_range", "20000-20999")).partition("-")
    w = db.get(Worker, wid)
    created = w is None
    if created:
        w = Worker(id=wid, name=f"worker-{wid}", status="active", config_version=1)
        db.add(w)
    w.ip = beat.get("ip", "")
    w.port_min, w.port_max = int(lo), int(hi)
    w.port_exclude = beat.get("port_exclude") or []
    w.version = beat.get("version")
    w.last_seen = now
    return created
