from alembic import context
from sqlalchemy import create_engine
from sqlalchemy.pool import NullPool

from psg_control.models import Base
from psg_control.settings import get_settings

# Autogenerate compares against the ORM models.
target_metadata = Base.metadata


def run_migrations_offline() -> None:
    context.configure(
        url=get_settings().mariadb_url.render_as_string(hide_password=False),
        target_metadata=target_metadata,
        literal_binds=True,
    )
    with context.begin_transaction():
        context.run_migrations()


def run_migrations_online() -> None:
    # Tests pass an open connection; otherwise connect using PSG_MARIADB_*.
    connection = context.config.attributes.get("connection")
    if connection is not None:
        context.configure(connection=connection, target_metadata=target_metadata)
        with context.begin_transaction():
            context.run_migrations()
        return
    engine = create_engine(get_settings().mariadb_url, poolclass=NullPool)
    with engine.connect() as connection:
        context.configure(connection=connection, target_metadata=target_metadata)
        with context.begin_transaction():
            context.run_migrations()


if context.is_offline_mode():
    run_migrations_offline()
else:
    run_migrations_online()
