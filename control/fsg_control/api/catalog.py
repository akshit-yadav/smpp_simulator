from fastapi import APIRouter, HTTPException, Request
from sqlalchemy import select

from fsg_control import audit
from fsg_control.api.deps import DB, Admin, StoresDep
from fsg_control.catalog import DLR_STATUSES
from fsg_control.models import CommandStatus, DLRError, OutcomeEntry
from fsg_control.schemas import (
    CommandStatusIn,
    CommandStatusOut,
    CommandStatusPatch,
    DlrErrorIn,
    DlrErrorOut,
    DlrErrorPatch,
)
from fsg_control.sync import Changes

router = APIRouter(prefix="/catalog", tags=["catalogs"])


# ---- command_status -----------------------------------------------------------


@router.get("/command-statuses", response_model=list[CommandStatusOut])
def list_command_statuses(db: DB, admin: Admin):
    """The SMPP 3.4 list (read-only) plus vendor codes in 0x400-0x4FF."""
    return list(db.scalars(select(CommandStatus).order_by(CommandStatus.code)))


def _vendor(db, code: int) -> CommandStatus:
    row = db.get(CommandStatus, code)
    if row is None:
        raise HTTPException(status_code=404, detail="Command status not found.")
    if row.system:
        raise HTTPException(status_code=409, detail="SMPP 3.4 command statuses are read-only.")
    return row


@router.post("/command-statuses", response_model=CommandStatusOut, status_code=201)
def create_command_status(body: CommandStatusIn, request: Request, db: DB, admin: Admin):
    if db.get(CommandStatus, body.code) is not None:
        raise HTTPException(status_code=409, detail=f"Code 0x{body.code:X} already exists.")
    row = CommandStatus(**body.model_dump(), system=False)
    db.add(row)
    audit.record(
        db,
        request,
        admin.username,
        "catalog.command_status.create",
        "command_status",
        f"0x{body.code:X}",
        body.model_dump(),
    )
    db.commit()
    return row


@router.patch("/command-statuses/{code}", response_model=CommandStatusOut)
def update_command_status(
    code: int, body: CommandStatusPatch, request: Request, db: DB, admin: Admin
):
    row = _vendor(db, code)
    changes = body.model_dump(exclude_unset=True)
    before = {k: getattr(row, k) for k in changes}
    for k, v in changes.items():
        setattr(row, k, v)
    audit.record(
        db,
        request,
        admin.username,
        "catalog.command_status.update",
        "command_status",
        f"0x{code:X}",
        audit.diff(before, changes),
    )
    db.commit()
    return row


@router.delete("/command-statuses/{code}", status_code=204)
def delete_command_status(code: int, request: Request, db: DB, admin: Admin):
    row = _vendor(db, code)
    if db.scalar(select(OutcomeEntry.id).where(OutcomeEntry.command_status == code).limit(1)):
        raise HTTPException(status_code=409, detail="Code is used by an outcome profile.")
    audit.record(
        db,
        request,
        admin.username,
        "catalog.command_status.delete",
        "command_status",
        f"0x{code:X}",
        {"name": row.name},
    )
    db.delete(row)
    db.commit()


# ---- DLR statuses ---------------------------------------------------------------


@router.get("/dlr-statuses")
def list_dlr_statuses(admin: Admin):
    return [{"status": s, "message_state": v} for s, v in DLR_STATUSES.items()]


# ---- DLR errors ---------------------------------------------------------------------


@router.get("/dlr-errors", response_model=list[DlrErrorOut])
def list_dlr_errors(db: DB, admin: Admin):
    return list(db.scalars(select(DLRError).order_by(DLRError.system.desc(), DLRError.code)))


def _custom(db, code: str) -> DLRError:
    row = db.get(DLRError, code)
    if row is None:
        raise HTTPException(status_code=404, detail="DLR error not found.")
    if row.system:
        raise HTTPException(status_code=409, detail="System DLR errors are read-only.")
    return row


@router.post("/dlr-errors", response_model=DlrErrorOut, status_code=201)
def create_dlr_error(body: DlrErrorIn, request: Request, db: DB, admin: Admin):
    if db.get(DLRError, body.code) is not None:
        raise HTTPException(status_code=409, detail=f"DLR error {body.code!r} already exists.")
    row = DLRError(**body.model_dump(), system=False)
    db.add(row)
    audit.record(
        db,
        request,
        admin.username,
        "catalog.dlr_error.create",
        "dlr_error",
        body.code,
        body.model_dump(),
    )
    db.commit()
    return row


@router.patch("/dlr-errors/{code}", response_model=DlrErrorOut)
def update_dlr_error(
    code: str, body: DlrErrorPatch, request: Request, db: DB, admin: Admin, stores: StoresDep
):
    row = _custom(db, code)
    changes = body.model_dump(exclude_unset=True)
    before = {k: getattr(row, k) for k in changes}
    for k, v in changes.items():
        setattr(row, k, v)
    if (row.network_error_type is None) != (row.network_error_code is None):
        raise HTTPException(
            status_code=422,
            detail="Set both network_error_type and network_error_code, or neither.",
        )
    sync = Changes()
    sync.bump(db, None, None, "catalog")  # the TLV and DLR text reach workers
    audit.record(
        db,
        request,
        admin.username,
        "catalog.dlr_error.update",
        "dlr_error",
        code,
        audit.diff(before, changes),
    )
    db.commit()
    sync.publish(stores)
    return row


@router.delete("/dlr-errors/{code}", status_code=204)
def delete_dlr_error(code: str, request: Request, db: DB, admin: Admin):
    row = _custom(db, code)
    if db.scalar(select(OutcomeEntry.id).where(OutcomeEntry.dlr_error == code).limit(1)):
        raise HTTPException(status_code=409, detail="DLR error is used by an outcome profile.")
    audit.record(
        db,
        request,
        admin.username,
        "catalog.dlr_error.delete",
        "dlr_error",
        code,
        {"label": row.label},
    )
    db.delete(row)
    db.commit()
