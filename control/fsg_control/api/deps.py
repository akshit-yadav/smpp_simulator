from typing import Annotated

from fastapi import Depends, HTTPException, Request
from sqlalchemy.orm import Session

from fsg_control.auth import CurrentAdmin, current_admin
from fsg_control.db import get_db
from fsg_control.security import Cipher
from fsg_control.stores import Stores

DB = Annotated[Session, Depends(get_db)]
Admin = Annotated[CurrentAdmin, Depends(current_admin)]


def stores(request: Request) -> Stores:
    return request.app.state.stores


def cipher(request: Request) -> Cipher:
    c = request.app.state.cipher
    if c is None:
        raise HTTPException(status_code=503, detail="FSG_SECRET_KEY is not configured.")
    return c


StoresDep = Annotated[Stores, Depends(stores)]
CipherDep = Annotated[Cipher, Depends(cipher)]
