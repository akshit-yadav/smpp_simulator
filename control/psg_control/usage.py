"""Server and PSG resource usage for the panel footer.

Server figures come from /proc (host-wide even inside a container). PSG
figures come from the psg.slice cgroup that every PSG container on this host
belongs to (docker-compose.yml: cgroup_parent), mounted read-only into the
control container: the kernel keeps the totals, so nothing samples in the
background. Figures are computed only when asked, from the previous sample,
and cached for CACHE_S, so any number of open panels costs a few small file
reads every two seconds.
"""

import threading
import time
from collections.abc import Callable
from pathlib import Path

CACHE_S = 2.0
MB = 1024 * 1024


def read_file(path: Path) -> str:
    return path.read_text()


class Usage:
    def __init__(
        self,
        proc_root: str = "/proc",
        cgroup_root: str = "/host/cgroup",
        slice_name: str = "psg.slice",
        clock: Callable[[], float] = time.monotonic,
        read: Callable[[Path], str] = read_file,
    ) -> None:
        self.proc = Path(proc_root)
        self.cgroup = Path(cgroup_root)
        self.slice = self.cgroup / slice_name
        self.clock, self.read = clock, read
        self._lock = threading.Lock()
        self._prev_cpu: tuple[int, int] | None = None  # /proc/stat (total, idle) jiffies
        self._prev_slice: tuple[float, int] | None = None  # (clock, usage_usec)
        self._cached: dict | None = None
        self._cached_at = 0.0

    def get(self) -> dict:
        with self._lock:
            now = self.clock()
            if self._cached is None or now - self._cached_at >= CACHE_S:
                self._cached, self._cached_at = self._sample(now), now
            return self._cached

    def _sample(self, now: float) -> dict:
        server = self._server()
        return {
            "ts": int(time.time() * 1000),
            "server": server,
            "psg": self._psg(now, server["cpus"]),
        }

    def _server(self) -> dict:
        stat = self.read(self.proc / "stat").splitlines()
        fields = [int(x) for x in stat[0].split()[1:9]]  # user..steal (guest is in user)
        total, idle = sum(fields), fields[3] + fields[4]  # idle + iowait
        cpus = sum(1 for line in stat if line.startswith("cpu") and line[3:4].isdigit()) or 1
        cpu_pct = None
        if self._prev_cpu and total > self._prev_cpu[0]:
            d_total, d_idle = total - self._prev_cpu[0], idle - self._prev_cpu[1]
            cpu_pct = round(100 * (1 - d_idle / d_total), 1)
        self._prev_cpu = (total, idle)

        mem = {}
        for line in self.read(self.proc / "meminfo").splitlines():
            key, _, rest = line.partition(":")
            if key in ("MemTotal", "MemAvailable", "SwapTotal", "SwapFree"):
                mem[key] = int(rest.split()[0]) * 1024
        load1 = float(self.read(self.proc / "loadavg").split()[0])
        used = mem["MemTotal"] - mem["MemAvailable"]
        return {
            "cpus": cpus,
            "cpu_pct": cpu_pct,
            "load1": load1,
            "mem_total_mb": round(mem["MemTotal"] / MB),
            "mem_used_mb": round(used / MB),
            "mem_pct": round(100 * used / mem["MemTotal"], 1),
            "swap_total_mb": round(mem.get("SwapTotal", 0) / MB),
            "swap_used_mb": round((mem.get("SwapTotal", 0) - mem.get("SwapFree", 0)) / MB),
        }

    def _psg(self, now: float, cpus: int) -> dict:
        if not self.cgroup.is_dir():
            return unavailable("Host cgroups are not mounted into the control container.")
        if not self.slice.is_dir():
            return unavailable("PSG's containers are not in a psg.slice cgroup (see README).")
        try:
            usec = int(_field(self.read(self.slice / "cpu.stat"), "usage_usec"))
            current = int(self.read(self.slice / "memory.current").strip())
            inactive = int(_field(self.read(self.slice / "memory.stat"), "inactive_file") or 0)
        except (OSError, ValueError, TypeError) as exc:
            return unavailable(f"PSG's cgroup could not be read ({type(exc).__name__}).")
        cpu_pct = None
        if self._prev_slice and now > self._prev_slice[0]:
            busy = (usec - self._prev_slice[1]) / 1e6
            cpu_pct = round(max(0.0, 100 * busy / ((now - self._prev_slice[0]) * cpus)), 1)
        self._prev_slice = (now, usec)
        return {
            "available": True,
            "reason": None,
            "cpu_pct": cpu_pct,
            "mem_mb": round(max(0, current - inactive) / MB),
        }


def unavailable(reason: str) -> dict:
    return {"available": False, "reason": reason, "cpu_pct": None, "mem_mb": None}


def _field(text: str, name: str) -> str | None:
    for line in text.splitlines():
        key, _, value = line.partition(" ")
        if key == name:
            return value.strip()
    return None
