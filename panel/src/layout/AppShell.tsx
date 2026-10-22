import { useEffect, useRef, useState } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useLicence, useLogout, useMe, useSystemUsage } from "../api/queries";
import { InfoTip } from "../components/ui";
import { LiveProvider } from "../lib/live";
import { LicenceBanner } from "../pages/Licence";

interface Item {
  to: string;
  label: string;
  soon?: string; // milestone that delivers it
}

const NAV: { group: string; items: Item[] }[] = [
  { group: "Overview", items: [{ to: "/", label: "Dashboard" }] },
  {
    group: "Gateways",
    items: [
      { to: "/gateways", label: "Gateway Manager" },
      { to: "/dlr-rules", label: "DLR Rules" },
      { to: "/catalog", label: "Error catalog" },
      { to: "/gateway-defaults", label: "Gateway Defaults" },
    ],
  },
  {
    group: "Traffic",
    items: [
      { to: "/reports", label: "Reports" },
      { to: "/search", label: "Message search" },
      { to: "/tester", label: "Gateway tester" },
    ],
  },
  {
    group: "System",
    items: [
      { to: "/workers", label: "Workers" },
      { to: "/licence", label: "Licence" },
      { to: "/settings", label: "Settings & audit" },
    ],
  },
];

function Brand() {
  return (
    <div className="brand">
      <div className="brand-mark" aria-hidden>
        PSG
      </div>
      <div>
        <div className="brand-name">Power SMPP Gateway Simulator</div>
        <div className="brand-sub">Admin panel</div>
      </div>
    </div>
  );
}

const gb = (mb: number) => (mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${mb} MB`);

/** "high" from 80%, "very high" from 90%: a word, not just a colour. */
function Pct({ v }: { v: number | null | undefined }) {
  if (v == null) return <span className="muted">…</span>;
  const level = v >= 90 ? "critical" : v >= 80 ? "warning" : "";
  return (
    <span className={level ? `sb-${level}` : ""}>
      {Math.round(v)}%{level && <b>{level === "critical" ? " very high" : " high"}</b>}
    </span>
  );
}

/** Sticky bar with this server's and PSG's CPU and RAM. Signed-in pages only. */
function StatusBar() {
  const q = useSystemUsage();
  const u = q.data;
  const swapSeen = useRef<number | null>(null);
  const [swapping, setSwapping] = useState(false);
  useEffect(() => {
    if (!u) return;
    const prev = swapSeen.current;
    setSwapping(prev !== null && u.server.swap_used_mb - prev > 50); // swap grew by 50+ MB in 5 s
    swapSeen.current = u.server.swap_used_mb;
  }, [u]);
  const s = u?.server;
  const p = u?.psg;
  return (
    <div className="statusbar">
      <span className="sb-group">
        <b>Server</b> <InfoTip term="usage" label="Server and PSG usage" />
        {s ? (
          <>
            <span>
              CPU <Pct v={s.cpu_pct} />
            </span>
            <span>
              RAM {gb(s.mem_used_mb)} / {gb(s.mem_total_mb)} (<Pct v={s.mem_pct} />)
            </span>
            {s.swap_total_mb > 0 && (
              <span className={swapping ? "sb-warning" : ""}>
                swap {gb(s.swap_used_mb)}
                {swapping && <b> growing</b>}
              </span>
            )}
          </>
        ) : (
          <span className="muted">{q.error ? "unavailable" : "…"}</span>
        )}
      </span>
      <span className="sb-group">
        <b>PSG</b>
        {p?.available ? (
          <>
            <span>
              CPU {p.cpu_pct == null ? <span className="muted">…</span> : `${p.cpu_pct.toFixed(1)}%`}
            </span>
            <span>RAM {gb(p.mem_mb ?? 0)}</span>
          </>
        ) : (
          <span className="muted" title={p?.reason ?? undefined}>
            {p ? "not measured" : "…"}
          </span>
        )}
      </span>
      <span className="sb-copy">© {new Date().getFullYear()} Hex Wireless Pvt Ltd</span>
    </div>
  );
}

export function Footer() {
  return (
    <footer className="footer">
      <span>© {new Date().getFullYear()} Hex Wireless Pvt Ltd. All rights reserved.</span>
      <span>Power SMPP Gateway Simulator</span>
    </footer>
  );
}

export { Brand };

export default function AppShell() {
  const me = useMe();
  const logout = useLogout();
  const navigate = useNavigate();
  const location = useLocation();
  const [open, setOpen] = useState(false);
  const licence = useLicence();

  return (
    <LiveProvider>
      <div className="shell">
        <aside className={`sidebar ${open ? "open" : ""}`} onClick={() => setOpen(false)}>
          <Brand />
          <nav aria-label="Main">
            {NAV.map((g) => (
              <div key={g.group}>
                <div className="nav-group">{g.group}</div>
                {g.items.map((it) => (
                  <NavLink
                    key={it.to}
                    to={it.to}
                    end={it.to === "/"}
                    className={({ isActive }) => `nav-link ${isActive ? "active" : ""} ${it.soon ? "soon" : ""}`}
                  >
                    {it.label}
                    {it.soon && <span className="nav-tag">{it.soon}</span>}
                  </NavLink>
                ))}
              </div>
            ))}
          </nav>
          <div className="sidebar-foot">
            <span className="small muted">Signed in as {me.data?.username}</span>
            <button
              type="button"
              className="btn sm"
              onClick={() => logout.mutate(undefined, { onSettled: () => navigate("/login", { replace: true }) })}
            >
              Log out
            </button>
          </div>
        </aside>
        <div className="shell-main">
          <header className="topbar">
            <button type="button" className="btn sm" aria-label="Open menu" onClick={() => setOpen(true)}>
              ☰
            </button>
            <strong>Power SMPP Gateway Simulator</strong>
          </header>
          <main className="main" key={location.pathname}>
            {location.pathname !== "/licence" && <LicenceBanner s={licence.data} />}
            <Outlet />
          </main>
          <StatusBar />
        </div>
      </div>
    </LiveProvider>
  );
}
