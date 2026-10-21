from collections.abc import Iterator

from fastapi import HTTPException, Request
from sqlalchemy.exc import OperationalError
from sqlalchemy.orm import Session


def get_db(request: Request) -> Iterator[Session]:
    """One session per request. A request that fails rolls back."""
    with Session(request.app.state.stores.db, expire_on_commit=False) as session:
        try:
            yield session
        except OperationalError as exc:
            session.rollback()
            raise HTTPException(status_code=503, detail="database unavailable") from exc
