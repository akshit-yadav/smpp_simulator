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

from psg_control import __version__, scheduler
from psg_control.api import (
    audit_log,
    auth,
    catalog,
    defaults,
    dlr_rules,
    gateways,
    health,
    internal,
    licence,
    live,
    mo,
    reports,
    system,
    tests,
    workers,
)
from psg_control.auth import bootstrap_admin
from psg_control.licence import Ticker, install_from_file
from psg_control.security import Cipher
from psg_control.settings import Settings, get_settings
from psg_control.stores import Stores
from psg_control.usage import Usage

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
        log.warning("PSG_SECRET_KEY is not set; gateway passwords cannot be stored")

    @asynccontextmanager
    async def lifespan(_app: FastAPI):
        try:
            with Session(stores.db) as db:
                bootstrap_admin(db, settings)
            install_from_file(stores, settings.licence_file, settings.licence_require_both)
        except SQLAlchemyError as exc:
            log.warning("admin bootstrap skipped, database unavailable: %s", type(exc).__name__)
        task = (
            asyncio.create_task(scheduler.run(stores, app.state.licence_ticker, settings))
            if run_scheduler
            else None
        )
        yield
        if task:
            task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await task
        stores.close()

    app = FastAPI(
        title="Power SMPP Gateway Simulator",
        version=__version__,
        lifespan=lifespan,
        docs_url="/api/docs",
        openapi_url="/api/openapi.json",
        redoc_url=None,
    )
    app.state.settings = settings
    app.state.stores = stores
    app.state.cipher = cipher
    app.state.licence_ticker = Ticker()
    app.state.usage = Usage(settings.proc_root, settings.cgroup_root, settings.cgroup_slice)

    app.include_router(health.router)
    for module in (
        auth,
        workers,
        gateways,
        live,
        licence,
        mo,
        reports,
        tests,
        system,
        dlr_rules,
        catalog,
        defaults,
        audit_log,
    ):
        app.include_router(module.router, prefix="/api/v1")
    app.include_router(internal.router)
    app.include_router(tests.agent_router)

    # Mounted last so API routes take precedence.
    if settings.panel_dir and Path(settings.panel_dir, "index.html").is_file():
        app.mount("/", SPAStaticFiles(directory=settings.panel_dir, html=True), name="panel")

    return app
