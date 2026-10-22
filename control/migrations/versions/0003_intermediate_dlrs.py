"""Intermediate DLRs: an ENROUTE/ACCEPTD receipt before the final one.

Revision ID: 0003
Revises: 0002
"""

import sqlalchemy as sa
from alembic import op

revision = "0003"
down_revision = "0002"
branch_labels = None
depends_on = None


def upgrade() -> None:
    # Defaults: off, so existing gateways behave as before.
    op.add_column(
        "defaults",
        sa.Column("dlr_intermediate", sa.String(16), nullable=False, server_default="off"),
    )
    op.add_column(
        "defaults",
        sa.Column(
            "dlr_intermediate_status", sa.String(8), nullable=False, server_default="ENROUTE"
        ),
    )
    op.add_column(
        "defaults",
        sa.Column("dlr_intermediate_delay_ms", sa.Integer(), nullable=False, server_default="0"),
    )
    # Gateways: NULL follows the default.
    op.add_column("gateway", sa.Column("dlr_intermediate", sa.String(16), nullable=True))
    op.add_column("gateway", sa.Column("dlr_intermediate_status", sa.String(8), nullable=True))
    op.add_column("gateway", sa.Column("dlr_intermediate_delay_ms", sa.Integer(), nullable=True))


def downgrade() -> None:
    for table in ("gateway", "defaults"):
        for col in ("dlr_intermediate_delay_ms", "dlr_intermediate_status", "dlr_intermediate"):
            op.drop_column(table, col)
