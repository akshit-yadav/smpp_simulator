import { useState } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import { useLogout, useMe } from "../api/queries";
import { LiveProvider } from "../lib/live";

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
      { to: "/reports", label: "Reports", soon: "M4" },
      { to: "/search", label: "Message search", soon: "M4" },
    ],
  },
  {
    group: "System",
    items: [
      { to: "/workers", label: "Workers" },
      { to: "/licence", label: "Licence", soon: "M8" },
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
        <div style={{ minWidth: 0 }}>
          <header className="topbar">
            <button type="button" className="btn sm" aria-label="Open menu" onClick={() => setOpen(true)}>
              ☰
            </button>
            <strong>Power SMPP Gateway Simulator</strong>
          </header>
          <main className="main" key={location.pathname}>
            <Outlet />
            <Footer />
          </main>
        </div>
      </div>
    </LiveProvider>
  );
}
