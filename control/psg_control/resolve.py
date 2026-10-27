"""Turns rows into effective settings and into the config workers consume
(the JSON shape of the worker's gateway.Config)."""

from sqlalchemy import func, select
from sqlalchemy.orm import Session

from psg_control.models import (
    SETTING_COLUMNS,
    Defaults,
    DLRError,
    DLRRule,
    Gateway,
    LicenceRow,
    MOGenerator,
    Worker,
)
from psg_control.security import Cipher


def defaults(db: Session) -> Defaults:
    return db.get(Defaults, 1)


def raw_settings(gw: Gateway) -> dict:
    return {c: getattr(gw, c) for c in SETTING_COLUMNS}


def effective(gw: Gateway | dict, d: Defaults) -> dict:
    """Gateway settings with every NULL replaced by the global default."""
    raw = gw if isinstance(gw, dict) else raw_settings(gw)
    return {c: raw.get(c) if raw.get(c) is not None else getattr(d, c) for c in SETTING_COLUMNS}


def outcome_config(rule: DLRRule, errors: dict[str, DLRError]) -> dict:
    """A DLR rule in the worker's outcome-profile shape."""
    outcomes = []
    for e in rule.entries:
        o = {"name": e.name, "command_status": e.command_status, "weight_bp": e.weight_bp}
        if e.command_status == 0:
            err = errors.get(e.dlr_error or "000")
            o["dlr_status"] = e.dlr_status
            o["dlr_error"] = e.dlr_error or "000"
            if err is not None and err.network_error_type is not None:
                o["network_error_code"] = {
                    "type": err.network_error_type,
                    "code": err.network_error_code,
                }
            if err is not None and err.dlr_text:
                o["dlr_text"] = err.dlr_text
        outcomes.append(o)
    out = {
        "name": rule.name,
        "mode": rule.mode,
        "distribution": rule.distribution,
        "outcomes": outcomes,
    }
    if rule.seed is not None:
        out["seed"] = rule.seed
    return out


def mo_config(mo: MOGenerator) -> dict:
    """An MO generator row in the worker's gateway.MO shape."""
    src = mo.sources
    out = {
        "enabled": mo.enabled,
        "run_id": mo.run_id,
        "mode": mo.mode,
        "rate": mo.rate,
        "burst": mo.burst,
        "stop_after_s": mo.stop_after_s,
        "stop_after_count": mo.stop_after_count,
        "sources": {
            "kind": src["kind"],
            "list": src.get("numbers") or [],
            "from": src.get("range_from") or "",
            "to": src.get("range_to") or "",
            "prefix": src.get("prefix") or "",
            "digits": src.get("digits") or 0,
        },
        "destinations": mo.destinations,
        "dest_order": mo.dest_order,
        "templates": mo.templates,
        "keywords": mo.keywords,
        "encoding": mo.encoding,
        "long_pct": mo.long_pct,
        "no_receiver": mo.no_receiver,
    }
    if mo.start_at is not None:
        out["start_at"] = mo.start_at.strftime("%Y-%m-%dT%H:%M:%SZ")
    return out


def gateway_config(
    gw: Gateway, eff: dict, outcome: dict, cipher: Cipher, mo: MOGenerator | None = None
) -> dict:
    cfg = {
        "id": gw.id,
        "name": gw.name,
        "port": gw.port,
        "system_id": gw.system_id,
        "password": cipher.decrypt(gw.password_enc),
        "status": gw.status,
        "max_binds": eff["max_binds"],
        "bind_types": eff["bind_types"],
        "tps": {"limit": eff["tps_limit"], "scope": eff["tps_scope"]},
        "dlr": {
            "enabled": eff["dlr_enabled"],
            "timing": eff["dlr_timing"],
            "delay_ms": eff["dlr_delay_ms"],
            "min_ms": eff["dlr_min_ms"],
            "max_ms": eff["dlr_max_ms"],
            "id_format": eff["dlr_id_format"],
            "date_seconds": eff["dlr_date_seconds"],
            "include_text": eff["dlr_include_text"],
            "include_tlvs": eff["dlr_include_tlvs"],
            "intermediate": eff["dlr_intermediate"],
            "intermediate_status": eff["dlr_intermediate_status"],
            "intermediate_delay_ms": eff["dlr_intermediate_delay_ms"],
        },
        "response_latency_ms": eff["response_latency_ms"],
        "outcome": outcome,
        "version": gw.version,
    }
    if mo is not None:
        cfg["mo"] = mo_config(mo)
    return cfg


def worker_config(db: Session, cipher: Cipher, worker: Worker) -> dict:
    """Everything a worker needs to run its gateways, with passwords decrypted."""
    d = defaults(db)
    errors = {e.code: e for e in db.scalars(select(DLRError))}
    rules: dict[int, dict] = {}
    gateways = []
    rows = list(
        db.scalars(select(Gateway).where(Gateway.worker_id == worker.id).order_by(Gateway.id))
    )
    mos = {
        m.gateway_id: m
        for m in db.scalars(
            select(MOGenerator).where(MOGenerator.gateway_id.in_([g.id for g in rows]))
        )
    }
    for gw in rows:
        eff = effective(gw, d)
        rid = eff["dlr_rule_id"]
        if rid not in rules:
            rules[rid] = outcome_config(db.get(DLRRule, rid), errors)
        gateways.append(gateway_config(gw, eff, rules[rid], cipher, mos.get(gw.id)))
    lic = db.get(LicenceRow, 1)
    active = db.scalar(select(func.count()).select_from(Worker).where(Worker.status == "active"))
    return {
        "worker_id": worker.id,
        "version": worker.config_version,
        "gateways": gateways,
        # The worker re-checks the licence itself (signature, expiry, machine).
        "licence": {"file": (lic.raw if lic else None) or "", "workers": max(active, 1)},
    }
