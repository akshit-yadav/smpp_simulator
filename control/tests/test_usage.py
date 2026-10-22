"""Footer usage figures from fake /proc and cgroup files."""

from pathlib import Path

from psg_control.usage import Usage, read_file
from tests.conftest import ADMIN_PASSWORD, needs_stores

MEMINFO = """MemTotal:       16000000 kB
MemFree:         1000000 kB
MemAvailable:    4000000 kB
SwapTotal:       2000000 kB
SwapFree:        1500000 kB
"""


class Clock:
    def __init__(self):
        self.t = 100.0

    def __call__(self):
        return self.t


def write_proc(root: Path, user: int, idle: int):
    (root / "proc").mkdir(exist_ok=True)
    # cpu  user nice system idle iowait irq softirq steal guest guest_nice
    (root / "proc/stat").write_text(
        f"cpu  {user} 0 0 {idle} 0 0 0 0 0 0\ncpu0 1 0 0 1 0 0 0 0 0 0\ncpu1 1 0 0 1 0 0 0 0 0 0\nintr 1\n"
    )
    (root / "proc/meminfo").write_text(MEMINFO)
    (root / "proc/loadavg").write_text("0.75 0.50 0.25 1/100 999\n")


def write_slice(root: Path, usage_usec: int, current: int, inactive: int):
    s = root / "cgroup/psg.slice"
    s.mkdir(parents=True, exist_ok=True)
    (s / "cpu.stat").write_text(f"usage_usec {usage_usec}\nuser_usec 1\nsystem_usec 1\n")
    (s / "memory.current").write_text(f"{current}\n")
    (s / "memory.stat").write_text(f"anon 1\nfile 2\ninactive_file {inactive}\nactive_file 3\n")


def test_server_and_psg_figures(tmp_path):
    clock = Clock()
    reads = []

    def read(p):
        reads.append(p.name)
        return read_file(p)

    u = Usage(str(tmp_path / "proc"), str(tmp_path / "cgroup"), clock=clock, read=read)
    write_proc(tmp_path, user=1000, idle=1000)
    write_slice(tmp_path, usage_usec=0, current=600 * 1024 * 1024, inactive=100 * 1024 * 1024)
    first = u.get()
    s, p = first["server"], first["psg"]
    assert s["cpu_pct"] is None and p["cpu_pct"] is None  # one sample is not a rate
    assert (s["cpus"], s["load1"], s["mem_total_mb"], s["mem_used_mb"], s["mem_pct"]) == (
        2,
        0.75,
        15625,
        11719,
        75.0,
    )
    assert (s["swap_total_mb"], s["swap_used_mb"]) == (1953, 488)
    assert p == {"available": True, "reason": None, "cpu_pct": None, "mem_mb": 500}

    # Within the cache window nothing is read again.
    n = len(reads)
    clock.t += 1
    assert u.get() is first and len(reads) == n

    # 4 s later: the host was 25% busy; PSG used 2 s of CPU on 2 cores = 25%.
    clock.t += 3
    write_proc(tmp_path, user=1100, idle=1300)
    write_slice(tmp_path, usage_usec=2_000_000, current=700 * 1024 * 1024, inactive=0)
    second = u.get()
    assert second["server"]["cpu_pct"] == 25.0
    assert second["psg"]["cpu_pct"] == 25.0 and second["psg"]["mem_mb"] == 700


def test_psg_unavailable_without_the_slice(tmp_path):
    write_proc(tmp_path, 1, 1)
    u = Usage(str(tmp_path / "proc"), str(tmp_path / "nothing"))
    assert "not mounted" in u.get()["psg"]["reason"]
    (tmp_path / "cgroup").mkdir()
    u = Usage(str(tmp_path / "proc"), str(tmp_path / "cgroup"))
    psg = u.get()["psg"]
    assert psg["available"] is False and "psg.slice" in psg["reason"] and psg["mem_mb"] is None


@needs_stores
def test_usage_endpoint(client, tmp_path):
    assert client.get("/api/v1/system/usage").status_code == 401
    write_proc(tmp_path, 1, 1)
    client.app.state.usage = Usage(str(tmp_path / "proc"), str(tmp_path / "cgroup"))
    client.post("/api/v1/auth/login", json={"username": "admin", "password": ADMIN_PASSWORD})
    body = client.get("/api/v1/system/usage").json()
    assert body["server"]["mem_pct"] == 75.0 and body["psg"]["available"] is False
