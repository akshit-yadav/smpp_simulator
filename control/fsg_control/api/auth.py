import pyotp
from fastapi import APIRouter, HTTPException, Request, Response
from fastapi.responses import JSONResponse
from sqlalchemy import delete, select

from fsg_control import audit
from fsg_control.api.deps import DB, Admin, CipherDep
from fsg_control.auth import COOKIE, LoginError, login, verify_totp
from fsg_control.models import Admin as AdminRow
from fsg_control.models import AdminSession
from fsg_control.schemas import (
    LoginIn,
    MeOut,
    PasswordChangeIn,
    PasswordConfirmIn,
    TotpCodeIn,
    TotpDisableIn,
    TotpSetupOut,
)
from fsg_control.security import hash_password, token_hash, verify_password

router = APIRouter(prefix="/auth", tags=["auth"])


@router.post("/login", response_model=MeOut)
def do_login(body: LoginIn, request: Request, response: Response, db: DB):
    """Logs in and sets the session cookie. If TOTP is enabled and no code is
    given, answers 401 with totp_required: true."""
    try:
        token = login(db, request, body.username, body.password, body.totp_code)
    except LoginError as exc:
        return JSONResponse(
            status_code=401, content={"detail": str(exc), "totp_required": exc.totp_required}
        )
    settings = request.app.state.settings
    response.set_cookie(
        COOKIE,
        token,
        max_age=settings.session_ttl_hours * 3600,
        httponly=True,
        secure=settings.cookie_secure,
        samesite="strict",
        path="/",
    )
    admin = db.scalar(select(AdminRow).where(AdminRow.username == body.username))
    return MeOut(username=admin.username, totp_enabled=admin.totp_secret_enc is not None)


@router.post("/logout", status_code=204)
def do_logout(request: Request, response: Response, db: DB):
    token = request.cookies.get(COOKIE)
    if token:
        db.execute(delete(AdminSession).where(AdminSession.token_hash == token_hash(token)))
        db.commit()
    response.delete_cookie(COOKIE, path="/")


@router.get("/me", response_model=MeOut)
def me(admin: Admin):
    return MeOut(username=admin.username, totp_enabled=admin.totp_enabled)


def _admin_row(db, admin) -> AdminRow:
    return db.get(AdminRow, admin.id)


@router.post("/password", status_code=204)
def change_password(body: PasswordChangeIn, request: Request, db: DB, admin: Admin):
    """Changes the admin password and ends every other session."""
    row = _admin_row(db, admin)
    if not verify_password(row.password_hash, body.current_password):
        raise HTTPException(status_code=403, detail="Current password is wrong.")
    row.password_hash = hash_password(body.new_password)
    current = token_hash(request.cookies.get(COOKIE, ""))
    db.execute(
        delete(AdminSession).where(
            AdminSession.admin_id == row.id, AdminSession.token_hash != current
        )
    )
    audit.record(db, request, admin.username, "auth.password_changed", "admin", admin.username)
    db.commit()


@router.post("/totp/setup", response_model=TotpSetupOut)
def totp_setup(body: PasswordConfirmIn, request: Request, db: DB, admin: Admin, cipher: CipherDep):
    """Starts TOTP enrolment. Scan the URI, then confirm with /totp/enable."""
    row = _admin_row(db, admin)
    if not verify_password(row.password_hash, body.password):
        raise HTTPException(status_code=403, detail="Password is wrong.")
    if row.totp_secret_enc is not None:
        raise HTTPException(status_code=409, detail="TOTP is already enabled; disable it first.")
    secret = pyotp.random_base32()
    row.totp_pending_enc = cipher.encrypt(secret)
    db.commit()
    uri = pyotp.TOTP(secret).provisioning_uri(name=row.username, issuer_name="Fake SMPP Gateway")
    return TotpSetupOut(secret=secret, otpauth_uri=uri)


@router.post("/totp/enable", response_model=MeOut)
def totp_enable(body: TotpCodeIn, request: Request, db: DB, admin: Admin, cipher: CipherDep):
    row = _admin_row(db, admin)
    if row.totp_pending_enc is None:
        raise HTTPException(status_code=409, detail="Start with /auth/totp/setup.")
    if not pyotp.TOTP(cipher.decrypt(row.totp_pending_enc)).verify(body.code, valid_window=1):
        raise HTTPException(status_code=422, detail="Invalid TOTP code.")
    row.totp_secret_enc, row.totp_pending_enc, row.totp_last_step = row.totp_pending_enc, None, None
    audit.record(db, request, admin.username, "auth.totp_enabled", "admin", admin.username)
    db.commit()
    return MeOut(username=row.username, totp_enabled=True)


@router.post("/totp/disable", response_model=MeOut)
def totp_disable(body: TotpDisableIn, request: Request, db: DB, admin: Admin, cipher: CipherDep):
    row = _admin_row(db, admin)
    if row.totp_secret_enc is None:
        raise HTTPException(status_code=409, detail="TOTP is not enabled.")
    if not verify_password(row.password_hash, body.password) or not verify_totp(
        request, row, body.code
    ):
        raise HTTPException(status_code=403, detail="Password or TOTP code is wrong.")
    row.totp_secret_enc = row.totp_last_step = None
    audit.record(db, request, admin.username, "auth.totp_disabled", "admin", admin.username)
    db.commit()
    return MeOut(username=row.username, totp_enabled=False)
