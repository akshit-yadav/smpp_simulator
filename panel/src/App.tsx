import { useEffect } from "react";
import { Navigate, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { setUnauthorizedHandler } from "./api/client";
import { keys, useMe } from "./api/queries";
import { Loading } from "./components/ui";
import AppShell from "./layout/AppShell";
import Catalog from "./pages/Catalog";
import Dashboard from "./pages/Dashboard";
import DlrRuleEdit from "./pages/DlrRuleEdit";
import DlrRules from "./pages/DlrRules";
import GatewayDefaults from "./pages/GatewayDefaults";
import GatewayDetail from "./pages/GatewayDetail";
import GatewayEditor from "./pages/GatewayEditor";
import Gateways from "./pages/Gateways";
import Licence from "./pages/Licence";
import Login from "./pages/Login";
import MessageSearch from "./pages/MessageSearch";
import Reports from "./pages/Reports";
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
        <Route path="dlr-rules" element={<DlrRules />} />
        <Route path="dlr-rules/new" element={<DlrRuleEdit />} />
        <Route path="dlr-rules/:id" element={<DlrRuleEdit />} />
        <Route path="catalog" element={<Catalog />} />
        <Route path="gateway-defaults" element={<GatewayDefaults />} />
        <Route path="workers" element={<Workers />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="reports" element={<Reports />} />
        <Route path="search" element={<MessageSearch />} />
        <Route path="licence" element={<Licence />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
