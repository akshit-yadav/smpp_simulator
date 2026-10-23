"""SMPP client simulator (M10): test runs against PSG's own gateways.

The admin queues a run here (router); a psg-tester agent picks it up and
reports back over the tester API (agent_router, PSG_TESTER_TOKEN only). The
agent always calls in, so it can run on another machine. Per-PDU data stays
on the tester and is deleted after the run; the error log lives in Redis for
at most 24 h; the result is kept until the run is deleted.
"""

import hmac
import json
import logging
from datetime import UTC, datetime, timedelta

from fastapi import APIRouter, Depends, HTTPException, Request, Response
from pydantic import BaseModel
from redis.exceptions import RedisError
from sqlalchemy import select
from sqlalchemy.orm import Session

from psg_control import audit, licence
from psg_control.api.deps import DB, Admin, CipherDep, StoresDep
from psg_control.api.gateways import get_gateway, runtime_for
from psg_control.auth import utcnow
from psg_control.models import Gateway, Tester, TestRun
from psg_control.resolve import defaults, effective
from psg_control.schemas import TestIn
from psg_control.stores import Stores

log = logging.getLogger(__name__)

ONLINE = timedelta(seconds=10)  # a tester heard from within this is online
STALE = timedelta(seconds=60)  # a running test with no progress this long has failed
STALE_ERROR = "The tester stopped reporting (crashed or lost the connection)."
ERRORS_KEPT = 24 * 3600
MAX_ERRORS = 10_000
DLR = {"none": 0, "final": 1, "intermediate": 0x11}


def prog_key(run_id: int) -> str:
    return f"psg:testprog:{run_id}"


def line_key(run_id: int) -> str:
    return f"psg:testline:{run_id}"


def err_key(run_id: int) -> str:
    return f"psg:testerr:{run_id}"


def online(t: Tester) -> bool:
    return t.last_seen is not None and utcnow() - t.last_seen < ONLINE


def tester_out(t: Tester) -> dict:
    return {
        "id": t.id,
        "name": t.name,
        "ip": t.ip,
        "version": t.version,
        "online": online(t),
        "mem_available_mb": t.mem_available_mb,
        "running_run": t.running_run or None,
        "last_seen": t.last_seen,
    }


TIMELINE_TAIL = 120  # points read first for an incremental timeline


def live(stores: Stores, run: TestRun, since: int | None = None) -> tuple[dict | None, list]:
    """The last progress report and the live timeline (running tests only).
    since: only the points after this one (its t), for a panel that has the rest."""
    if run.status != "running":
        return None, []
    try:
        raw = stores.redis.get(prog_key(run.id))
        if since is None:
            line = stores.redis.lrange(line_key(run.id), 0, -1)
        else:  # usually the last few points do; read all only after a long gap
            line = stores.redis.lrange(line_key(run.id), -TIMELINE_TAIL, -1)
            if line and json.loads(line[0])["t"] > since:
                line = stores.redis.lrange(line_key(run.id), 0, -1)
    except RedisError:
        return None, []
    points = [json.loads(x) for x in line]
    if since is not None:
        points = [p for p in points if p["t"] > since]
    return (json.loads(raw) if raw else None), points


def run_out(stores: Stores, run: TestRun, full: bool = True, since: int | None = None) -> dict:
    out = {
        "id": run.id,
        "tester_id": run.tester_id,
        "status": run.status,
        "spec": run.spec,
        "keep_errors": run.keep_errors,
        "stop_requested": run.stop_requested,
        "created_by": run.created_by,
        "created_at": run.created_at,
        "started_at": run.started_at,
        "finished_at": run.finished_at,
        "error": run.error,
    }
    r = run.result or {}
    out["summary"] = (
        {k: r.get(k) for k in ("sent", "ok", "errors", "achieved_tps", "stopped")} if r else None
    )
    if full:
        out["result"] = run.result
        out["progress"], out["timeline"] = live(stores, run, since)
        if since is not None:
            out["timeline_since"] = since  # the timeline holds only the newer points
    return out


def get_run(db: Session, run_id: int) -> TestRun:
    run = db.get(TestRun, run_id)
    if run is None:
        raise HTTPException(status_code=404, detail="Test not found.")
    return run


def expire_stale(db: Session, stores: Stores) -> None:
    """Fails running tests whose tester stopped reporting (crashed, cut off)."""
    for run in db.scalars(select(TestRun).where(TestRun.status == "running")):
        try:
            alive = stores.redis.exists(prog_key(run.id))
        except RedisError:
            return
        if not alive and run.started_at and utcnow() - run.started_at > STALE:
            run.status, run.finished_at = "failed", utcnow()
            run.error = STALE_ERROR
    db.commit()


# ---- admin API ---------------------------------------------------------------------------

router = APIRouter(tags=["tests"])


@router.get("/testers")
def list_testers(db: DB, admin: Admin):
    return [tester_out(t) for t in db.scalars(select(Tester).order_by(Tester.id))]


@router.delete("/testers/{tester_id}", status_code=204)
def remove_tester(tester_id: int, request: Request, db: DB, admin: Admin):
    t = db.get(Tester, tester_id)
    if t is None:
        raise HTTPException(status_code=404, detail="Tester not found.")
    if online(t):
        raise HTTPException(status_code=409, detail="The tester is online; stop it first.")
    audit.record(db, request, admin.username, "tester.remove", "tester", t.id, {"name": t.name})
    db.delete(t)
    db.commit()


@router.get("/tests")
def list_tests(db: DB, admin: Admin, stores: StoresDep, limit: int = 50):
    runs = db.scalars(select(TestRun).order_by(TestRun.id.desc()).limit(min(limit, 500)))
    return [run_out(stores, r, full=False) for r in runs]


@router.post("/tests", status_code=201)
def start_test(body: TestIn, request: Request, db: DB, admin: Admin, stores: StoresDep):
    """Queues a test run; its tester starts it within a second. Returns the
    run with any warnings (e.g. a TPS above the licence cap)."""
    expire_stale(db, stores)
    testers = list(db.scalars(select(Tester).order_by(Tester.id)))
    if body.tester_id is None:
        up = [t for t in testers if online(t)]
        if len(up) != 1:
            raise HTTPException(
                status_code=422,
                detail="Pick a tester" if up else "No tester is online. Start psg-tester first.",
            )
        tester = up[0]
    else:
        tester = next((t for t in testers if t.id == body.tester_id), None)
        if tester is None or not online(tester):
            raise HTTPException(status_code=422, detail=f"Tester {body.tester_id} is not online.")
    busy = db.scalar(
        select(TestRun.id).where(
            TestRun.tester_id == tester.id, TestRun.status.in_(("queued", "running"))
        )
    )
    if busy:
        raise HTTPException(
            status_code=409, detail=f"{tester.name} is already running test {busy}."
        )

    d = defaults(db)
    targets = []
    for gid in dict.fromkeys(body.gateway_ids):
        gw = get_gateway(db, gid)
        rt = runtime_for(stores, gw) or {}
        if gw.status != "active" or rt.get("state") != "listening":
            raise HTTPException(status_code=422, detail=f"Gateway {gw.name} is not listening.")
        eff = effective(gw, d)
        binds, allowed = body.sessions * (2 if body.bind_type == "tx_rx" else 1), eff["max_binds"]
        if allowed and binds > allowed:
            raise HTTPException(
                status_code=422,
                detail=f"Gateway {gw.name} allows {allowed} binds; the test needs {binds}.",
            )
        need = {"trx"} if body.bind_type == "trx" else {"tx", "rx"}
        if not need <= set(eff["bind_types"]):
            raise HTTPException(
                status_code=422,
                detail=f"Gateway {gw.name} does not allow {', '.join(sorted(need))} binds.",
            )
        targets.append({"gateway_id": gw.id, "name": gw.name, "port": gw.port})

    warnings = []
    state = licence.evaluate(db, stores, datetime.now(UTC))
    if body.tps > state.max_tps:
        warnings.append(
            f"{body.tps:g} TPS is above the licence cap of {state.max_tps}: the excess "
            "will be licence-throttled."
        )
    spec = body.model_dump(exclude={"tester_id", "keep_errors"}) | {"targets": targets}
    run = TestRun(
        tester_id=tester.id,
        status="queued",
        spec=spec,
        keep_errors=body.keep_errors,
        stop_requested=False,
        created_by=admin.username,
    )
    db.add(run)
    db.flush()
    audit.record(db, request, admin.username, "test.start", "test", run.id, spec)
    db.commit()
    return run_out(stores, run) | {"warnings": warnings}


@router.get("/tests/{run_id}")
def get_test(run_id: int, db: DB, admin: Admin, stores: StoresDep, since: int | None = None):
    """since: the t of the last timeline point the caller has; only newer ones come back."""
    expire_stale(db, stores)
    return run_out(stores, get_run(db, run_id), since=since)


@router.post("/tests/{run_id}/stop")
def stop_test(run_id: int, request: Request, db: DB, admin: Admin, stores: StoresDep):
    """Asks the tester to stop; it unbinds and sends the result within a few seconds."""
    run = get_run(db, run_id)
    if run.status == "queued":
        run.status, run.finished_at = "stopped", utcnow()
    elif run.status == "running":
        run.stop_requested = True
    else:
        raise HTTPException(status_code=409, detail="The test has already finished.")
    audit.record(db, request, admin.username, "test.stop", "test", run.id)
    db.commit()
    return run_out(stores, run)


@router.delete("/tests/{run_id}", status_code=204)
def delete_test(run_id: int, request: Request, db: DB, admin: Admin, stores: StoresDep):
    run = get_run(db, run_id)
    if run.status in ("queued", "running"):
        raise HTTPException(status_code=409, detail="Stop the test first.")
    try:
        stores.redis.unlink(err_key(run.id), prog_key(run.id), line_key(run.id))
    except RedisError:
        pass  # they expire anyway
    audit.record(db, request, admin.username, "test.delete", "test", run.id)
    db.delete(run)
    db.commit()


@router.get("/tests/{run_id}/errors")
def test_errors(run_id: int, db: DB, admin: Admin, stores: StoresDep, limit: int = 1000):
    """The run's error log (at most 10,000 entries), while it is kept."""
    get_run(db, run_id)
    try:
        raw = stores.redis.lrange(err_key(run_id), 0, min(limit, MAX_ERRORS) - 1)
        ttl = stores.redis.ttl(err_key(run_id))
    except RedisError as exc:
        raise HTTPException(status_code=503, detail="Redis is unavailable.") from exc
    return {"errors": [json.loads(x) for x in raw], "expires_in_s": ttl if ttl > 0 else None}


# ---- tester API (psg-tester agents) ----------------------------------------------------


def require_tester_token(request: Request) -> None:
    expected = request.app.state.settings.tester_token
    if expected is None or not expected.get_secret_value():
        raise HTTPException(status_code=503, detail="PSG_TESTER_TOKEN is not configured.")
    given = request.headers.get("authorization", "").removeprefix("Bearer ")
    if not hmac.compare_digest(given.encode(), expected.get_secret_value().encode()):
        raise HTTPException(status_code=401, detail="Invalid tester token.")


agent_router = APIRouter(
    prefix="/internal/v1",
    tags=["tester"],
    dependencies=[Depends(require_tester_token)],
    include_in_schema=False,
)


class Heartbeat(BaseModel):
    name: str = ""
    ip: str = ""
    version: str = ""
    mem_available_mb: int | None = None
    running: int = 0


@agent_router.post("/testers/{tester_id}/heartbeat", status_code=204)
def heartbeat(tester_id: int, body: Heartbeat, db: DB):
    if not 1 <= tester_id <= 255:
        raise HTTPException(status_code=422, detail="Tester IDs are 1-255.")
    t = db.get(Tester, tester_id)
    if t is None:
        t = Tester(id=tester_id, name=body.name or f"tester-{tester_id}")
        db.add(t)
        log.info("tester %s registered (%s)", tester_id, body.ip)
    t.name = (body.name or t.name)[:64]
    t.ip, t.version = body.ip[:64], body.version[:32]
    t.mem_available_mb, t.running_run, t.last_seen = body.mem_available_mb, body.running, utcnow()
    db.commit()


@agent_router.get("/testers/{tester_id}/job")
def next_job(tester_id: int, db: DB, cipher: CipherDep):
    """The tester's next queued run, now marked running, with what it needs to
    bind (worker IP, port, system_id and password). 204 when there is none."""
    t = db.get(Tester, tester_id)
    if t is not None:
        t.last_seen = utcnow()
    run = db.scalar(
        select(TestRun)
        .where(TestRun.tester_id == tester_id, TestRun.status == "queued")
        .order_by(TestRun.id)
        .limit(1)
        .with_for_update()
    )
    if run is None:
        db.commit()
        return Response(status_code=204)
    spec = run.spec
    targets = []
    for t_ in spec["targets"]:
        gw = db.get(Gateway, t_["gateway_id"])
        if gw is None:
            run.status, run.error, run.finished_at = "failed", "A gateway was deleted.", utcnow()
            db.commit()
            return Response(status_code=204)
        targets.append(
            {
                "gateway_id": gw.id,
                "name": gw.name,
                "addr": f"{gw.worker.ip}:{gw.port}",
                "system_id": gw.system_id,
                "password": cipher.decrypt(gw.password_enc),
                "sessions": spec["sessions"],
                "bind_type": spec["bind_type"],
            }
        )
    run.status, run.started_at = "running", utcnow()
    db.commit()
    return {
        "run_id": run.id,
        "tps": spec["tps"],
        "duration_s": spec["duration_s"],
        "ramp_up_s": spec["ramp_up_s"],
        "window": spec["window"],
        "registered_delivery": DLR[spec["dlr"]],
        "text": spec["text"],
        "encoding": spec["encoding"],
        "net_delay": spec["net_delay"],
        "resp_delay": spec["resp_delay"],
        "trace": spec["trace"],
        "targets": targets,
    }


@agent_router.post("/tests/{run_id}/progress")
def report_progress(run_id: int, body: dict, db: DB, stores: StoresDep):
    """Stores the latest progress for the panel; the reply says whether to stop."""
    run = get_run(db, run_id)
    data = json.dumps(body)
    try:
        pipe = stores.redis.pipeline()
        pipe.set(prog_key(run_id), data, ex=60)
        if (last := (body.get("progress") or {}).get("last")) and last.get("t"):
            pipe.rpush(line_key(run_id), json.dumps(last))
            pipe.ltrim(line_key(run_id), -7200, -1)
            pipe.expire(line_key(run_id), 3600)
        pipe.execute()
    except RedisError:
        pass  # progress is best effort
    return {"stop": run.stop_requested or run.status != "running"}


@agent_router.post("/tests/{run_id}/errors", status_code=204)
def report_errors(run_id: int, body: list[dict], db: DB, stores: StoresDep):
    get_run(db, run_id)
    if not body:
        return
    try:
        pipe = stores.redis.pipeline()
        pipe.rpush(err_key(run_id), *[json.dumps(e) for e in body[:MAX_ERRORS]])
        pipe.ltrim(err_key(run_id), 0, MAX_ERRORS - 1)
        pipe.expire(err_key(run_id), ERRORS_KEPT)
        pipe.execute()
    except RedisError:
        pass


@agent_router.post("/tests/{run_id}/result", status_code=204)
def report_result(run_id: int, body: dict, request: Request, db: DB, stores: StoresDep):
    """Final result (or error). Errors are kept 24 h or dropped now, as asked."""
    run = get_run(db, run_id)
    late = run.status == "failed" and run.error == STALE_ERROR  # the tester was only slow
    if run.status in ("done", "stopped", "failed") and run.finished_at and not late:
        return  # a retried delivery
    result = body.get("result")
    run.finished_at = utcnow()
    if body.get("error") or result is None:
        run.status, run.error = "failed", body.get("error") or "No result."
    else:
        run.result, run.error = result, None
        run.status = "stopped" if result.get("stopped") or run.stop_requested else "done"
    try:
        stores.redis.unlink(prog_key(run_id), line_key(run_id))
        if run.keep_errors:
            stores.redis.expire(err_key(run_id), ERRORS_KEPT)
        else:
            stores.redis.unlink(err_key(run_id))
    except RedisError:
        pass
    db.commit()
