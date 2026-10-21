import asyncio
import contextlib
import logging
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI
from fastapi.staticfiles import StaticFiles
from sqlalchemy.exc import SQLAlchemyError
from sqlalchemy.orm import Session
from starlette.exceptions import HTTPException as StarletteHTTPException

from fsg_control import __version__, scheduler
from fsg_control.api import (
    audit_log,
    auth,
    catalog,
    defaults,
    gateways,
    health,
    internal,
    profiles,
    workers,
)
from fsg_control.auth import bootstrap_admin
from fsg_control.security import Cipher
from fsg_control.settings import Settings, get_settings
from fsg_control.stores import Stores

log = logging.getLogger(__name__)


class SPAStaticFiles(StaticFiles):
    """Serves the built panel; unknown paths fall back to index.html for client routing."""

    async def get_response(self, path, scope):
        try:
            return await super().get_response(path, scope)
        except StarletteHTTPException as exc:
            if exc.status_code != 404 or path.startswith("api/"):
                raise
            return await super().get_response("index.html", scope)


def create_app(settings: Settings | None = None, run_scheduler: bool = True) -> FastAPI:
    settings = settings or get_settings()
    stores = Stores(settings)
    cipher = Cipher(settings.secret_key.get_secret_value()) if settings.secret_key else None
    if cipher is None:
        log.warning("FSG_SECRET_KEY is not set; gateway passwords cannot be stored")

    @asynccontextmanager
    async def lifespan(_app: FastAPI):
        try:
            with Session(stores.db) as db:
                bootstrap_admin(db, settings)
        except SQLAlchemyError as exc:
            log.warning("admin bootstrap skipped, database unavailable: %s", type(exc).__name__)
        task = asyncio.create_task(scheduler.run(stores)) if run_scheduler else None
        yield
        if task:
            task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await task
        stores.close()

    app = FastAPI(
        title="Fake SMPP Gateway",
        version=__version__,
        lifespan=lifespan,
        docs_url="/api/docs",
        openapi_url="/api/openapi.json",
        redoc_url=None,
    )
    app.state.settings = settings
    app.state.stores = stores
    app.state.cipher = cipher

    app.include_router(health.router)
    for module in (auth, workers, gateways, profiles, catalog, defaults, audit_log):
        app.include_router(module.router, prefix="/api/v1")
    app.include_router(internal.router)

    # Mounted last so API routes take precedence.
    if settings.panel_dir and Path(settings.panel_dir, "index.html").is_file():
        app.mount("/", SPAStaticFiles(directory=settings.panel_dir, html=True), name="panel")

    return app
