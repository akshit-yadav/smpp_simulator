"""DLR rule entries: one entry per position, so concurrent saves cannot merge
two rules' entries (weights totalling 200%); shares only in weighted rules.

Revision ID: 0007
Revises: 0006
"""

import sqlalchemy as sa
from alembic import op

revision = "0007"
down_revision = "0006"
branch_labels = None
depends_on = None


def upgrade() -> None:
    # A rule already merged by concurrent saves keeps its first copy of each position.
    op.get_bind().execute(
        sa.text(
            "DELETE e FROM dlr_rule_entry e JOIN dlr_rule_entry first "
            "ON first.rule_id = e.rule_id AND first.position = e.position AND first.id < e.id"
        )
    )
    # Fixed and random rules ignore shares; clear them so none reappear later.
    op.get_bind().execute(
        sa.text(
            "UPDATE dlr_rule_entry e JOIN dlr_rule r ON r.id = e.rule_id "
            "SET e.weight_bp = 0 WHERE r.mode <> 'weighted'"
        )
    )
    op.create_unique_constraint(
        "uq_dlr_rule_entry_position", "dlr_rule_entry", ["rule_id", "position"]
    )


def downgrade() -> None:
    op.drop_constraint("uq_dlr_rule_entry_position", "dlr_rule_entry", type_="unique")
