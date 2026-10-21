from fastapi import APIRouter, HTTPException, Request
from sqlalchemy import select

from fsg_control import audit, resolve
from fsg_control.api.deps import DB, Admin, StoresDep
from fsg_control.models import Defaults, Gateway, OutcomeProfile
from fsg_control.schemas import DefaultsIn, DefaultsOut
from fsg_control.sync import Changes

router = APIRouter(prefix="/defaults", tags=["defaults"])


@router.get("", response_model=DefaultsOut)
def get_defaults(db: DB, admin: Admin):
    """Global defaults. Every gateway setting left null follows these."""
    return db.get(Defaults, 1)


@router.put("", response_model=DefaultsOut)
def put_defaults(body: DefaultsIn, request: Request, db: DB, admin: Admin, stores: StoresDep):
    profile = db.get(OutcomeProfile, body.outcome_profile_id)
    if profile is None or profile.owner_gateway_id is not None:
        raise HTTPException(status_code=422, detail="outcome_profile_id must be a shared profile.")
    row = db.get(Defaults, 1)
    before = DefaultsIn.model_validate(
        DefaultsOut.model_validate(row).model_dump(exclude={"updated_at"})
    )
    for k, v in body.model_dump().items():
        setattr(row, k, v)
    db.flush()
    bad = [
        gw.name
        for gw in db.scalars(select(Gateway))
        if (e := resolve.effective(gw, row))["dlr_min_ms"] > e["dlr_max_ms"]
    ]
    if bad:
        db.rollback()
        raise HTTPException(
            status_code=422,
            detail=f"These gateways would get an invalid DLR delay range: {', '.join(bad[:10])}.",
        )
    changes = Changes()
    changes.bump(db, None, None, "defaults")
    audit.record(
        db,
        request,
        admin.username,
        "defaults.update",
        "defaults",
        1,
        audit.diff(before.model_dump(), body.model_dump()),
    )
    db.commit()
    changes.publish(stores)
    return row
