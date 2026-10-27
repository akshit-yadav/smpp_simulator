"""DLR rules: rename outcome profiles, drop per-gateway custom rules and
per-entry delay overrides, one entry for fixed rules, numeric err: codes.

Revision ID: 0002
Revises: 0001
"""

import sqlalchemy as sa
from alembic import op

revision = "0002"
down_revision = "0001"
branch_labels = None
depends_on = None

RENAMED = [
    ("dlr_rule_entry", "profile_id", "rule_id"),
    ("gateway", "outcome_profile_id", "dlr_rule_id"),
    ("defaults", "outcome_profile_id", "dlr_rule_id"),
]
# The sample custom codes seeded in 0001 get fixed numbers.
SAMPLES = {"T01": "901", "G01": "902"}


def upgrade() -> None:
    conn = op.get_bind()

    # Per-gateway custom profiles become shared rules named after their gateway.
    conn.execute(
        sa.text(
            "UPDATE outcome_profile p JOIN gateway g ON g.id = p.owner_gateway_id "
            "SET p.name = LEFT(CONCAT(g.name, ' rule #', p.id), 128)"
        )
    )
    op.drop_constraint("fk_outcome_profile_owner", "outcome_profile", type_="foreignkey")
    op.drop_column("outcome_profile", "owner_gateway_id")
    op.drop_column("outcome_entry", "dlr_delay_min_ms")
    op.drop_column("outcome_entry", "dlr_delay_max_ms")

    # A fixed rule uses only its first entry; drop the ignored ones.
    conn.execute(
        sa.text(
            "DELETE e FROM outcome_entry e JOIN outcome_profile p ON p.id = e.profile_id "
            "WHERE p.mode = 'fixed' AND e.position > ("
            "  SELECT m FROM (SELECT profile_id, MIN(position) m FROM outcome_entry"
            "  GROUP BY profile_id) f WHERE f.profile_id = e.profile_id)"
        )
    )

    op.rename_table("outcome_profile", "dlr_rule")
    op.rename_table("outcome_entry", "dlr_rule_entry")
    for table, old, new in RENAMED:
        conn.execute(sa.text(f"ALTER TABLE {table} RENAME COLUMN {old} TO {new}"))
        conn.execute(sa.text(f"ALTER TABLE {table} RENAME INDEX {old} TO {new}"))  # FK index

    # err: codes are strictly numeric: give each non-numeric custom code the
    # next free 9xx code and repoint the rules that use it.
    codes = [r[0] for r in conn.execute(sa.text("SELECT code FROM dlr_error"))]
    taken = {c for c in codes if c.isdigit()}
    for code in sorted((c for c in codes if not c.isdigit()), key=lambda c: (c not in SAMPLES, c)):
        new = SAMPLES.get(code)
        if new is None or new in taken:
            new = next(f"{n:03d}" for n in range(901, 1000) if f"{n:03d}" not in taken)
        taken.add(new)
        conn.execute(
            sa.text(
                "INSERT INTO dlr_error (code, label, suggested_status, network_error_type, "
                "network_error_code, dlr_text, `system`) SELECT :new, label, suggested_status, "
                "network_error_type, network_error_code, dlr_text, `system` FROM dlr_error "
                "WHERE code = :old"
            ),
            {"new": new, "old": code},
        )
        conn.execute(
            sa.text("UPDATE dlr_rule_entry SET dlr_error = :new WHERE dlr_error = :old"),
            {"new": new, "old": code},
        )
        conn.execute(sa.text("DELETE FROM dlr_error WHERE code = :old"), {"old": code})


def downgrade() -> None:
    conn = op.get_bind()
    for table, old, new in RENAMED:
        conn.execute(sa.text(f"ALTER TABLE {table} RENAME COLUMN {new} TO {old}"))
        conn.execute(sa.text(f"ALTER TABLE {table} RENAME INDEX {new} TO {old}"))
    op.rename_table("dlr_rule_entry", "outcome_entry")
    op.rename_table("dlr_rule", "outcome_profile")
    op.add_column("outcome_entry", sa.Column("dlr_delay_min_ms", sa.Integer(), nullable=True))
    op.add_column("outcome_entry", sa.Column("dlr_delay_max_ms", sa.Integer(), nullable=True))
    op.add_column("outcome_profile", sa.Column("owner_gateway_id", sa.BigInteger(), nullable=True))
    op.create_foreign_key(
        "fk_outcome_profile_owner",
        "outcome_profile",
        "gateway",
        ["owner_gateway_id"],
        ["id"],
        ondelete="CASCADE",
    )
