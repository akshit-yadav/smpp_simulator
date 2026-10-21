from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI
from fastapi.staticfiles import StaticFiles
from starlette.exceptions import HTTPException as StarletteHTTPException

from fsg_control import __version__
from fsg_control.api import health, workers
from fsg_control.settings import Settings, get_settings
from fsg_control.stores import Stores


class SPAStaticFiles(StaticFiles):
    """Serves the built panel; unknown paths fall back to index.html for client routing."""

    async def get_response(self, path, scope):
        try:
            return await super().get_response(path, scope)
        except StarletteHTTPException as exc:
            if exc.status_code != 404 or path.startswith("api/"):
                raise
            return await super().get_response("index.html", scope)


def create_app(settings: Settings | None = None) -> FastAPI:
    settings = settings or get_settings()
    stores = Stores(settings)

    @asynccontextmanager
    async def lifespan(_app: FastAPI):
        yield
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

    app.include_router(health.router)
    app.include_router(workers.router, prefix="/api/v1")

    # Mounted last so API routes take precedence.
    if settings.panel_dir and Path(settings.panel_dir, "index.html").is_file():
        app.mount("/", SPAStaticFiles(directory=settings.panel_dir, html=True), name="panel")

    return app
