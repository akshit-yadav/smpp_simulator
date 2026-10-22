"""Licensing (M8): verifies licence files, works out the limits in force and
enforces them.

A licence file is {"format": "psg-licence-1", "payload": base64 JSON,
"signature": base64 Ed25519 over the payload bytes} (see worker/pkg/licence,
which uses the same key and format). Without a valid licence for every
active worker's machine, the free tier applies.

Every 5 s the scheduler (tick):
- advances the clock high-water mark (a clock set back more than 24 h
  makes the licence count as expired),
- suspends gateways over the limit (licence_keep first, then oldest stay)
  and restores suspended ones when the limit allows,
- splits the TPS cap between online workers by demand and publishes each
  share on psg:licshare:<worker_id>.
"""

import base64
import binascii
import json
import logging
import math
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from pathlib import Path

from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
from redis.exceptions import RedisError
from sqlalchemy import func, select
from sqlalchemy.orm import Session

from psg_control import sync
from psg_control.models import AuditLog, Gateway, LicenceRow, Worker
from psg_control.stores import Stores

log = logging.getLogger(__name__)

FORMAT = "psg-licence-1"
# The public half of HWPL's licence signing key (tools/licgen keygen); the
# same value is built into the worker (worker/pkg/licence).
PUBLIC_KEY = "t2XblfGnqZ798s9cEm1KKmnvtcT0+txOKEIPZz/sSx4="
# The free tier, without a valid licence (keep in step with worker/pkg/licence).
FREE_GATEWAYS = 10
FREE_TPS = 1000
WARN_DAYS = (30, 7, 1)
ROLLBACK = timedelta(hours=24)
SHARE_FLOOR = 0.2  # of the cap, split evenly before the rest follows demand


class LicenceError(ValueError):
    pass


@dataclass
class Licence:
    licence_id: str
    licensee: str
    issued_at: datetime
    expires_at: datetime
    max_gateways: int
    max_tps: int
    machines: list[dict]
    features: list[str]


@dataclass
class State:
    """The limits in force and why."""

    status: str  # licensed | free | expired | invalid | wrong_machine
    reason: str
    max_gateways: int
    max_tps: int
    licence: Licence | None = None
    days_left: int | None = None
    warning: str = ""  # expiry or partial-match warning for the panel
    workers: list[dict] = field(default_factory=list)  # id, name, online, match


def utc(text: str) -> datetime:
    t = datetime.fromisoformat(text)
    return t.replace(tzinfo=UTC) if t.tzinfo is None else t.astimezone(UTC)


def parse(text: str, public_key: str | None = None) -> Licence:
    """Verifies the signature and decodes the licence (expiry and machines
    are checked by evaluate)."""
    try:
        env = json.loads(text)
        if env.get("format") != FORMAT:
            raise LicenceError("This is not a PSG licence file.")
        payload = base64.b64decode(env["payload"], validate=True)
        signature = base64.b64decode(env["signature"], validate=True)
    except (ValueError, KeyError, TypeError, AttributeError, binascii.Error) as exc:
        raise LicenceError("This is not a PSG licence file.") from exc
    try:
        key = Ed25519PublicKey.from_public_bytes(base64.b64decode(public_key or PUBLIC_KEY))
        key.verify(signature, payload)
    except InvalidSignature as exc:
        raise LicenceError(
            "The licence signature is invalid (altered or not issued by HWPL)."
        ) from exc
    try:
        d = json.loads(payload)
        return Licence(
            licence_id=d["licence_id"],
            licensee=d["licensee"],
            issued_at=utc(d["issued_at"]),
            expires_at=utc(d["expires_at"]),
            max_gateways=int(d["max_gateways"]),
            max_tps=int(d["max_tps"]),
            machines=d.get("machines") or [],
            features=d.get("features") or [],
        )
    except (ValueError, KeyError, TypeError) as exc:
        raise LicenceError(f"The licence content is incomplete ({exc}).") from exc


def match(lic: Licence, machine_id: str | None, mac: str | None) -> str:
    """full (machine ID and MAC), partial (one of them) or none."""
    best = "none"
    for m in lic.machines:
        same_id = bool(machine_id) and m.get("machine_id") == machine_id
        same_mac = bool(mac) and m.get("mac") == mac
        if same_id and same_mac:
            return "full"
        if same_id or same_mac:
            best = "partial"
    return best


def row(db: Session) -> LicenceRow:
    r = db.get(LicenceRow, 1)
    if r is None:
        r = LicenceRow(id=1)
        db.add(r)
        db.flush()
    return r


def evaluate(db: Session, stores: Stores, now: datetime, require_both: bool = False) -> State:
    r = row(db)
    workers = [
        {
            "id": w.id,
            "name": w.name,
            "online": sync.worker_online(stores, w.id),
            "machine_id": w.machine_id,
            "mac": w.mac,
            "match": None,
        }
        for w in db.scalars(select(Worker).where(Worker.status == "active").order_by(Worker.id))
    ]

    def free(status: str, reason: str, lic: Licence | None = None) -> State:
        return State(status, reason, FREE_GATEWAYS, FREE_TPS, licence=lic, workers=workers)

    if not r.raw:
        return free("free", "No licence uploaded: the free tier applies.")
    try:
        lic = parse(r.raw)
    except LicenceError as exc:
        return free("invalid", str(exc))
    for w in workers:
        w["match"] = match(lic, w["machine_id"], w["mac"])
    high_water = r.clock_high_water.replace(tzinfo=UTC) if r.clock_high_water else None
    if now > lic.expires_at:
        return free(
            "expired", f"Licence {lic.licence_id} expired on {lic.expires_at:%Y-%m-%d}.", lic
        )
    if high_water and now < high_water - ROLLBACK:
        return free(
            "expired", "The clock went back more than 24 hours; the licence counts as expired.", lic
        )
    unmatched = [
        w["name"]
        for w in workers
        if w["machine_id"] or w["mac"]
        if w["match"] == "none" or (require_both and w["match"] == "partial")
    ]
    if unmatched:
        return free("wrong_machine", f"Not licensed for {', '.join(unmatched)}.", lic)

    days = math.floor((lic.expires_at - now).total_seconds() / 86400)
    warning = ""
    if days < WARN_DAYS[0]:
        plural = "s" if days != 1 else ""
        warning = f"The licence expires in {days} day{plural} ({lic.expires_at:%Y-%m-%d})."
    partial = [w["name"] for w in workers if w["match"] == "partial"]
    if partial:
        warning = (warning + " " if warning else "") + (
            f"{', '.join(partial)} only partly match the licence (machine ID or MAC changed)."
        )
    return State(
        "licensed",
        f"Licence {lic.licence_id} for {lic.licensee}.",
        lic.max_gateways,
        lic.max_tps,
        licence=lic,
        days_left=days,
        warning=warning,
        workers=workers,
    )


def check_upload(
    text: str, db: Session, stores: Stores, now: datetime, require_both: bool
) -> Licence:
    """Refuses a licence that is invalid, expired or for none of the workers."""
    lic = parse(text)
    if now > lic.expires_at:
        raise LicenceError(f"This licence expired on {lic.expires_at:%Y-%m-%d}.")
    known = [w for w in db.scalars(select(Worker)) if w.machine_id or w.mac]
    matches = [match(lic, w.machine_id, w.mac) for w in known]
    if known and all(m == "none" for m in matches):
        raise LicenceError("This licence is for another machine: no registered worker matches it.")
    if known and require_both and "full" not in matches:
        raise LicenceError("No worker fully matches this licence (machine ID and MAC).")
    return lic


def install_from_file(stores: Stores, path: str, require_both: bool) -> str | None:
    """Installs the licence file at path if no licence is installed yet (a
    licence uploaded in the panel always wins). Returns its ID, or None."""
    try:
        text = Path(path).read_text(encoding="utf-8").strip()
    except OSError:
        return None
    with Session(stores.db) as db:
        r = row(db)
        if r.raw:
            return None
        try:
            lic = check_upload(text, db, stores, datetime.now(UTC), require_both)
        except LicenceError as exc:
            log.error("licence file %s not installed: %s", path, exc)
            return None
        r.raw, r.uploaded_at, r.uploaded_by = text, datetime.now(UTC).replace(tzinfo=None), "file"
        db.add(
            AuditLog(
                actor="system",
                action="licence.upload",
                entity_type="licence",
                entity_id=lic.licence_id,
                details={"file": path},
            )
        )
        changes = sync.Changes()
        changes.bump(db, None, None, "licence")
        db.commit()
    changes.publish(stores)
    log.info("licence %s installed from %s", lic.licence_id, path)
    return lic.licence_id


def licensed_count(db: Session) -> int:
    """Gateways that count against the limit: every one not suspended."""
    return db.scalar(select(func.count()).select_from(Gateway).where(Gateway.status != "suspended"))


def enforce_gateways(db: Session, max_gateways: int) -> tuple[list[int], list[int]]:
    """Suspends gateways beyond the limit and restores suspended ones within
    it: licence_keep first, then lowest ID. Returns (suspended, restored) IDs;
    the caller bumps config and commits."""
    order = db.scalars(select(Gateway).order_by(Gateway.licence_keep.desc(), Gateway.id))
    suspended, restored = [], []
    for i, gw in enumerate(order):
        if i >= max_gateways and gw.status != "suspended":
            gw.status_before_suspend, gw.status = gw.status, "suspended"
            gw.version += 1
            suspended.append(gw.id)
        elif i < max_gateways and gw.status == "suspended":
            gw.status, gw.status_before_suspend = gw.status_before_suspend or "active", None
            gw.version += 1
            restored.append(gw.id)
    return suspended, restored


def shares(max_tps: int, demand: dict[int, float]) -> dict[int, float]:
    """Splits the TPS cap between workers: an even floor, the rest by demand
    (evenly when there is none)."""
    if not demand:
        return {}
    n = len(demand)
    floor = max_tps * SHARE_FLOOR / n
    rest = max_tps - floor * n
    total = sum(demand.values())
    return {
        w: round(floor + (rest * d / total if total > 0 else rest / n), 3)
        for w, d in demand.items()
    }


@dataclass
class Ticker:
    """Scheduler state between licence ticks."""

    last_status: str = ""
    last_warning: str = ""
    last_counts: dict[int, tuple[float, int]] = field(default_factory=dict)  # worker -> (ts, count)


def offered(stats: dict) -> int:
    """submit_sm and MO parts a worker has handled (cumulative), its demand."""
    total = 0
    for g in (stats.get("gateways") or {}).values():
        total += sum(
            g.get(k, 0)
            for k in ("accepted", "rejected", "throttled", "invalid", "licence_throttled")
        )
        total += (g.get("mo") or {}).get("sent", 0)
    return total


def tick(stores: Stores, ticker: Ticker, require_both: bool = False) -> State:
    now = datetime.now(UTC)
    with Session(stores.db) as db:
        r = row(db)
        if r.clock_high_water is None or now.replace(tzinfo=None) > r.clock_high_water:
            r.clock_high_water = now.replace(tzinfo=None)
        state = evaluate(db, stores, now, require_both)

        suspended, restored = enforce_gateways(db, state.max_gateways)
        if suspended or restored:
            changes = sync.Changes()
            ids = suspended + restored
            workers = {
                w for (w,) in db.execute(select(Gateway.worker_id).where(Gateway.id.in_(ids)))
            }
            changes.bump(db, workers, None, "licence")
            for action, gids in (("licence.suspend", suspended), ("licence.restore", restored)):
                if gids:
                    db.add(
                        AuditLog(
                            actor="system",
                            action=action,
                            entity_type="gateway",
                            entity_id=",".join(map(str, gids)),
                            details={"reason": state.reason, "max_gateways": state.max_gateways},
                        )
                    )
                    log.warning("%s gateways %s (%s)", action, gids, state.reason)
        db.commit()
        if suspended or restored:
            changes.publish(stores)

    if state.status != ticker.last_status:
        (log.info if state.status == "licensed" else log.warning)(
            "licence: %s (%s)", state.status, state.reason
        )
        ticker.last_status = state.status
    if state.warning and state.warning != ticker.last_warning:
        log.warning("licence: %s", state.warning)
    ticker.last_warning = state.warning
    publish_shares(stores, ticker, state)
    return state


def publish_shares(stores: Stores, ticker: Ticker, state: State) -> None:
    now = datetime.now(UTC).timestamp()
    demand = {}
    for w in state.workers:
        if not w["online"]:
            continue
        stats = sync.live_json(stores, f"psg:stats:{w['id']}") or {}
        count = offered(stats)
        prev = ticker.last_counts.get(w["id"])
        ticker.last_counts[w["id"]] = (now, count)
        dt = now - prev[0] if prev else 0
        demand[w["id"]] = max(0.0, (count - prev[1]) / dt) if prev and dt > 0 else 0.0
    try:
        for wid, tps in shares(state.max_tps, demand).items():
            stores.redis.set(
                f"psg:licshare:{wid}",
                json.dumps({"tps": tps, "max_tps": state.max_tps, "ts": int(now * 1000)}),
                ex=60,
            )
    except RedisError as exc:
        log.warning("licence share publish failed: %s", type(exc).__name__)
