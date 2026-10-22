"""Request and response models for the REST API."""

import re
from datetime import datetime
from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, Field, field_validator, model_validator

from psg_control.catalog import DLR_STATUSES

BindType = Literal["tx", "rx", "trx"]
DlrStatus = Literal[
    "ENROUTE", "DELIVRD", "EXPIRED", "DELETED", "UNDELIV", "ACCEPTD", "UNKNOWN", "REJECTD"
]
Ms = Annotated[int, Field(ge=0, le=86_400_000)]  # up to 24 h
Intermediate = Literal["off", "requested", "always"]
IntermediateStatus = Literal["ENROUTE", "ACCEPTD"]

if set(DlrStatus.__args__) != set(DLR_STATUSES):
    raise RuntimeError("DlrStatus is out of sync with catalog.DLR_STATUSES")


class Strict(BaseModel):
    model_config = ConfigDict(extra="forbid")


# ---- auth ---------------------------------------------------------------------


class LoginIn(Strict):
    username: str = Field(min_length=1, max_length=64)
    password: str = Field(min_length=1, max_length=256)
    totp_code: str | None = Field(default=None, pattern=r"^\d{6}$")


class MeOut(BaseModel):
    username: str
    totp_enabled: bool


class PasswordChangeIn(Strict):
    current_password: str
    new_password: str = Field(min_length=10, max_length=256)


class PasswordConfirmIn(Strict):
    password: str


class TotpSetupOut(BaseModel):
    secret: str
    otpauth_uri: str


class TotpCodeIn(Strict):
    code: str = Field(pattern=r"^\d{6}$")


class TotpDisableIn(Strict):
    password: str
    code: str = Field(pattern=r"^\d{6}$")


# ---- gateway settings -----------------------------------------------------------


class Settings(Strict):
    """Gateway settings. On a gateway, null means "follow the global default"."""

    max_binds: int | None = Field(default=None, ge=0, le=1000)
    bind_types: list[BindType] | None = Field(default=None, min_length=1, max_length=3)
    tps_limit: int | None = Field(default=None, ge=0, le=1_000_000)
    tps_scope: Literal["bind", "gateway"] | None = None
    dlr_enabled: bool | None = None
    dlr_timing: Literal["immediate", "fixed", "random"] | None = None
    dlr_delay_ms: Ms | None = None
    dlr_min_ms: Ms | None = None
    dlr_max_ms: Ms | None = None
    dlr_id_format: Literal["hex", "decimal"] | None = None
    dlr_date_seconds: bool | None = None
    dlr_include_text: bool | None = None
    dlr_include_tlvs: bool | None = None
    dlr_intermediate: Intermediate | None = None
    dlr_intermediate_status: IntermediateStatus | None = None
    dlr_intermediate_delay_ms: Ms | None = None
    response_latency_ms: int | None = Field(default=None, ge=0, le=60_000)
    dlr_rule_id: int | None = None

    @field_validator("bind_types")
    @classmethod
    def unique_bind_types(cls, v):
        if v is not None and len(set(v)) != len(v):
            raise ValueError("bind_types must not repeat")
        return v


class DefaultsIn(Strict):
    max_binds: int = Field(ge=0, le=1000)
    bind_types: list[BindType] = Field(min_length=1, max_length=3)
    tps_limit: int = Field(ge=0, le=1_000_000)
    tps_scope: Literal["bind", "gateway"]
    dlr_enabled: bool
    dlr_timing: Literal["immediate", "fixed", "random"]
    dlr_delay_ms: Ms
    dlr_min_ms: Ms
    dlr_max_ms: Ms
    dlr_id_format: Literal["hex", "decimal"]
    dlr_date_seconds: bool
    dlr_include_text: bool
    dlr_include_tlvs: bool
    dlr_intermediate: Intermediate = "off"
    dlr_intermediate_status: IntermediateStatus = "ENROUTE"
    dlr_intermediate_delay_ms: Ms = 0
    response_latency_ms: int = Field(ge=0, le=60_000)
    dlr_rule_id: int

    @model_validator(mode="after")
    def check_range(self):
        if self.dlr_min_ms > self.dlr_max_ms:
            raise ValueError("dlr_min_ms must be <= dlr_max_ms")
        return self


class DefaultsOut(DefaultsIn):
    model_config = ConfigDict(from_attributes=True, extra="ignore")
    updated_at: datetime | None = None


# ---- DLR rules ---------------------------------------------------------------------


class EntryIn(Strict):
    name: str = Field(default="", max_length=64)
    command_status: int = Field(default=0, ge=0, le=0xFFFFFFFF)
    dlr_status: DlrStatus | None = None
    dlr_error: str | None = Field(default=None, pattern=r"^\d{3}$")
    weight_bp: int = Field(default=0, ge=0, le=10_000)

    @model_validator(mode="after")
    def check(self):
        if self.command_status == 0:
            if self.dlr_status is None:
                raise ValueError("dlr_status is required for an accepted entry (command_status 0)")
            self.dlr_error = self.dlr_error or "000"
        else:  # rejected: no message ID, no DLR
            self.dlr_status = self.dlr_error = None
        return self


class RuleIn(Strict):
    name: str = Field(min_length=1, max_length=128)
    description: str = Field(default="", max_length=255)
    mode: Literal["fixed", "random", "weighted"]
    distribution: Literal["probabilistic", "exact"] = "probabilistic"
    seed: int | None = Field(default=None, ge=0, le=2**63 - 1)
    entries: list[EntryIn] = Field(min_length=1, max_length=50)

    @model_validator(mode="after")
    def check_mode(self):
        if self.mode == "fixed":
            if len(self.entries) != 1:
                raise ValueError("a fixed rule uses exactly one entry; remove the others")
            self.seed = None  # nothing random to seed
        if self.mode == "weighted":
            total = sum(e.weight_bp for e in self.entries)
            if total != 10_000:
                raise ValueError(f"weights total {total / 100:g}%; they must total 100% (10000 bp)")
        return self


class EntryOut(EntryIn):
    model_config = ConfigDict(from_attributes=True, extra="ignore")


class RuleOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: int
    name: str
    description: str
    mode: str
    distribution: str
    seed: int | None
    entries: list[EntryOut]
    created_at: datetime
    updated_at: datetime


class PreviewIn(Strict):
    rule: RuleIn | None = None
    rule_id: int | None = None
    messages: int = Field(default=10_000, ge=1, le=1_000_000)


# ---- gateways ---------------------------------------------------------------------

SYSTEM_ID_RE = re.compile(r"^[\x21-\x7e]{1,15}$")  # printable, no spaces


class GatewayBase(Settings):
    @field_validator("system_id", check_fields=False)
    @classmethod
    def printable_system_id(cls, v):
        if v is not None and not SYSTEM_ID_RE.match(v):
            raise ValueError("system_id must be 1-15 printable characters without spaces")
        return v

    @field_validator("password", check_fields=False)
    @classmethod
    def printable_password(cls, v):
        if v is not None and not re.match(r"^[\x20-\x7e]{1,8}$", v):
            raise ValueError("password must be 1-8 printable ASCII characters")
        return v


class GatewayCreate(GatewayBase):
    name: str = Field(min_length=1, max_length=128)
    worker_id: int | None = Field(default=None, ge=0, le=255)
    port: int = Field(ge=1, le=65535)
    system_id: str
    password: str
    status: Literal["active", "disabled"] = "active"
    notes: str = Field(default="", max_length=2000)


class GatewayUpdate(GatewayBase):
    name: str | None = Field(default=None, min_length=1, max_length=128)
    worker_id: int | None = Field(default=None, ge=0, le=255)
    port: int | None = Field(default=None, ge=1, le=65535)
    system_id: str | None = None
    password: str | None = None
    status: Literal["active", "disabled"] | None = None
    notes: str | None = Field(default=None, max_length=2000)


class GatewayOut(BaseModel):
    id: int
    name: str
    worker_id: int
    worker_name: str
    ip: str
    port: int
    system_id: str
    password: str = "********"  # noqa: S105 - always masked
    status: str
    notes: str
    version: int
    settings: Settings
    effective: dict
    dlr_rule_name: str
    runtime: dict | None
    warnings: list[str] = []
    created_at: datetime
    updated_at: datetime


# ---- catalogs ---------------------------------------------------------------------


class CommandStatusIn(Strict):
    code: int = Field(ge=0x400, le=0x4FF, description="Vendor codes only: 0x400-0x4FF")
    name: str = Field(min_length=1, max_length=64)
    description: str = Field(default="", max_length=255)


class CommandStatusPatch(Strict):
    name: str | None = Field(default=None, min_length=1, max_length=64)
    description: str | None = Field(default=None, max_length=255)


class CommandStatusOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    code: int
    hex: str = ""
    name: str
    description: str
    system: bool

    @model_validator(mode="after")
    def set_hex(self):
        self.hex = f"0x{self.code:08X}"
        return self


class DlrErrorIn(Strict):
    code: str = Field(pattern=r"^\d{1,3}$", description="Numeric, stored as 3 digits (7 -> 007)")
    label: str = Field(min_length=1, max_length=128)
    suggested_status: DlrStatus = "UNDELIV"
    network_error_type: int | None = Field(default=None, ge=0, le=255)
    network_error_code: int | None = Field(default=None, ge=0, le=65535)
    dlr_text: str | None = Field(default=None, max_length=64)

    @field_validator("code")
    @classmethod
    def three_digits(cls, v: str) -> str:
        return v.zfill(3)

    @model_validator(mode="after")
    def both_or_neither(self):
        if (self.network_error_type is None) != (self.network_error_code is None):
            raise ValueError("set both network_error_type and network_error_code, or neither")
        return self


class DlrErrorPatch(Strict):
    label: str | None = Field(default=None, min_length=1, max_length=128)
    suggested_status: DlrStatus | None = None
    network_error_type: int | None = Field(default=None, ge=0, le=255)
    network_error_code: int | None = Field(default=None, ge=0, le=65535)
    dlr_text: str | None = Field(default=None, max_length=64)


class DlrErrorOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    code: str
    label: str
    suggested_status: str
    network_error_type: int | None
    network_error_code: int | None
    dlr_text: str | None
    system: bool


# ---- workers & audit ----------------------------------------------------------------


class WorkerOut(BaseModel):
    id: int
    name: str
    ip: str
    port_min: int
    port_max: int
    port_exclude: list[int]
    status: str
    version: str | None
    config_version: int
    online: bool
    last_seen: datetime | None
    gateways: int


class WorkerPatch(Strict):
    name: str | None = Field(default=None, min_length=1, max_length=64)
    status: Literal["active", "draining"] | None = None


class AuditOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: int
    ts: datetime
    actor: str
    action: str
    entity_type: str | None
    entity_id: str | None
    ip: str | None
    details: dict | None
