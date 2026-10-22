import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  reportUrl,
  useBindReport,
  useDelayReport,
  useGateway,
  useGateways,
  useMixReport,
  useMoReport,
  useTrafficReport,
  type ReportQuery,
} from "../api/queries";
import type { TrafficRow } from "../api/types";
import { BarChart, LineChart } from "../components/charts";
import { Badge, Card, Check, Empty, ErrorBox, Loading, PageHead, Segmented, Tabs, Term } from "../components/ui";
import { fmtMs, fmtNum, fmtTime, hex } from "../lib/format";

type Preset = "1h" | "6h" | "24h" | "7d" | "30d";
type Tab = "traffic" | "mix" | "delay" | "throttling" | "mo" | "binds";

const PRESET_MS: Record<Preset, number> = { "1h": 3600e3, "6h": 6 * 3600e3, "24h": 86400e3, "7d": 7 * 86400e3, "30d": 30 * 86400e3 };
const TAB_PATH: Record<Tab, string> = {
  traffic: "/reports/traffic",
  mix: "/reports/outcomes",
  delay: "/reports/dlr-delay",
  throttling: "/reports/traffic",
  mo: "/reports/mo",
  binds: "/reports/binds",
};

export default function Reports() {
  const gateways = useGateways();
  const [tab, setTab] = useState<Tab>("traffic");
  const [preset, setPreset] = useState<Preset>("24h");
  const [anchor, setAnchor] = useState(() => Date.now()); // fixed until Refresh, so queries stay cached
  const [gatewayId, setGatewayId] = useState<number | undefined>(undefined);
  const [withSimulator, setWithSimulator] = useState(false);

  const query: ReportQuery = useMemo(
    () => ({
      from: new Date(anchor - PRESET_MS[preset]).toISOString(),
      to: new Date(anchor).toISOString(),
      gateway_id: gatewayId,
      include_simulator: withSimulator || undefined,
    }),
    [anchor, preset, gatewayId, withSimulator],
  );
  const csvDisabled = tab === "mix" && gatewayId === undefined;

  return (
    <>
      <PageHead
        title="Reports"
        sub={
          <Term info="reportRetention">Traffic, DLR rule mix, DLR delays, throttling and binds, from the events workers record.</Term>
        }
      />
      <div className="stack">
        <div className="row filters">
          <Segmented<Preset>
            label="Time range"
            value={preset}
            onChange={(p) => {
              setPreset(p);
              setAnchor(Date.now());
            }}
            options={(Object.keys(PRESET_MS) as Preset[]).map((p) => ({ value: p, label: p }))}
          />
          <select
            className="input"
            style={{ maxWidth: 240 }}
            aria-label="Gateway"
            value={gatewayId ?? ""}
            onChange={(e) => setGatewayId(e.target.value ? Number(e.target.value) : undefined)}
          >
            <option value="">All gateways</option>
            {(gateways.data ?? []).map((g) => (
              <option key={g.id} value={g.id}>
                {g.name}
              </option>
            ))}
          </select>
          <Check checked={withSimulator} onChange={setWithSimulator}>
            <Term info="includeSimulator">Include simulator tests</Term>
          </Check>
          <button type="button" className="btn" onClick={() => setAnchor(Date.now())}>
            Refresh
          </button>
          <span className="spacer" />
          {csvDisabled ? (
            <button type="button" className="btn" disabled title="Pick a gateway first">
              Export CSV
            </button>
          ) : (
            <a className="btn" href={reportUrl(TAB_PATH[tab], { ...query, format: "csv" })} download>
              Export CSV
            </a>
          )}
        </div>

        <Tabs<Tab>
          value={tab}
          onChange={setTab}
          tabs={[
            { value: "traffic", label: "Traffic" },
            { value: "mix", label: "DLR rule mix" },
            { value: "delay", label: "DLR delay" },
            { value: "throttling", label: "Throttling" },
            { value: "mo", label: "MO traffic" },
            { value: "binds", label: "Bind history" },
          ]}
        />
        {tab === "traffic" && <TrafficTab query={query} />}
        {tab === "mix" && <MixTab query={query} />}
        {tab === "delay" && <DelayTab query={query} />}
        {tab === "throttling" && <ThrottlingTab query={query} />}
        {tab === "mo" && <MoTab query={query} />}
        {tab === "binds" && <BindsTab query={query} />}
      </div>
    </>
  );
}

// ---- traffic -----------------------------------------------------------------------

type Counts = Omit<TrafficRow, "t" | "gateway_id" | "gateway">;

const ZERO: Counts = {
  accepted: 0,
  rejected: 0,
  throttled: 0,
  invalid: 0,
  licence_throttled: 0,
  dlr_acknowledged: 0,
  dlr_retried: 0,
  dlr_failed: 0,
  dlr_queued: 0,
  dlr_expired: 0,
};

function add(a: Counts, b: Counts): Counts {
  const out = { ...a };
  for (const k of Object.keys(ZERO) as (keyof Counts)[]) out[k] += b[k];
  return out;
}

/** Rows summed across gateways, one per step from `from` to `to`, gaps filled with zeros. */
function timeline(rows: TrafficRow[], from: string, to: string, step: number) {
  const ms = step * 1000;
  const byT = new Map<number, Counts>();
  for (const r of rows) byT.set(r.t, add(byT.get(r.t) ?? ZERO, r));
  const times: number[] = [];
  const counts: Counts[] = [];
  for (let t = Math.floor(Date.parse(from) / ms) * ms; t < Date.parse(to); t += ms) {
    times.push(t);
    counts.push(byT.get(t) ?? ZERO);
  }
  return { times, counts };
}

function stepLabel(step: number): string {
  if (step % 86400 === 0) return step === 86400 ? "day" : `${step / 86400} days`;
  if (step % 3600 === 0) return step === 3600 ? "hour" : `${step / 3600} hours`;
  return step === 60 ? "minute" : `${step / 60} minutes`;
}

function timeFormatter(step: number) {
  if (step >= 86400) return (t: number) => new Date(t).toLocaleDateString([], { day: "numeric", month: "short" });
  if (step >= 3600) return (t: number) => new Date(t).toLocaleString([], { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
  return (t: number) => new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

const sum = (xs: number[]) => xs.reduce((a, b) => a + b, 0);
const total = (s: { values: number[] }) => `${fmtNum(sum(s.values))} total`;

function TrafficTab({ query }: { query: ReportQuery }) {
  const r = useTrafficReport(query);
  const data = r.data;
  const { times, counts } = useMemo(() => (data ? timeline(data.rows, data.from, data.to, data.step) : { times: [], counts: [] }), [data]);
  const perGateway = useMemo(() => {
    const m = new Map<number, { name: string | null; c: Counts }>();
    for (const row of data?.rows ?? []) {
      const g = m.get(row.gateway_id) ?? { name: row.gateway, c: ZERO };
      m.set(row.gateway_id, { name: g.name, c: add(g.c, row) });
    }
    return [...m.entries()].sort((a, b) => b[1].c.accepted - a[1].c.accepted);
  }, [data]);

  if (r.isLoading) return <Loading />;
  if (r.error || !data) return <ErrorBox error={r.error} />;
  const per = stepLabel(data.step);
  const fmt = timeFormatter(data.step);
  const grand = perGateway.reduce((a, [, g]) => add(a, g.c), ZERO);

  return (
    <>
      <div className="grid grid-2">
        <Card title={`submit_sm per ${per}`}>
          <LineChart
            ariaLabel={`submit_sm per ${per}: accepted, throttled and rejected`}
            times={times}
            unit=""
            formatTime={fmt}
            legendValue={total}
            emptyText="No traffic in this range."
            series={[
              { name: "Accepted", color: "var(--series-1)", values: counts.map((c) => c.accepted) },
              { name: "Throttled", color: "var(--series-2)", values: counts.map((c) => c.throttled + c.licence_throttled) },
              { name: "Rejected", color: "var(--series-3)", values: counts.map((c) => c.rejected + c.invalid) },
            ]}
          />
        </Card>
        <Card title={`DLRs per ${per}`}>
          <LineChart
            ariaLabel={`DLRs per ${per}: acknowledged, retried and failed`}
            times={times}
            unit=""
            formatTime={fmt}
            legendValue={total}
            emptyText="No DLRs in this range."
            series={[
              { name: "Acknowledged", color: "var(--series-1)", values: counts.map((c) => c.dlr_acknowledged) },
              { name: "Retried", color: "var(--series-2)", values: counts.map((c) => c.dlr_retried) },
              { name: "Failed", color: "var(--series-3)", values: counts.map((c) => c.dlr_failed) },
            ]}
          />
        </Card>
      </div>
      <Card title="By gateway">
        {perGateway.length === 0 ? (
          <Empty title="No traffic in this range" />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Gateway</th>
                  <th className="num">Accepted</th>
                  <th className="num">Rejected</th>
                  <th className="num">Invalid</th>
                  <th className="num">Throttled</th>
                  <th className="num">
                    <Term info="dlrAcknowledged">DLRs acked</Term>
                  </th>
                  <th className="num">
                    <Term info="dlrRetried">Retried</Term>
                  </th>
                  <th className="num">
                    <Term info="dlrFailed">Failed</Term>
                  </th>
                  <th className="num">
                    <Term info="dlrQueued">Queued</Term>
                  </th>
                  <th className="num">Expired</th>
                </tr>
              </thead>
              <tbody>
                {perGateway.map(([id, g]) => (
                  <CountsRow key={id} label={g.name ? <Link to={`/gateways/${id}`}>{g.name}</Link> : <span className="muted">deleted (#{id})</span>} c={g.c} />
                ))}
                {perGateway.length > 1 && <CountsRow label={<b>Total</b>} c={grand} />}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  );
}

function CountsRow({ label, c }: { label: React.ReactNode; c: Counts }) {
  return (
    <tr>
      <td>{label}</td>
      {[c.accepted, c.rejected, c.invalid, c.throttled + c.licence_throttled, c.dlr_acknowledged, c.dlr_retried, c.dlr_failed, c.dlr_queued, c.dlr_expired].map((v, i) => (
        <td key={i} className="num mono">
          {fmtNum(v)}
        </td>
      ))}
    </tr>
  );
}

// ---- DLR rule mix ------------------------------------------------------------------

function PickGateway({ what }: { what: string }) {
  return (
    <div className="card">
      <Empty title="Pick a gateway">
        <p className="muted">{what}</p>
      </Empty>
    </div>
  );
}

function MixTab({ query }: { query: ReportQuery }) {
  const r = useMixReport(query);
  if (query.gateway_id === undefined) return <PickGateway what="The DLR rule mix compares one gateway's traffic with its DLR rule." />;
  if (r.isLoading) return <Loading />;
  if (r.error || !r.data) return <ErrorBox error={r.error} />;
  const d = r.data;
  const np = d.not_picked;
  const skipped = np.throttled + np.invalid + np.licence_throttled + np.receiver_only;

  return (
    <Card
      title={
        <Term info="ruleMix">
          DLR rule <Link to={`/dlr-rules/${d.rule.id}`}>{d.rule.name}</Link> ({d.rule.mode}): actual vs configured
        </Term>
      }
    >
      {d.picked === 0 ? (
        <Empty title="No messages reached the rule in this range" />
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Entry</th>
                <th>Result</th>
                <th style={{ width: "34%" }}>
                  <span className="legend">
                    <span className="legend-item">
                      <span className="legend-swatch" style={{ background: "var(--series-1)" }} />
                      Actual
                    </span>
                    <span className="legend-item">
                      <span className="mix-marker-key" />
                      Configured
                    </span>
                  </span>
                </th>
                <th className="num">Configured</th>
                <th className="num">Actual</th>
                <th className="num">Difference</th>
                <th className="num">Messages</th>
              </tr>
            </thead>
            <tbody>
              {d.rows.map((row) => {
                const diff = row.configured_pct !== null && row.actual_pct !== null ? row.actual_pct - row.configured_pct : null;
                return (
                  <tr key={`${row.name}:${row.command_status}`}>
                    <td>{row.name || <span className="muted">(unnamed)</span>}</td>
                    <td>
                      {row.command_status === 0 ? (
                        <span className="mono">
                          {row.dlr_status ?? "accepted"}
                          {row.dlr_error ? ` · err ${row.dlr_error}` : ""}
                        </span>
                      ) : (
                        <span className="mono">reject {hex(row.command_status)}</span>
                      )}
                    </td>
                    <td>
                      <div
                        className="mix-bar"
                        role="img"
                        aria-label={`actual ${row.actual_pct ?? 0}%, configured ${row.configured_pct ?? "none"}%`}
                      >
                        <div className="mix-fill" style={{ width: `${row.actual_pct ?? 0}%` }} />
                        {row.configured_pct !== null && <div className="mix-marker" style={{ left: `${row.configured_pct}%` }} />}
                      </div>
                    </td>
                    <td className="num mono">{row.configured_pct === null ? <span className="muted">not in rule</span> : `${row.configured_pct.toFixed(2)}%`}</td>
                    <td className="num mono">{row.actual_pct === null ? "—" : `${row.actual_pct.toFixed(2)}%`}</td>
                    <td className="num mono">{diff === null ? "—" : `${diff >= 0 ? "+" : "−"}${Math.abs(diff).toFixed(2)} pp`}</td>
                    <td className="num mono">{fmtNum(row.actual)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      <p className="muted small" style={{ marginTop: 12 }}>
        {fmtNum(d.picked)} messages reached the rule.
        {skipped > 0 &&
          ` Not counted: ${[
            np.throttled && `${fmtNum(np.throttled)} throttled`,
            np.licence_throttled && `${fmtNum(np.licence_throttled)} licence-throttled`,
            np.invalid && `${fmtNum(np.invalid)} invalid`,
            np.receiver_only && `${fmtNum(np.receiver_only)} sent on receiver (RX) binds`,
          ]
            .filter(Boolean)
            .join(", ")}.`}
      </p>
    </Card>
  );
}

// ---- DLR delay ---------------------------------------------------------------------

function shortMs(ms: number): string {
  if (ms >= 60_000) return `${ms / 60_000}m`;
  if (ms >= 1000) return `${ms / 1000}s`;
  return `${ms}ms`;
}

function DelayTab({ query }: { query: ReportQuery }) {
  const r = useDelayReport(query);
  if (r.isLoading) return <Loading />;
  if (r.error || !r.data) return <ErrorBox error={r.error} />;
  const d = r.data;
  const bars = d.rows.map((b) => ({
    label: b.to_ms === null ? `${shortMs(b.from_ms)}+` : shortMs(b.from_ms),
    title: b.to_ms === null ? `${fmtMs(b.from_ms)} or more` : `${fmtMs(b.from_ms)} to ${fmtMs(b.to_ms)}`,
    value: b.dlrs,
  }));

  return (
    <div className="grid grid-2-1">
      <Card title={<Term info="dlrDelay">Acknowledged DLRs by delay</Term>}>
        {d.dlrs === 0 ? (
          <Empty title="No acknowledged DLRs in this range" />
        ) : (
          <BarChart ariaLabel="Acknowledged DLRs by delay from submit_sm to deliver_sm" bars={bars} formatValue={(v) => `${fmtNum(v)} DLRs`} />
        )}
      </Card>
      <div className="stack">
        <div className="tile">
          <div className="tile-label">Acknowledged DLRs</div>
          <div className="tile-value">{fmtNum(d.dlrs)}</div>
        </div>
        <div className="tile">
          <div className="tile-label">Average delay</div>
          <div className="tile-value">{d.avg_ms === null ? "—" : fmtMs(d.avg_ms)}</div>
          {query.gateway_id !== undefined && <ConfiguredTiming id={query.gateway_id} />}
        </div>
        {d.dlrs > 0 && (
          <div className="table-wrap card">
            <table>
              <thead>
                <tr>
                  <th>Delay</th>
                  <th className="num">DLRs</th>
                </tr>
              </thead>
              <tbody>
                {bars
                  .filter((b) => b.value > 0)
                  .map((b) => (
                    <tr key={b.title}>
                      <td>{b.title}</td>
                      <td className="num mono">{fmtNum(b.value)}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  );
}

function ConfiguredTiming({ id }: { id: number }) {
  const g = useGateway(id).data;
  if (!g) return null;
  const e = g.effective;
  const timing =
    e.dlr_timing === "immediate" ? "immediately" : e.dlr_timing === "fixed" ? `after ${fmtMs(e.dlr_delay_ms)}` : `${fmtMs(e.dlr_min_ms)} to ${fmtMs(e.dlr_max_ms)}`;
  return <div className="tile-sub">Configured: {timing}</div>;
}

// ---- throttling --------------------------------------------------------------------

function ThrottlingTab({ query }: { query: ReportQuery }) {
  const r = useTrafficReport(query);
  const data = r.data;
  const { times, counts } = useMemo(() => (data ? timeline(data.rows, data.from, data.to, data.step) : { times: [], counts: [] }), [data]);
  if (r.isLoading) return <Loading />;
  if (r.error || !data) return <ErrorBox error={r.error} />;
  const per = stepLabel(data.step);
  const byGw = new Map<number, { name: string | null; gw: number; lic: number; offered: number }>();
  for (const row of data.rows) {
    const g = byGw.get(row.gateway_id) ?? { name: row.gateway, gw: 0, lic: 0, offered: 0 };
    g.gw += row.throttled;
    g.lic += row.licence_throttled;
    g.offered += row.accepted + row.rejected + row.invalid + row.throttled + row.licence_throttled;
    byGw.set(row.gateway_id, g);
  }
  const rows = [...byGw.entries()].filter(([, g]) => g.gw + g.lic > 0).sort((a, b) => b[1].gw + b[1].lic - (a[1].gw + a[1].lic));

  return (
    <>
      <Card title={`Throttled submit_sm per ${per}`}>
        <LineChart
          ariaLabel={`Throttled submit_sm per ${per}: gateway TPS limit and licence limit`}
          times={times}
          unit=""
          formatTime={timeFormatter(data.step)}
          legendValue={total}
          emptyText="No traffic in this range."
          series={[
            { name: "Gateway TPS limit", color: "var(--series-1)", values: counts.map((c) => c.throttled) },
            { name: "Licence limit", color: "var(--series-2)", values: counts.map((c) => c.licence_throttled) },
          ]}
        />
        <p className="muted small">
          <Term info="licenceThrottled">Licence throttling</Term> is counted once licensing arrives (M8).
        </p>
      </Card>
      <Card title="By gateway">
        {rows.length === 0 ? (
          <Empty title="Nothing was throttled in this range" />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Gateway</th>
                  <th className="num">
                    <Term info="throttled">Gateway limit</Term>
                  </th>
                  <th className="num">Licence limit</th>
                  <th className="num">Share of submit_sm</th>
                </tr>
              </thead>
              <tbody>
                {rows.map(([id, g]) => (
                  <tr key={id}>
                    <td>{g.name ? <Link to={`/gateways/${id}`}>{g.name}</Link> : <span className="muted">deleted (#{id})</span>}</td>
                    <td className="num mono">{fmtNum(g.gw)}</td>
                    <td className="num mono">{fmtNum(g.lic)}</td>
                    <td className="num mono">{g.offered ? `${((100 * (g.gw + g.lic)) / g.offered).toFixed(2)}%` : "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  );
}

// ---- MO traffic --------------------------------------------------------------------

const MO_KEYS = ["acknowledged", "retried", "failed", "queued", "expired", "dropped", "test"] as const;
type MoCounts = Record<(typeof MO_KEYS)[number], number>;

function MoTab({ query }: { query: ReportQuery }) {
  const r = useMoReport(query);
  const data = r.data;
  const { times, counts, perGateway } = useMemo(() => {
    const empty = { times: [] as number[], counts: [] as MoCounts[], perGateway: [] as [number, { name: string | null; c: MoCounts }][] };
    if (!data) return empty;
    const zero = Object.fromEntries(MO_KEYS.map((k) => [k, 0])) as MoCounts;
    const plus = (a: MoCounts, b: MoCounts) => Object.fromEntries(MO_KEYS.map((k) => [k, a[k] + b[k]])) as MoCounts;
    const byT = new Map<number, MoCounts>();
    const byGw = new Map<number, { name: string | null; c: MoCounts }>();
    for (const row of data.rows) {
      byT.set(row.t, plus(byT.get(row.t) ?? zero, row));
      const g = byGw.get(row.gateway_id) ?? { name: row.gateway, c: zero };
      byGw.set(row.gateway_id, { name: g.name, c: plus(g.c, row) });
    }
    const ms = data.step * 1000;
    for (let t = Math.floor(Date.parse(data.from) / ms) * ms; t < Date.parse(data.to); t += ms) {
      empty.times.push(t);
      empty.counts.push(byT.get(t) ?? zero);
    }
    empty.perGateway = [...byGw.entries()].sort((a, b) => b[1].c.acknowledged - a[1].c.acknowledged);
    return empty;
  }, [data]);

  if (r.isLoading) return <Loading />;
  if (r.error || !data) return <ErrorBox error={r.error} />;
  const per = stepLabel(data.step);
  return (
    <>
      <Card title={<Term info="moGenerator">{`MO deliver_sm per ${per}`}</Term>}>
        <LineChart
          ariaLabel={`MO deliver_sm per ${per}: acknowledged, queued and dropped or failed`}
          times={times}
          unit=""
          formatTime={timeFormatter(data.step)}
          legendValue={total}
          emptyText="No MO traffic in this range."
          series={[
            { name: "Acknowledged", color: "var(--series-1)", values: counts.map((c) => c.acknowledged) },
            { name: "Queued", color: "var(--series-2)", values: counts.map((c) => c.queued) },
            { name: "Dropped or failed", color: "var(--series-3)", values: counts.map((c) => c.dropped + c.failed) },
          ]}
        />
      </Card>
      <Card title="By gateway">
        {perGateway.length === 0 ? (
          <Empty title="No MO traffic in this range" />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Gateway</th>
                  <th className="num">Acknowledged</th>
                  <th className="num">Retried</th>
                  <th className="num">Failed</th>
                  <th className="num">Queued</th>
                  <th className="num">Expired</th>
                  <th className="num">Dropped</th>
                  <th className="num">
                    <Term info="moTest">Test MOs</Term>
                  </th>
                </tr>
              </thead>
              <tbody>
                {perGateway.map(([id, g]) => (
                  <tr key={id}>
                    <td>{g.name ? <Link to={`/gateways/${id}`}>{g.name}</Link> : <span className="muted">deleted (#{id})</span>}</td>
                    {MO_KEYS.map((k) => (
                      <td key={k} className="num mono">
                        {fmtNum(g.c[k])}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  );
}

// ---- bind history ------------------------------------------------------------------

const EVENT_LABEL = { bound: "Bound", bind_failed: "Bind failed", closed: "Closed", killed: "Killed" } as const;
const EVENT_TONE = { bound: "good", bind_failed: "critical", closed: "neutral", killed: "warning" } as const;

function BindsTab({ query }: { query: ReportQuery }) {
  const r = useBindReport(query);
  if (r.isLoading) return <Loading />;
  if (r.error || !r.data) return <ErrorBox error={r.error} />;
  const rows = r.data.rows;
  return (
    <Card title={<Term info="bindEvent">Bind history</Term>}>
      {rows.length === 0 ? (
        <Empty title="No binds in this range">
          <p className="muted">Bind history covers the last 3 days.</p>
        </Empty>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Time</th>
                <th>Gateway</th>
                <th>Event</th>
                <th>Bind type</th>
                <th>System ID</th>
                <th>Remote</th>
                <th>Session</th>
                <th className="num">Status</th>
                <th className="num">submit_sm</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((e) => (
                <tr key={`${e.ts}:${e.session_id}:${e.event}`}>
                  <td className="nowrap">{fmtTime(e.ts)}</td>
                  <td>{e.gateway ? <Link to={`/gateways/${e.gateway_id}`}>{e.gateway}</Link> : <span className="muted">#{e.gateway_id}</span>}</td>
                  <td>
                    <Badge tone={EVENT_TONE[e.event]}>{EVENT_LABEL[e.event]}</Badge>
                  </td>
                  <td className="mono">{e.bind_type.toUpperCase() || "—"}</td>
                  <td className="mono">{e.system_id || "—"}</td>
                  <td className="mono">{e.remote}</td>
                  <td className="mono">{e.session_id}</td>
                  <td className="num mono">{e.event === "bind_failed" ? hex(e.status) : "—"}</td>
                  <td className="num mono">{e.event === "closed" || e.event === "killed" ? fmtNum(e.submits) : "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {r.data.truncated && <p className="muted small">Showing the newest {fmtNum(rows.length)}. Narrow the range or pick a gateway to see older ones.</p>}
        </div>
      )}
    </Card>
  );
}
