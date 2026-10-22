"""MO generator: one row per gateway with its MO traffic settings.

Revision ID: 0004
Revises: 0003
"""

import sqlalchemy as sa
from alembic import op

revision = "0004"
down_revision = "0003"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_table(
        "mo_generator",
        sa.Column("gateway_id", sa.BigInteger(), nullable=False),
        sa.Column("enabled", sa.Boolean(), nullable=False),
        sa.Column("run_id", sa.BigInteger(), nullable=False),
        sa.Column("mode", sa.String(8), nullable=False),
        sa.Column("rate", sa.Float(), nullable=False),
        sa.Column("burst", sa.BigInteger(), nullable=False),
        sa.Column("start_at", sa.DateTime(), nullable=True),
        sa.Column("stop_after_s", sa.BigInteger(), nullable=False),
        sa.Column("stop_after_count", sa.BigInteger(), nullable=False),
        sa.Column("sources", sa.JSON(), nullable=False),
        sa.Column("destinations", sa.JSON(), nullable=False),
        sa.Column("dest_order", sa.String(16), nullable=False),
        sa.Column("templates", sa.JSON(), nullable=False),
        sa.Column("keywords", sa.JSON(), nullable=False),
        sa.Column("encoding", sa.String(8), nullable=False),
        sa.Column("long_pct", sa.Integer(), nullable=False),
        sa.Column("no_receiver", sa.String(8), nullable=False),
        sa.Column("updated_at", sa.DateTime(), server_default=sa.text("now()"), nullable=False),
        sa.ForeignKeyConstraint(["gateway_id"], ["gateway.id"], ondelete="CASCADE"),
        sa.PrimaryKeyConstraint("gateway_id"),
        mysql_engine="InnoDB",
        mysql_charset="utf8mb4",
        mysql_collate="utf8mb4_unicode_ci",
    )


def downgrade() -> None:
    op.drop_table("mo_generator")
