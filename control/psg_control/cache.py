"""A short-lived cache for live data the panel polls. Workers publish their
stats once a second, so fifty open tabs can share one read instead of each
doing their own."""

import threading
import time
from collections.abc import Callable
from typing import Any


class TTLCache:
    def __init__(self, ttl_s: float) -> None:
        self.ttl = ttl_s
        self._lock = threading.Lock()
        self._values: dict[str, tuple[float, Any]] = {}

    def get(self, key: str, compute: Callable[[], Any]) -> Any:
        """The cached value, or compute() when it is older than the TTL. Callers
        share the value: they must not change it."""
        if self.ttl <= 0:
            return compute()
        now = time.monotonic()
        with self._lock:
            hit = self._values.get(key)
        if hit and hit[0] > now:
            return hit[1]
        value = compute()  # concurrent misses each compute: at most once per TTL each
        with self._lock:
            self._values[key] = (now + self.ttl, value)
        return value
