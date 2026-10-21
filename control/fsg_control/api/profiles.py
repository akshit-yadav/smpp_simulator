import random
from collections import Counter

from fastapi import APIRouter, HTTPException, Request
from sqlalchemy import select
from sqlalchemy.orm import Session

from fsg_control import audit
from fsg_control.api.deps import DB, Admin, StoresDep
from fsg_control.models import (
    CommandStatus,
    Defaults,
    DLRError,
    Gateway,
    OutcomeEntry,
    OutcomeProfile,
)
from fsg_control.schemas import PreviewIn, ProfileIn, ProfileOut
from fsg_control.sync import Changes

router = APIRouter(prefix="/profiles", tags=["outcome profiles"])


def check_references(db: Session, p: ProfileIn) -> None:
    """Every command_status and dlr_error must exist in the catalogs."""
    statuses = set(db.scalars(select(CommandStatus.code)))
    errors = set(db.scalars(select(DLRError.code)))
    for i, e in enumerate(p.entries):
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


def apply(db: Session, row: OutcomeProfile, p: ProfileIn) -> None:
    check_references(db, p)
    row.name, row.description = p.name, p.description
    row.mode, row.distribution, row.seed = p.mode, p.distribution, p.seed
    row.entries = [OutcomeEntry(position=i, **e.model_dump()) for i, e in enumerate(p.entries)]


def as_input(row: OutcomeProfile) -> ProfileIn:
    return ProfileIn.model_validate(
        ProfileOut.model_validate(row).model_dump(include=set(ProfileIn.model_fields))
    )


def name_taken(db: Session, name: str, exclude_id: int | None = None) -> bool:
    q = select(OutcomeProfile.id).where(OutcomeProfile.name == name)
    if exclude_id is not None:
        q = q.where(OutcomeProfile.id != exclude_id)
    return db.scalar(q) is not None


def get_profile_row(db: Session, profile_id: int) -> OutcomeProfile:
    row = db.get(OutcomeProfile, profile_id)
    if row is None:
        raise HTTPException(status_code=404, detail="Profile not found.")
    return row


@router.get("", response_model=list[ProfileOut])
def list_profiles(db: DB, admin: Admin, include_custom: bool = False):
    q = select(OutcomeProfile).order_by(OutcomeProfile.name)
    if not include_custom:
        q = q.where(OutcomeProfile.owner_gateway_id.is_(None))
    return list(db.scalars(q))


@router.post("", response_model=ProfileOut, status_code=201)
def create_profile(body: ProfileIn, request: Request, db: DB, admin: Admin):
    if name_taken(db, body.name):
        raise HTTPException(
            status_code=409, detail=f"A profile named {body.name!r} already exists."
        )
    row = OutcomeProfile()
    apply(db, row, body)
    db.add(row)
    db.flush()
    audit.record(
        db, request, admin.username, "profile.create", "profile", row.id, body.model_dump()
    )
    db.commit()
    return row


@router.get("/{profile_id}", response_model=ProfileOut)
def get_profile(profile_id: int, db: DB, admin: Admin):
    return get_profile_row(db, profile_id)


@router.put("/{profile_id}", response_model=ProfileOut)
def update_profile(
    profile_id: int, body: ProfileIn, request: Request, db: DB, admin: Admin, stores: StoresDep
):
    row = get_profile_row(db, profile_id)
    if row.owner_gateway_id is not None:
        raise HTTPException(
            status_code=409, detail="Edit a gateway's custom profile via the gateway."
        )
    if name_taken(db, body.name, row.id):
        raise HTTPException(
            status_code=409, detail=f"A profile named {body.name!r} already exists."
        )
    before = as_input(row).model_dump()
    apply(db, row, body)
    changes = Changes()
    changes.bump(db, None, None, "profile")
    audit.record(
        db,
        request,
        admin.username,
        "profile.update",
        "profile",
        row.id,
        audit.diff(before, body.model_dump()),
    )
    db.commit()
    changes.publish(stores)
    db.refresh(row)
    return row


@router.delete("/{profile_id}", status_code=204)
def delete_profile(profile_id: int, request: Request, db: DB, admin: Admin):
    row = get_profile_row(db, profile_id)
    if row.owner_gateway_id is not None:
        raise HTTPException(status_code=409, detail="A custom profile is removed with its gateway.")
    users = list(db.scalars(select(Gateway.name).where(Gateway.outcome_profile_id == row.id)))
    if db.get(Defaults, 1).outcome_profile_id == row.id:
        users.insert(0, "global defaults")
    if users:
        raise HTTPException(
            status_code=409, detail=f"Profile is in use by: {', '.join(users[:10])}."
        )
    audit.record(
        db, request, admin.username, "profile.delete", "profile", row.id, {"name": row.name}
    )
    db.delete(row)
    db.commit()


@router.post("/preview")
def preview(body: PreviewIn, db: DB, admin: Admin):
    """Simulates the profile for N messages (default 10,000) and reports the
    actual mix against the configured one. Uses the profile's seed if set."""
    if body.profile is not None:
        p = body.profile
        check_references(db, p)
    elif body.profile_id is not None:
        p = as_input(get_profile_row(db, body.profile_id))
    else:
        raise HTTPException(status_code=422, detail="Give either profile or profile_id.")

    n = len(p.entries)
    rng = random.Random(p.seed)  # noqa: S311 - simulation, not security
    if p.mode == "weighted":
        weights = [e.weight_bp for e in p.entries]
    else:
        weights = [10_000 // n + (1 if i < 10_000 % n else 0) for i in range(n)]

    if p.mode == "fixed":
        picks = Counter({0: body.messages})
    elif p.distribution == "exact":
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
    for i, e in enumerate(p.entries):
        if p.mode == "fixed":
            configured = 100.0 if i == 0 else 0.0
        else:
            configured = weights[i] / 100
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
