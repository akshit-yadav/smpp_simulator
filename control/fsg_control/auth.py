"""Single-admin authentication: password (argon2) plus optional TOTP, with
server-side sessions in an HttpOnly, SameSite=Strict cookie."""

import logging
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from typing import Annotated

import pyotp
from fastapi import Depends, HTTPException, Request
from sqlalchemy import delete, select
from sqlalchemy.orm import Session

from fsg_control import audit
from fsg_control.db import get_db
from fsg_control.models import Admin, AdminSession
from fsg_control.security import hash_password, new_token, token_hash, verify_password
from fsg_control.settings import Settings

log = logging.getLogger(__name__)

COOKIE = "fsg_session"


def utcnow() -> datetime:
    """Naive UTC, matching the DATETIME columns."""
    return datetime.now(UTC).replace(tzinfo=None)


@dataclass
class CurrentAdmin:
    id: int
    username: str
    totp_enabled: bool


class LoginError(Exception):
    def __init__(self, message: str, totp_required: bool = False) -> None:
        super().__init__(message)
        self.totp_required = totp_required


def bootstrap_admin(db: Session, settings: Settings) -> bool:
    """Creates the admin from FSG_ADMIN_USERNAME/PASSWORD if none exists."""
    if db.scalar(select(Admin.id).limit(1)) is not None:
        return False
    if settings.admin_password is None or not settings.admin_password.get_secret_value():
        log.warning("no admin account and FSG_ADMIN_PASSWORD is not set; login is impossible")
        return False
    db.add(
        Admin(
            username=settings.admin_username,
            password_hash=hash_password(settings.admin_password.get_secret_value()),
            failed_logins=0,
        )
    )
    audit.record(db, None, "system", "admin.bootstrap", "admin", settings.admin_username)
    db.commit()
    log.info("created admin account %r from FSG_ADMIN_USERNAME", settings.admin_username)
    return True


def verify_totp(request: Request, admin: Admin, code: str | None) -> bool:
    if not code:
        return False
    secret = request.app.state.cipher.decrypt(admin.totp_secret_enc)
    totp = pyotp.TOTP(secret)
    now = utcnow().timestamp()
    for offset in (-1, 0, 1):  # allow 30 s of clock skew
        step = int(now // totp.interval) + offset
        if step > (admin.totp_last_step or 0) and totp.at(step * totp.interval) == code:
            admin.totp_last_step = step  # a code works once
            return True
    return False


def login(
    db: Session, request: Request, username: str, password: str, totp_code: str | None
) -> str:
    """Returns a new session token, or raises LoginError."""
    settings: Settings = request.app.state.settings
    admin = db.scalar(select(Admin).where(Admin.username == username))
    now = utcnow()

    if admin and admin.locked_until and admin.locked_until > now:
        raise LoginError("Too many failed attempts. Try again later.")

    def fail(action: str) -> None:
        admin.failed_logins += 1
        if admin.failed_logins >= settings.login_max_failures:
            admin.locked_until = now + timedelta(minutes=settings.lockout_minutes)
            admin.failed_logins = 0
        audit.record(db, request, username, action, "admin", username)
        db.commit()

    if not verify_password(admin.password_hash if admin else None, password) or admin is None:
        if admin:
            fail("auth.login_failed")
        raise LoginError("Invalid username or password.")
    if admin.totp_secret_enc is not None:
        if not totp_code:
            raise LoginError("TOTP code required.", totp_required=True)
        if not verify_totp(request, admin, totp_code):
            fail("auth.totp_failed")
            raise LoginError("Invalid TOTP code.", totp_required=True)

    admin.failed_logins = 0
    admin.locked_until = None
    token = new_token()
    db.add(
        AdminSession(
            token_hash=token_hash(token),
            admin_id=admin.id,
            expires_at=now + timedelta(hours=settings.session_ttl_hours),
            ip=audit.client_ip(request),
            user_agent=(request.headers.get("user-agent") or "")[:255],
        )
    )
    db.execute(delete(AdminSession).where(AdminSession.expires_at < now))
    audit.record(db, request, username, "auth.login", "admin", username)
    db.commit()
    return token


def current_admin(request: Request, db: Annotated[Session, Depends(get_db)]) -> CurrentAdmin:
    token = request.cookies.get(COOKIE)
    if not token:
        raise HTTPException(status_code=401, detail="Not logged in.")
    row = db.execute(
        select(AdminSession, Admin)
        .join(Admin, Admin.id == AdminSession.admin_id)
        .where(AdminSession.token_hash == token_hash(token))
    ).first()
    if row is None or row.AdminSession.expires_at < utcnow():
        raise HTTPException(status_code=401, detail="Session expired. Log in again.")
    admin = row.Admin
    return CurrentAdmin(
        id=admin.id, username=admin.username, totp_enabled=admin.totp_secret_enc is not None
    )
