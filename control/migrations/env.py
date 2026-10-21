from alembic import context
from sqlalchemy import create_engine
from sqlalchemy.pool import NullPool

from fsg_control.settings import get_settings

# Set to the models' Base.metadata once models exist (M3) to enable autogenerate.
target_metadata = None


def run_migrations_offline() -> None:
    context.configure(
        url=get_settings().mariadb_url.render_as_string(hide_password=False),
        target_metadata=target_metadata,
        literal_binds=True,
    )
    with context.begin_transaction():
        context.run_migrations()


def run_migrations_online() -> None:
    engine = create_engine(get_settings().mariadb_url, poolclass=NullPool)
    with engine.connect() as connection:
        context.configure(connection=connection, target_metadata=target_metadata)
        with context.begin_transaction():
            context.run_migrations()


if context.is_offline_mode():
    run_migrations_offline()
else:
    run_migrations_online()
