import uvicorn

from fsg_control.logging_config import logging_config
from fsg_control.settings import get_settings


def main() -> None:
    settings = get_settings()
    uvicorn.run(
        "fsg_control.main:create_app",
        factory=True,
        host=settings.http_host,
        port=settings.http_port,
        log_config=logging_config(settings.log_level),
        proxy_headers=True,
    )


if __name__ == "__main__":
    main()
