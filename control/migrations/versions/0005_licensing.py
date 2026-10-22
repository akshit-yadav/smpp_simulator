"""Licensing: worker fingerprints, licence-suspension bookkeeping on gateways,
and the licence row (with the clock high-water mark).

Revision ID: 0005
Revises: 0004
"""

import sqlalchemy as sa
from alembic import op

revision = "0005"
down_revision = "0004"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.add_column("worker", sa.Column("machine_id", sa.String(64), nullable=True))
    op.add_column("worker", sa.Column("mac", sa.String(64), nullable=True))
    op.add_column("gateway", sa.Column("status_before_suspend", sa.String(16), nullable=True))
    op.add_column(
        "gateway",
        sa.Column("licence_keep", sa.Boolean(), nullable=False, server_default=sa.false()),
    )
    licence = op.create_table(
        "licence",
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column("raw", sa.Text(), nullable=True),
        sa.Column("uploaded_at", sa.DateTime(), nullable=True),
        sa.Column("uploaded_by", sa.String(64), nullable=True),
        sa.Column("clock_high_water", sa.DateTime(), nullable=True),
        sa.PrimaryKeyConstraint("id"),
        mysql_engine="InnoDB",
        mysql_charset="utf8mb4",
        mysql_collate="utf8mb4_unicode_ci",
    )
    op.bulk_insert(licence, [{"id": 1}])


def downgrade() -> None:
    op.drop_table("licence")
    op.drop_column("gateway", "licence_keep")
    op.drop_column("gateway", "status_before_suspend")
    op.drop_column("worker", "mac")
    op.drop_column("worker", "machine_id")
