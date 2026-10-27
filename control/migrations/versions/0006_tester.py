"""SMPP client simulator: tester agents and test runs.

Revision ID: 0006
Revises: 0005
"""

import sqlalchemy as sa
from alembic import op

revision = "0006"
down_revision = "0005"
branch_labels = None
depends_on = None

TABLE = {
    "mysql_engine": "InnoDB",
    "mysql_charset": "utf8mb4",
    "mysql_collate": "utf8mb4_unicode_ci",
}


def upgrade() -> None:
    op.create_table(
        "tester",
        sa.Column("id", sa.SmallInteger(), autoincrement=False, nullable=False),
        sa.Column("name", sa.String(64), nullable=False),
        sa.Column("ip", sa.String(64), nullable=False),
        sa.Column("version", sa.String(32), nullable=True),
        sa.Column("mem_available_mb", sa.Integer(), nullable=True),
        sa.Column("running_run", sa.BigInteger(), nullable=True),
        sa.Column("last_seen", sa.DateTime(), nullable=True),
        sa.Column("created_at", sa.DateTime(), server_default=sa.text("now()"), nullable=False),
        sa.PrimaryKeyConstraint("id"),
        **TABLE,
    )
    op.create_table(
        "test_run",
        sa.Column("id", sa.BigInteger(), autoincrement=True, nullable=False),
        sa.Column("tester_id", sa.SmallInteger(), nullable=True),
        sa.Column("status", sa.String(16), nullable=False),
        sa.Column("spec", sa.JSON(), nullable=False),
        sa.Column("keep_errors", sa.Boolean(), nullable=False),
        sa.Column("stop_requested", sa.Boolean(), nullable=False),
        sa.Column("created_by", sa.String(64), nullable=False),
        sa.Column("created_at", sa.DateTime(), server_default=sa.text("now()"), nullable=False),
        sa.Column("started_at", sa.DateTime(), nullable=True),
        sa.Column("finished_at", sa.DateTime(), nullable=True),
        sa.Column("result", sa.JSON(), nullable=True),
        sa.Column("error", sa.Text(), nullable=True),
        sa.ForeignKeyConstraint(["tester_id"], ["tester.id"], ondelete="SET NULL"),
        sa.PrimaryKeyConstraint("id"),
        **TABLE,
    )


def downgrade() -> None:
    op.drop_table("test_run")
    op.drop_table("tester")
