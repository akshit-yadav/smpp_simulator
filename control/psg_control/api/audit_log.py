from fastapi import APIRouter, Query
from sqlalchemy import select

from psg_control.api.deps import DB, Admin
from psg_control.models import AuditLog
from psg_control.schemas import AuditOut

router = APIRouter(prefix="/audit", tags=["audit"])


@router.get("", response_model=list[AuditOut])
def list_audit(
    db: DB,
    admin: Admin,
    limit: int = Query(default=100, ge=1, le=1000),
    before_id: int | None = None,
    action: str | None = None,
    entity_type: str | None = None,
    entity_id: str | None = None,
):
    """Newest first. Page with before_id = the last id you received."""
    q = select(AuditLog).order_by(AuditLog.id.desc()).limit(limit)
    if before_id is not None:
        q = q.where(AuditLog.id < before_id)
    if action:
        q = q.where(AuditLog.action.like(f"{action}%"))
    if entity_type:
        q = q.where(AuditLog.entity_type == entity_type)
    if entity_id:
        q = q.where(AuditLog.entity_id == entity_id)
    return list(db.scalars(q))
