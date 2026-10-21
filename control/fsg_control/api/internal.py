"""Worker-facing API. Authenticated with FSG_INTERNAL_TOKEN; returns plain passwords."""

import hmac
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Request

from fsg_control import resolve
from fsg_control.api.deps import DB, CipherDep
from fsg_control.models import Worker


def require_token(request: Request) -> None:
    expected = request.app.state.settings.internal_token
    if expected is None or not expected.get_secret_value():
        raise HTTPException(status_code=503, detail="FSG_INTERNAL_TOKEN is not configured.")
    given = request.headers.get("authorization", "").removeprefix("Bearer ")
    if not hmac.compare_digest(given.encode(), expected.get_secret_value().encode()):
        raise HTTPException(status_code=401, detail="Invalid internal token.")


router = APIRouter(
    prefix="/internal/v1",
    tags=["internal"],
    dependencies=[Depends(require_token)],
    include_in_schema=False,
)


@router.get("/workers/{worker_id}/config")
def worker_config(worker_id: int, db: DB, cipher: CipherDep):
    """Every gateway assigned to the worker, fully resolved. An unregistered
    worker gets version 0 and no gateways."""
    w = db.get(Worker, worker_id)
    if w is None:
        return {"worker_id": worker_id, "version": 0, "gateways": []}
    return resolve.worker_config(db, cipher, w)


@router.get("/workers/{worker_id}/config/version")
def worker_config_version(worker_id: int, db: DB):
    w = db.get(Worker, worker_id)
    return {"worker_id": worker_id, "version": w.config_version if w else 0}


InternalAuth = Annotated[None, Depends(require_token)]
