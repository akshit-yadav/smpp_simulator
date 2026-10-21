from functools import lru_cache

from pydantic import SecretStr
from pydantic_settings import BaseSettings, SettingsConfigDict
from sqlalchemy import URL


class Settings(BaseSettings):
    """Control-plane settings, read from FSG_* environment variables."""

    model_config = SettingsConfigDict(env_prefix="FSG_", extra="ignore")

    http_host: str = "127.0.0.1"
    http_port: int = 8080
    log_level: str = "info"
    panel_dir: str | None = None

    secret_key: SecretStr | None = None
    internal_token: SecretStr | None = None
    admin_username: str = "admin"
    admin_password: SecretStr | None = None

    mariadb_host: str = "127.0.0.1"
    mariadb_port: int = 3306
    mariadb_database: str = "fsg"
    mariadb_user: str = "fsg"
    mariadb_password: SecretStr = SecretStr("")

    redis_host: str = "127.0.0.1"
    redis_port: int = 6379
    redis_password: SecretStr = SecretStr("")
    redis_db: int = 0

    clickhouse_host: str = "127.0.0.1"
    clickhouse_http_port: int = 8123
    clickhouse_database: str = "fsg"
    clickhouse_user: str = "default"
    clickhouse_password: SecretStr = SecretStr("")

    @property
    def mariadb_url(self) -> URL:
        return URL.create(
            "mysql+pymysql",
            username=self.mariadb_user,
            password=self.mariadb_password.get_secret_value(),
            host=self.mariadb_host,
            port=self.mariadb_port,
            database=self.mariadb_database,
            query={"charset": "utf8mb4"},
        )


@lru_cache
def get_settings() -> Settings:
    return Settings()
