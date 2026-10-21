import { useEffect } from "react";
import { Navigate, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { setUnauthorizedHandler } from "./api/client";
import { keys, useMe } from "./api/queries";
import { Loading } from "./components/ui";
import AppShell from "./layout/AppShell";
import Catalog from "./pages/Catalog";
import ComingSoon from "./pages/ComingSoon";
import Dashboard from "./pages/Dashboard";
import DefaultsPage from "./pages/Defaults";
import GatewayDetail from "./pages/GatewayDetail";
import GatewayEditor from "./pages/GatewayEditor";
import Gateways from "./pages/Gateways";
import Login from "./pages/Login";
import ProfileEdit from "./pages/ProfileEdit";
import Profiles from "./pages/Profiles";
import SettingsPage from "./pages/Settings";
import Workers from "./pages/Workers";

function RequireAuth({ children }: { children: React.ReactNode }) {
  const me = useMe();
  const location = useLocation();
  if (me.isLoading) return <Loading />;
  if (!me.data) return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  return <>{children}</>;
}

export default function App() {
  const qc = useQueryClient();
  const navigate = useNavigate();

  useEffect(() => {
    // An expired session anywhere sends the admin back to the login page.
    setUnauthorizedHandler(() => {
      qc.setQueryData(keys.me, null);
      navigate("/login", { replace: true });
    });
  }, [qc, navigate]);

  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route
        element={
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        }
      >
        <Route index element={<Dashboard />} />
        <Route path="gateways" element={<Gateways />} />
        <Route path="gateways/new" element={<GatewayEditor />} />
        <Route path="gateways/:id" element={<GatewayDetail />} />
        <Route path="gateways/:id/edit" element={<GatewayEditor />} />
        <Route path="profiles" element={<Profiles />} />
        <Route path="profiles/new" element={<ProfileEdit />} />
        <Route path="profiles/:id" element={<ProfileEdit />} />
        <Route path="catalog" element={<Catalog />} />
        <Route path="defaults" element={<DefaultsPage />} />
        <Route path="workers" element={<Workers />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route
          path="reports"
          element={<ComingSoon title="Reports" milestone="M4" what="Traffic by gateway, outcome mix (actual vs configured), DLR delay distribution, throttling, MO traffic, bind history and CSV export, from ClickHouse." />}
        />
        <Route
          path="search"
          element={<ComingSoon title="Message search" milestone="M4" what="Find a message by message ID or destination and see its submit, outcome and DLR events." />}
        />
        <Route
          path="licence"
          element={<ComingSoon title="Licence" milestone="M8" what="Upload a licence, see machine fingerprints and limits, export a licence request, and choose which gateways stay active." />}
        />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
