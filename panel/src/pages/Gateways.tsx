import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useGateways, useProfiles } from "../api/queries";
import { RuntimeBadge } from "../components/GatewayBadges";
import { Card, Empty, ErrorBox, Loading, PageHead } from "../components/ui";
import { fmtNum, fmtRate } from "../lib/format";
import { useLiveRates } from "../lib/live";

export default function Gateways() {
  const q = useGateways(5000);
  const profiles = useProfiles();
  const { latest } = useLiveRates();
  const navigate = useNavigate();
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");

  const rows = useMemo(() => {
    const s = search.trim().toLowerCase();
    return (q.data ?? []).filter(
      (g) =>
        (!status || g.status === status) &&
        (!s || g.name.toLowerCase().includes(s) || g.system_id.toLowerCase().includes(s) || String(g.port).includes(s)),
    );
  }, [q.data, search, status]);

  const profileName = (id: number) => profiles.data?.find((p) => p.id === id)?.name;

  return (
    <>
      <PageHead
        title="Gateways"
        sub="Each gateway is a real SMPP 3.4 listener with its own port, credentials and behaviour."
        actions={
          <Link className="btn primary" to="/gateways/new">
            New gateway
          </Link>
        }
      />
      <Card>
        <div className="row" style={{ marginBottom: 12 }}>
          <input className="input" style={{ maxWidth: 300 }} placeholder="Search name, system ID or port" value={search} onChange={(e) => setSearch(e.target.value)} aria-label="Search gateways" />
          <select className="input" style={{ maxWidth: 160 }} value={status} onChange={(e) => setStatus(e.target.value)} aria-label="Filter by status">
            <option value="">All statuses</option>
            <option value="active">Active</option>
            <option value="disabled">Disabled</option>
            <option value="suspended">Suspended</option>
          </select>
          <span className="muted small">
            {rows.length} of {q.data?.length ?? 0}
          </span>
        </div>
        <ErrorBox error={q.error} />
        {q.isLoading ? (
          <Loading />
        ) : rows.length === 0 ? (
          <Empty title={q.data?.length ? "No gateways match" : "No gateways yet"}>
            {!q.data?.length && (
              <Link className="btn primary" to="/gateways/new">
                Create the first gateway
              </Link>
            )}
          </Empty>
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>System ID</th>
                  <th>Address</th>
                  <th>Worker</th>
                  <th>State</th>
                  <th>Outcomes</th>
                  <th className="num">TPS limit</th>
                  <th className="num">Sessions</th>
                  <th className="num">Accepted/s</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((g) => {
                  const r = latest?.gateways[g.id];
                  const tps = g.effective.tps_limit;
                  return (
                    <tr key={g.id} className="clickable" onClick={() => navigate(`/gateways/${g.id}`)}>
                      <td>
                        <Link to={`/gateways/${g.id}`} onClick={(e) => e.stopPropagation()}>
                          {g.name}
                        </Link>
                      </td>
                      <td className="mono">{g.system_id}</td>
                      <td className="mono">
                        {g.ip}:{g.port}
                      </td>
                      <td>{g.worker_name}</td>
                      <td>
                        <RuntimeBadge gw={g} />
                      </td>
                      <td>{g.custom_outcome ? <span className="muted">Custom</span> : (profileName(g.effective.outcome_profile_id) ?? "—")}</td>
                      <td className="num">{tps ? `${fmtNum(tps)} / ${g.effective.tps_scope}` : "Unlimited"}</td>
                      <td className="num">{fmtNum(r?.sessions ?? 0)}</td>
                      <td className="num">{fmtRate(r?.accepted ?? 0)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  );
}
