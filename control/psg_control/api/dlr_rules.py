import random
from collections import Counter

from fastapi import APIRouter, HTTPException, Request
from sqlalchemy import delete, select
from sqlalchemy.orm import Session
from sqlalchemy.orm.attributes import set_committed_value

from psg_control import audit
from psg_control.api.deps import DB, Admin, StoresDep
from psg_control.models import CommandStatus, Defaults, DLRError, DLRRule, DLRRuleEntry, Gateway
from psg_control.schemas import EntryIn, PreviewIn, RuleIn, RuleOut
from psg_control.sync import Changes

router = APIRouter(prefix="/dlr-rules", tags=["DLR rules"])


def check_references(db: Session, r: RuleIn) -> None:
    """Every command_status and DLR error must exist in the catalogs."""
    statuses = set(db.scalars(select(CommandStatus.code)))
    errors = set(db.scalars(select(DLRError.code)))
    for i, e in enumerate(r.entries):
        if e.command_status not in statuses:
            raise HTTPException(
                status_code=422,
                detail=f"entries[{i}]: command_status 0x{e.command_status:X} is not in the catalog",
            )
        if e.dlr_error is not None and e.dlr_error not in errors:
            raise HTTPException(
                status_code=422,
                detail=f"entries[{i}]: DLR error {e.dlr_error!r} is not in the catalog.",
            )


def apply(db: Session, row: DLRRule, r: RuleIn) -> None:
    check_references(db, r)
    row.name, row.description = r.name, r.description
    row.mode, row.distribution, row.seed = r.mode, r.distribution, r.seed
    if row.id is not None:
        # A plain DELETE removes the latest committed entries, a concurrent
        # save's included; the ORM would only delete the ones it loaded.
        db.execute(delete(DLRRuleEntry).where(DLRRuleEntry.rule_id == row.id))
        set_committed_value(row, "entries", [])
    row.entries = [DLRRuleEntry(position=i, **e.model_dump()) for i, e in enumerate(r.entries)]


def as_input(row: DLRRule) -> RuleIn:
    """The stored rule as input, not validated again: a rule saved before a
    newer check (whole-percent shares, say) still previews and diffs."""
    out = RuleOut.model_validate(row)
    entries = [
        EntryIn.model_construct(**e.model_dump(include=set(EntryIn.model_fields)))
        for e in out.entries
    ]
    return RuleIn.model_construct(
        **out.model_dump(include=set(RuleIn.model_fields) - {"entries"}), entries=entries
    )


def name_taken(db: Session, name: str, exclude_id: int | None = None) -> bool:
    q = select(DLRRule.id).where(DLRRule.name == name)
    if exclude_id is not None:
        q = q.where(DLRRule.id != exclude_id)
    return db.scalar(q) is not None


def get_rule(db: Session, rule_id: int, lock: bool = False) -> DLRRule:
    """lock: hold the rule's row until commit, so saves of one rule run one at a time."""
    row = db.get(DLRRule, rule_id, with_for_update=lock, populate_existing=lock)
    if row is None:
        raise HTTPException(status_code=404, detail="DLR rule not found.")
    return row


@router.get("", response_model=list[RuleOut])
def list_rules(db: DB, admin: Admin):
    return list(db.scalars(select(DLRRule).order_by(DLRRule.name)))


@router.post("", response_model=RuleOut, status_code=201)
def create_rule(body: RuleIn, request: Request, db: DB, admin: Admin):
    if name_taken(db, body.name):
        raise HTTPException(
            status_code=409, detail=f"A DLR rule named {body.name!r} already exists."
        )
    row = DLRRule()
    apply(db, row, body)
    db.add(row)
    db.flush()
    audit.record(
        db, request, admin.username, "dlr_rule.create", "dlr_rule", row.id, body.model_dump()
    )
    db.commit()
    return row


@router.get("/{rule_id}", response_model=RuleOut)
def read_rule(rule_id: int, db: DB, admin: Admin):
    return get_rule(db, rule_id)


@router.put("/{rule_id}", response_model=RuleOut)
def update_rule(
    rule_id: int, body: RuleIn, request: Request, db: DB, admin: Admin, stores: StoresDep
):
    row = get_rule(db, rule_id, lock=True)
    if name_taken(db, body.name, row.id):
        raise HTTPException(
            status_code=409, detail=f"A DLR rule named {body.name!r} already exists."
        )
    before = as_input(row).model_dump()
    apply(db, row, body)
    changes = Changes()
    changes.bump(db, None, None, "dlr_rule")
    audit.record(
        db,
        request,
        admin.username,
        "dlr_rule.update",
        "dlr_rule",
        row.id,
        audit.diff(before, body.model_dump()),
    )
    db.commit()
    changes.publish(stores)
    db.refresh(row)
    return row


@router.delete("/{rule_id}", status_code=204)
def delete_rule(rule_id: int, request: Request, db: DB, admin: Admin):
    row = get_rule(db, rule_id)
    users = list(db.scalars(select(Gateway.name).where(Gateway.dlr_rule_id == row.id)))
    if db.get(Defaults, 1).dlr_rule_id == row.id:
        users.insert(0, "Gateway Defaults")
    if users:
        raise HTTPException(
            status_code=409, detail=f"DLR rule is in use by: {', '.join(users[:10])}."
        )
    audit.record(
        db, request, admin.username, "dlr_rule.delete", "dlr_rule", row.id, {"name": row.name}
    )
    db.delete(row)
    db.commit()


@router.post("/preview")
def preview(body: PreviewIn, db: DB, admin: Admin):
    """Simulates the rule for N messages (default 10,000) and reports the
    actual mix against the configured one. Uses the rule's seed if set."""
    if body.rule is not None:
        r = body.rule
        check_references(db, r)
    elif body.rule_id is not None:
        r = as_input(get_rule(db, body.rule_id))
    else:
        raise HTTPException(status_code=422, detail="Give either rule or rule_id.")

    n = len(r.entries)
    rng = random.Random(r.seed)  # noqa: S311 - simulation, not security
    if r.mode == "weighted":
        weights = [e.weight_bp for e in r.entries]
    else:
        weights = [10_000 // n + (1 if i < 10_000 % n else 0) for i in range(n)]

    if r.mode == "fixed":
        picks = Counter({0: body.messages})
    elif r.distribution == "exact":
        deck = [i for i, w in enumerate(weights) for _ in range(w)]
        picks = Counter()
        left = body.messages
        while left > 0:
            rng.shuffle(deck)
            picks.update(deck[: min(left, len(deck))])
            left -= len(deck)
    else:
        picks = Counter(rng.choices(range(n), weights=weights, k=body.messages))

    entries = []
    for i, e in enumerate(r.entries):
        configured = (100.0 if i == 0 else 0.0) if r.mode == "fixed" else weights[i] / 100
        entries.append(
            {
                "name": e.name,
                "command_status": e.command_status,
                "dlr_status": e.dlr_status,
                "dlr_error": e.dlr_error,
                "configured_pct": configured,
                "count": picks.get(i, 0),
                "actual_pct": round(picks.get(i, 0) * 100 / body.messages, 3),
            }
        )
    accepted = sum(x["count"] for x in entries if x["command_status"] == 0)
    by_status = Counter()
    for x in entries:
        if x["command_status"] == 0:
            by_status[x["dlr_status"]] += x["count"]
    return {
        "messages": body.messages,
        "accepted": accepted,
        "rejected": body.messages - accepted,
        "dlr_status": dict(by_status),
        "entries": entries,
    }
