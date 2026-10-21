import { Link, useNavigate } from "react-router-dom";
import { useGateways, useWorkers } from "../api/queries";
import { LineChart, Sparkline } from "../components/charts";
import { RuntimeBadge } from "../components/GatewayBadges";
import { Badge, Card, Empty, ErrorBox, Loading, PageHead } from "../components/ui";
import { fmtNum, fmtRate } from "../lib/format";
import { useLiveRates } from "../lib/live";

function Tile({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="tile">
      <div className="tile-label">{label}</div>
      <div className="tile-value">{value}</div>
      {sub && <div className="tile-sub">{sub}</div>}
    </div>
  );
}

export default function Dashboard() {
  const gateways = useGateways(5000);
  const workers = useWorkers();
  const { history, latest, live, error } = useLiveRates();
  const navigate = useNavigate();

  if (gateways.isLoading || workers.isLoading) return <Loading />;
  const gws = gateways.data ?? [];
  const ws = workers.data ?? [];
  const listening = gws.filter((g) => g.runtime?.state === "listening").length;
  const failing = gws.filter((g) => g.runtime?.state === "error");
  const online = ws.filter((w) => w.online).length;
  const t = latest?.total;
  const times = history.map((s) => s.ts);
  const backlog = (live?.workers ?? []).reduce((a, w) => ({ pending: a.pending + w.dlr_pending, queued: a.queued + w.dlr_queued }), { pending: 0, queued: 0 });

  return (
    <>
      <PageHead title="Dashboard" sub="Live traffic across all gateways. Updates every 2 seconds." />
      <div className="stack">
        <ErrorBox error={gateways.error ?? workers.error ?? error} />
        {failing.length > 0 && (
          <div className="alert critical">
            {failing.length === 1 ? "1 gateway is" : `${failing.length} gateways are`} not listening:{" "}
            {failing.map((g, i) => (
              <span key={g.id}>
                {i > 0 && ", "}
                <Link to={`/gateways/${g.id}`}>{g.name}</Link> ({g.runtime?.error})
              </span>
            ))}
          </div>
        )}
        {ws.length === 0 && (
          <div className="alert warning">No worker has registered yet. Start fsg-worker; it appears here within a few seconds.</div>
        )}

        <div className="grid grid-4">
          <Tile label="Accepted submit_sm" value={`${fmtRate(t?.accepted)}/s`} sub={`${fmtRate(t?.throttled)}/s throttled · ${fmtRate(t?.rejected)}/s rejected`} />
          <Tile label="DLRs sent" value={`${fmtRate(t?.dlr)}/s`} sub={`${fmtNum(backlog.pending)} scheduled · ${fmtNum(backlog.queued)} queued (no receiver)`} />
          <Tile label="Bound sessions" value={fmtNum(t?.sessions ?? 0)} sub="across all gateways" />
          <Tile label="Gateways listening" value={`${listening} / ${gws.length}`} sub={`${online} of ${ws.length} workers online`} />
        </div>

        <div className="grid grid-2">
          <Card title="submit_sm per second">
            <LineChart
              ariaLabel="submit_sm per second: accepted, throttled and rejected"
              times={times}
              series={[
                { name: "Accepted", color: "var(--series-1)", values: history.map((s) => s.total.accepted) },
                { name: "Throttled", color: "var(--series-2)", values: history.map((s) => s.total.throttled) },
                { name: "Rejected", color: "var(--series-3)", values: history.map((s) => s.total.rejected) },
              ]}
            />
          </Card>
          <Card title="DLRs sent per second">
            <LineChart ariaLabel="DLRs sent per second" times={times} series={[{ name: "DLRs", color: "var(--series-1)", values: history.map((s) => s.total.dlr) }]} />
          </Card>
        </div>

        <Card
          title="Gateways"
          actions={
            <Link className="btn sm" to="/gateways/new">
              New gateway
            </Link>
          }
        >
          {gws.length === 0 ? (
            <Empty title="No gateways yet">
              <p>Create a gateway to start accepting SMPP traffic.</p>
              <Link className="btn primary" to="/gateways/new">
                New gateway
              </Link>
            </Empty>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Gateway</th>
                    <th>Address</th>
                    <th>State</th>
                    <th className="num">Sessions</th>
                    <th className="num">Accepted/s</th>
                    <th className="num">Throttled/s</th>
                    <th className="num">DLRs/s</th>
                    <th>Last 5 min</th>
                  </tr>
                </thead>
                <tbody>
                  {gws.map((g) => {
                    const r = latest?.gateways[g.id];
                    return (
                      <tr key={g.id} className="clickable" onClick={() => navigate(`/gateways/${g.id}`)}>
                        <td>
                          <Link to={`/gateways/${g.id}`} onClick={(e) => e.stopPropagation()}>
                            {g.name}
                          </Link>
                        </td>
                        <td className="mono">
                          {g.ip}:{g.port}
                        </td>
                        <td>
                          <RuntimeBadge gw={g} />
                        </td>
                        <td className="num">{fmtNum(r?.sessions ?? 0)}</td>
                        <td className="num">{fmtRate(r?.accepted ?? 0)}</td>
                        <td className="num">{fmtRate(r?.throttled ?? 0)}</td>
                        <td className="num">{fmtRate(r?.dlr ?? 0)}</td>
                        <td>
                          <Sparkline label={`${g.name} accepted per second`} values={history.map((s) => s.gateways[g.id]?.accepted ?? 0)} />
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </Card>

        {ws.length > 0 && (
          <Card title="Workers">
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Worker</th>
                    <th>IP</th>
                    <th>State</th>
                    <th className="num">Gateways</th>
                    <th className="num">DLRs scheduled</th>
                    <th className="num">DLRs queued</th>
                  </tr>
                </thead>
                <tbody>
                  {ws.map((w) => {
                    const lw = live?.workers.find((x) => x.id === w.id);
                    return (
                      <tr key={w.id}>
                        <td>{w.name}</td>
                        <td className="mono">{w.ip}</td>
                        <td>
                          {w.online ? <Badge tone="good">Online</Badge> : <Badge tone="critical">Offline</Badge>}{" "}
                          {w.status === "draining" && <Badge tone="warning">Draining</Badge>}
                        </td>
                        <td className="num">{w.gateways}</td>
                        <td className="num">{fmtNum(lw?.dlr_pending ?? 0)}</td>
                        <td className="num">{fmtNum(lw?.dlr_queued ?? 0)}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </Card>
        )}
      </div>
    </>
  );
}
