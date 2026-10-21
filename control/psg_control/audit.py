"""Audit log. Every change made through the API is recorded; secrets are masked."""

from typing import Any

from fastapi import Request
from sqlalchemy.orm import Session

from psg_control.models import AuditLog

SECRET_FIELDS = {"password", "password_enc", "current_password", "new_password", "totp_code"}


def mask(value: Any) -> Any:
    if isinstance(value, dict):
        return {k: "*****" if k in SECRET_FIELDS else mask(v) for k, v in value.items()}
    if isinstance(value, list):
        return [mask(v) for v in value]
    return value


def diff(before: dict, after: dict) -> dict:
    """Fields that changed, as {field: [old, new]}, with secrets masked."""
    out = {}
    for k in before.keys() | after.keys():
        if before.get(k) != after.get(k):
            out[k] = ["*****", "*****"] if k in SECRET_FIELDS else [before.get(k), after.get(k)]
    return out


def client_ip(request: Request | None) -> str | None:
    if request is None or request.client is None:
        return None
    return request.client.host


def record(
    db: Session,
    request: Request | None,
    actor: str,
    action: str,
    entity_type: str | None = None,
    entity_id: Any = None,
    details: dict | None = None,
) -> None:
    """Adds an audit row to the current transaction (committed with the change)."""
    db.add(
        AuditLog(
            actor=actor,
            action=action,
            entity_type=entity_type,
            entity_id=None if entity_id is None else str(entity_id),
            ip=client_ip(request),
            details=mask(details) if details else None,
        )
    )
