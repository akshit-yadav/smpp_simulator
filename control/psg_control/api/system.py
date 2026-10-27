"""Server and PSG resource usage for the panel footer (see psg_control.usage)."""

from fastapi import APIRouter, Request

from psg_control.api.deps import Admin

router = APIRouter(tags=["system"])


@router.get("/system/usage")
def system_usage(request: Request, admin: Admin):
    """CPU and RAM of this server and of PSG's containers on it. Cached for 2 s;
    CPU is null until a second sample exists."""
    return request.app.state.usage.get()
