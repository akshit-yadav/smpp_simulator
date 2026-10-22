"""MariaDB schema (InnoDB, utf8mb4). Migrations live in migrations/versions."""

from datetime import datetime

from sqlalchemy import (
    JSON,
    VARBINARY,
    BigInteger,
    Boolean,
    DateTime,
    Float,
    ForeignKey,
    Integer,
    SmallInteger,
    String,
    Text,
    UniqueConstraint,
    func,
)
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column, relationship


class Base(DeclarativeBase):
    pass


class Admin(Base):
    __tablename__ = "admin"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    username: Mapped[str] = mapped_column(String(64), unique=True)
    password_hash: Mapped[str] = mapped_column(String(255))
    totp_secret_enc: Mapped[bytes | None] = mapped_column(VARBINARY(256))
    totp_pending_enc: Mapped[bytes | None] = mapped_column(VARBINARY(256))
    totp_last_step: Mapped[int | None] = mapped_column(BigInteger)  # blocks code replay
    failed_logins: Mapped[int] = mapped_column(Integer, default=0)
    locked_until: Mapped[datetime | None] = mapped_column(DateTime)
    created_at: Mapped[datetime] = mapped_column(DateTime, server_default=func.now())
    updated_at: Mapped[datetime] = mapped_column(
        DateTime, server_default=func.now(), onupdate=func.now()
    )


class AdminSession(Base):
    __tablename__ = "admin_session"

    id: Mapped[int] = mapped_column(BigInteger, primary_key=True)
    token_hash: Mapped[str] = mapped_column(String(64), unique=True)  # sha256 of the cookie
    admin_id: Mapped[int] = mapped_column(ForeignKey("admin.id", ondelete="CASCADE"))
    created_at: Mapped[datetime] = mapped_column(DateTime, server_default=func.now())
    expires_at: Mapped[datetime] = mapped_column(DateTime)
    ip: Mapped[str | None] = mapped_column(String(64))
    user_agent: Mapped[str | None] = mapped_column(String(255))


class AuditLog(Base):
    __tablename__ = "audit_log"

    id: Mapped[int] = mapped_column(BigInteger, primary_key=True)
    ts: Mapped[datetime] = mapped_column(DateTime, server_default=func.now(), index=True)
    actor: Mapped[str] = mapped_column(String(64))
    action: Mapped[str] = mapped_column(String(64), index=True)
    entity_type: Mapped[str | None] = mapped_column(String(32))
    entity_id: Mapped[str | None] = mapped_column(String(64))
    ip: Mapped[str | None] = mapped_column(String(64))
    details: Mapped[dict | None] = mapped_column(JSON)


class Worker(Base):
    """A worker host. Rows are created from heartbeats; admins can rename or drain."""

    __tablename__ = "worker"

    id: Mapped[int] = mapped_column(SmallInteger, primary_key=True, autoincrement=False)
    name: Mapped[str] = mapped_column(String(64))
    ip: Mapped[str] = mapped_column(String(64))
    port_min: Mapped[int] = mapped_column(Integer, default=20000)
    port_max: Mapped[int] = mapped_column(Integer, default=20999)
    port_exclude: Mapped[list] = mapped_column(JSON, default=list)
    status: Mapped[str] = mapped_column(String(16), default="active")  # active | draining
    version: Mapped[str | None] = mapped_column(String(32))
    config_version: Mapped[int] = mapped_column(BigInteger, default=1)
    last_seen: Mapped[datetime | None] = mapped_column(DateTime)
    created_at: Mapped[datetime] = mapped_column(DateTime, server_default=func.now())
    # Licence fingerprint from the heartbeat: SHA-256 of /etc/machine-id and of
    # the default-route MAC.
    machine_id: Mapped[str | None] = mapped_column(String(64))
    mac: Mapped[str | None] = mapped_column(String(64))


class CommandStatus(Base):
    """SMPP command_status catalog: the read-only SMPP 3.4 list plus vendor codes."""

    __tablename__ = "command_status"

    code: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=False)
    name: Mapped[str] = mapped_column(String(64))
    description: Mapped[str] = mapped_column(String(255), default="")
    system: Mapped[bool] = mapped_column(Boolean, default=False)


class DLRError(Base):
    """DLR err: catalog. System codes are read-only; custom codes are admin-defined."""

    __tablename__ = "dlr_error"

    code: Mapped[str] = mapped_column(String(3), primary_key=True)
    label: Mapped[str] = mapped_column(String(128))
    suggested_status: Mapped[str] = mapped_column(String(8), default="UNDELIV")
    network_error_type: Mapped[int | None] = mapped_column(SmallInteger)
    network_error_code: Mapped[int | None] = mapped_column(Integer)
    dlr_text: Mapped[str | None] = mapped_column(String(64))
    system: Mapped[bool] = mapped_column(Boolean, default=False)


class DLRRule(Base):
    """A named, reusable mix of results for accepted and rejected messages."""

    __tablename__ = "dlr_rule"

    id: Mapped[int] = mapped_column(BigInteger, primary_key=True)
    name: Mapped[str] = mapped_column(String(128), unique=True)
    description: Mapped[str] = mapped_column(String(255), default="")
    mode: Mapped[str] = mapped_column(String(16))  # fixed | random | weighted
    distribution: Mapped[str] = mapped_column(String(16), default="probabilistic")
    seed: Mapped[int | None] = mapped_column(BigInteger)
    created_at: Mapped[datetime] = mapped_column(DateTime, server_default=func.now())
    updated_at: Mapped[datetime] = mapped_column(
        DateTime, server_default=func.now(), onupdate=func.now()
    )

    entries: Mapped[list["DLRRuleEntry"]] = relationship(
        back_populates="rule",
        cascade="all, delete-orphan",
        order_by="DLRRuleEntry.position",
        lazy="selectin",
    )


class DLRRuleEntry(Base):
    __tablename__ = "dlr_rule_entry"

    id: Mapped[int] = mapped_column(BigInteger, primary_key=True)
    rule_id: Mapped[int] = mapped_column(ForeignKey("dlr_rule.id", ondelete="CASCADE"))
    position: Mapped[int] = mapped_column(Integer)
    name: Mapped[str] = mapped_column(String(64), default="")
    command_status: Mapped[int] = mapped_column(ForeignKey("command_status.code"), default=0)
    dlr_status: Mapped[str | None] = mapped_column(String(8))
    dlr_error: Mapped[str | None] = mapped_column(ForeignKey("dlr_error.code"))
    weight_bp: Mapped[int] = mapped_column(Integer, default=0)

    rule: Mapped[DLRRule] = relationship(back_populates="entries")


# Gateway settings that follow the global default when NULL.
SETTING_COLUMNS = (
    "max_binds",
    "bind_types",
    "tps_limit",
    "tps_scope",
    "dlr_enabled",
    "dlr_timing",
    "dlr_delay_ms",
    "dlr_min_ms",
    "dlr_max_ms",
    "dlr_id_format",
    "dlr_date_seconds",
    "dlr_include_text",
    "dlr_include_tlvs",
    "dlr_intermediate",
    "dlr_intermediate_status",
    "dlr_intermediate_delay_ms",
    "response_latency_ms",
    "dlr_rule_id",
)


class Defaults(Base):
    """Global gateway defaults (a single row, id = 1)."""

    __tablename__ = "defaults"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    max_binds: Mapped[int] = mapped_column(Integer, default=10)
    bind_types: Mapped[list] = mapped_column(JSON)
    tps_limit: Mapped[int] = mapped_column(Integer, default=0)
    tps_scope: Mapped[str] = mapped_column(String(8), default="bind")
    dlr_enabled: Mapped[bool] = mapped_column(Boolean, default=True)
    dlr_timing: Mapped[str] = mapped_column(String(16), default="random")
    dlr_delay_ms: Mapped[int] = mapped_column(Integer, default=1000)
    dlr_min_ms: Mapped[int] = mapped_column(Integer, default=500)
    dlr_max_ms: Mapped[int] = mapped_column(Integer, default=2000)
    dlr_id_format: Mapped[str] = mapped_column(String(8), default="hex")
    dlr_date_seconds: Mapped[bool] = mapped_column(Boolean, default=False)
    dlr_include_text: Mapped[bool] = mapped_column(Boolean, default=True)
    dlr_include_tlvs: Mapped[bool] = mapped_column(Boolean, default=True)
    dlr_intermediate: Mapped[str] = mapped_column(String(16), default="off")
    dlr_intermediate_status: Mapped[str] = mapped_column(String(8), default="ENROUTE")
    dlr_intermediate_delay_ms: Mapped[int] = mapped_column(Integer, default=0)
    response_latency_ms: Mapped[int] = mapped_column(Integer, default=0)
    dlr_rule_id: Mapped[int] = mapped_column(ForeignKey("dlr_rule.id"))
    updated_at: Mapped[datetime] = mapped_column(
        DateTime, server_default=func.now(), onupdate=func.now()
    )


class Gateway(Base):
    __tablename__ = "gateway"
    __table_args__ = (UniqueConstraint("worker_id", "port", name="uq_gateway_worker_port"),)

    id: Mapped[int] = mapped_column(BigInteger, primary_key=True)
    name: Mapped[str] = mapped_column(String(128), unique=True)
    worker_id: Mapped[int] = mapped_column(ForeignKey("worker.id"))
    port: Mapped[int] = mapped_column(Integer)
    system_id: Mapped[str] = mapped_column(String(15))
    password_enc: Mapped[bytes] = mapped_column(VARBINARY(128))  # AES-256-GCM
    status: Mapped[str] = mapped_column(String(16), default="active")
    # Suspended by the licence: the status to restore, and whether the admin
    # picked this gateway to stay active when over the limit.
    status_before_suspend: Mapped[str | None] = mapped_column(String(16))
    licence_keep: Mapped[bool] = mapped_column(Boolean, default=False)
    notes: Mapped[str] = mapped_column(Text, default="")
    version: Mapped[int] = mapped_column(BigInteger, default=1)
    created_at: Mapped[datetime] = mapped_column(DateTime, server_default=func.now())
    updated_at: Mapped[datetime] = mapped_column(
        DateTime, server_default=func.now(), onupdate=func.now()
    )

    # Settings: NULL = follow Defaults.
    max_binds: Mapped[int | None] = mapped_column(Integer)
    bind_types: Mapped[list | None] = mapped_column(JSON)
    tps_limit: Mapped[int | None] = mapped_column(Integer)
    tps_scope: Mapped[str | None] = mapped_column(String(8))
    dlr_enabled: Mapped[bool | None] = mapped_column(Boolean)
    dlr_timing: Mapped[str | None] = mapped_column(String(16))
    dlr_delay_ms: Mapped[int | None] = mapped_column(Integer)
    dlr_min_ms: Mapped[int | None] = mapped_column(Integer)
    dlr_max_ms: Mapped[int | None] = mapped_column(Integer)
    dlr_id_format: Mapped[str | None] = mapped_column(String(8))
    dlr_date_seconds: Mapped[bool | None] = mapped_column(Boolean)
    dlr_include_text: Mapped[bool | None] = mapped_column(Boolean)
    dlr_include_tlvs: Mapped[bool | None] = mapped_column(Boolean)
    dlr_intermediate: Mapped[str | None] = mapped_column(String(16))
    dlr_intermediate_status: Mapped[str | None] = mapped_column(String(8))
    dlr_intermediate_delay_ms: Mapped[int | None] = mapped_column(Integer)
    response_latency_ms: Mapped[int | None] = mapped_column(Integer)
    dlr_rule_id: Mapped[int | None] = mapped_column(ForeignKey("dlr_rule.id", ondelete="SET NULL"))

    worker: Mapped[Worker] = relationship(lazy="joined")


class MOGenerator(Base):
    """A gateway's MO traffic generator (M7). One row per gateway, created on
    first save; run_id goes up on every Start so the worker can tell runs apart."""

    __tablename__ = "mo_generator"

    gateway_id: Mapped[int] = mapped_column(
        ForeignKey("gateway.id", ondelete="CASCADE"), primary_key=True
    )
    enabled: Mapped[bool] = mapped_column(Boolean, default=False)
    run_id: Mapped[int] = mapped_column(BigInteger, default=0)
    mode: Mapped[str] = mapped_column(String(8), default="rate")  # rate | burst
    rate: Mapped[float] = mapped_column(Float, default=1.0)
    burst: Mapped[int] = mapped_column(BigInteger, default=100)
    start_at: Mapped[datetime | None] = mapped_column(DateTime)  # naive UTC; NULL = on Start
    stop_after_s: Mapped[int] = mapped_column(BigInteger, default=0)  # 0 = no limit
    stop_after_count: Mapped[int] = mapped_column(BigInteger, default=0)
    sources: Mapped[dict] = mapped_column(JSON)  # schemas.MOSources
    destinations: Mapped[list] = mapped_column(JSON)
    dest_order: Mapped[str] = mapped_column(String(16), default="round_robin")
    templates: Mapped[list] = mapped_column(JSON)  # [{text, weight}]
    keywords: Mapped[list] = mapped_column(JSON)
    encoding: Mapped[str] = mapped_column(String(8), default="gsm7")
    long_pct: Mapped[int] = mapped_column(Integer, default=0)
    no_receiver: Mapped[str] = mapped_column(String(8), default="queue")
    updated_at: Mapped[datetime] = mapped_column(
        DateTime, server_default=func.now(), onupdate=func.now()
    )


class LicenceRow(Base):
    """The uploaded licence (a single row, id = 1) and the clock high-water mark."""

    __tablename__ = "licence"

    id: Mapped[int] = mapped_column(Integer, primary_key=True)
    raw: Mapped[str | None] = mapped_column(Text)  # the licence file as uploaded
    uploaded_at: Mapped[datetime | None] = mapped_column(DateTime)
    uploaded_by: Mapped[str | None] = mapped_column(String(64))
    clock_high_water: Mapped[datetime | None] = mapped_column(DateTime)  # naive UTC


class Tester(Base):
    """An SMPP client simulator agent (psg-tester), registered by its heartbeat."""

    __tablename__ = "tester"

    id: Mapped[int] = mapped_column(SmallInteger, primary_key=True, autoincrement=False)
    name: Mapped[str] = mapped_column(String(64))
    ip: Mapped[str] = mapped_column(String(64), default="")
    version: Mapped[str | None] = mapped_column(String(32))
    mem_available_mb: Mapped[int | None] = mapped_column(Integer)
    running_run: Mapped[int | None] = mapped_column(BigInteger)
    last_seen: Mapped[datetime | None] = mapped_column(DateTime)
    created_at: Mapped[datetime] = mapped_column(DateTime, server_default=func.now())


class TestRun(Base):
    """A simulator test run: what to send (spec, without passwords), its state
    and, when finished, the result. Per-PDU data never reaches the control
    plane; errors live in Redis for 24 h at most."""

    __tablename__ = "test_run"
    __test__ = False  # not a pytest test class

    id: Mapped[int] = mapped_column(BigInteger, primary_key=True)
    tester_id: Mapped[int | None] = mapped_column(ForeignKey("tester.id", ondelete="SET NULL"))
    status: Mapped[str] = mapped_column(
        String(16), default="queued"
    )  # queued|running|done|stopped|failed
    spec: Mapped[dict] = mapped_column(JSON)
    keep_errors: Mapped[bool] = mapped_column(Boolean, default=True)
    stop_requested: Mapped[bool] = mapped_column(Boolean, default=False)
    created_by: Mapped[str] = mapped_column(String(64))
    created_at: Mapped[datetime] = mapped_column(DateTime, server_default=func.now())
    started_at: Mapped[datetime | None] = mapped_column(DateTime)
    finished_at: Mapped[datetime | None] = mapped_column(DateTime)
    result: Mapped[dict | None] = mapped_column(JSON)
    error: Mapped[str | None] = mapped_column(Text)
