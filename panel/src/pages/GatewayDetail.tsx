import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { revealPassword, useAudit, useDefaults, useDeleteGateway, useGateway, useGatewayRule, useKillSession, useSaveGateway, useSessions } from "../api/queries";
import type { Defaults, Gateway, SettingKey } from "../api/types";
import { LineChart } from "../components/charts";
import { RuntimeBadge } from "../components/GatewayBadges";
import MoCard from "../components/MoCard";
import { useToast } from "../components/toast";
import { Badge, Card, ConfirmButton, Empty, ErrorBox, Loading, PageHead, Term, errorMessage } from "../components/ui";
import { fmtAgo, fmtDuration, fmtMs, fmtNum, fmtRate, fmtTime, hex } from "../lib/format";
import type { GlossaryKey } from "../lib/glossary";
import { useLiveRates } from "../lib/live";

const SETTING_LABELS: [SettingKey, string, GlossaryKey, (d: Defaults) => string][] = [
  ["tps_limit", "TPS limit", "tpsLimit", (d) => (d.tps_limit ? `${fmtNum(d.tps_limit)}/s per ${d.tps_scope}` : "Unlimited")],
  ["max_binds", "Max binds", "maxBinds", (d) => (d.max_binds ? String(d.max_binds) : "Unlimited")],
  ["bind_types", "Bind types", "bindTypes", (d) => d.bind_types.map((b) => b.toUpperCase()).join(", ")],
  ["dlr_enabled", "DLRs", "dlrEnabled", (d) => (d.dlr_enabled ? "On" : "Off")],
  [
    "dlr_timing",
    "DLR timing",
    "dlrTiming",
    (d) => (d.dlr_timing === "immediate" ? "Immediate" : d.dlr_timing === "fixed" ? `After ${fmtMs(d.dlr_delay_ms)}` : `${fmtMs(d.dlr_min_ms)} – ${fmtMs(d.dlr_max_ms)}`),
  ],
  ["dlr_id_format", "DLR id: format", "dlrIdFormat", (d) => d.dlr_id_format],
  ["dlr_include_tlvs", "DLR TLVs", "dlrIncludeTlvs", (d) => (d.dlr_include_tlvs ? "Included" : "Off")],
  [
    "dlr_intermediate",
    "Intermediate DLR",
    "dlrIntermediate",
    (d) =>
      d.dlr_intermediate === "off"
        ? "Off"
        : `${d.dlr_intermediate_status} ${d.dlr_intermediate === "always" ? "always" : "when requested"}, after ${fmtMs(d.dlr_intermediate_delay_ms)}`,
  ],
];

const isCustom = (g: Gateway, k: SettingKey) => {
  if (k === "tps_limit") return g.settings.tps_limit !== null || g.settings.tps_scope !== null;
  if (k === "dlr_intermediate") return ["dlr_intermediate", "dlr_intermediate_status", "dlr_intermediate_delay_ms"].some((x) => g.settings[x as SettingKey] !== null);
  if (k === "dlr_timing") return ["dlr_timing", "dlr_delay_ms", "dlr_min_ms", "dlr_max_ms"].some((x) => g.settings[x as SettingKey] !== null);
  return g.settings[k] !== null;
};

function Password({ id }: { id: number }) {
  const [pw, setPw] = useState<string | null>(null);
  const toast = useToast();
  return (
    <span className="row" style={{ gap: 8 }}>
      <span className="mono">{pw ?? "••••••••"}</span>
      <button
        type="button"
        className="btn sm"
        onClick={async () => {
          if (pw) return setPw(null);
          try {
            setPw((await revealPassword(id)).password);
          } catch (e) {
            toast(errorMessage(e), true);
          }
        }}
      >
        {pw ? "Hide" : "Reveal"}
      </button>
    </span>
  );
}

function Sessions({ gw }: { gw: Gateway }) {
  const sessions = useSessions(gw.id);
  const kill = useKillSession(gw.id);
  const toast = useToast();
  const list = sessions.data ?? [];
  return (
    <Card title={`Sessions (${list.length})`}>
      <ErrorBox error={sessions.error} />
      {list.length === 0 ? (
        <p className="muted" style={{ margin: 0 }}>
          No bound sessions. Clients bind to <span className="mono">{gw.ip}:{gw.port}</span> with system ID <span className="mono">{gw.system_id}</span>.
        </p>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Remote</th>
                <th>Bind</th>
                <th>Bound for</th>
                <th className="num">submit_sm</th>
                <th className="num">
                  <Term info="dlrsInFlight">DLRs in flight</Term>
                </th>
                <th />
              </tr>
            </thead>
            <tbody>
              {list.map((s) => (
                <tr key={s.id}>
                  <td className="mono">{s.remote}</td>
                  <td>
                    <Badge plain>{s.bind_type.toUpperCase()}</Badge>
                  </td>
                  <td>{fmtDuration(s.bound_at)}</td>
                  <td className="num">{fmtNum(s.submits)}</td>
                  <td className="num">{fmtNum(s.dlrs_in_flight)}</td>
                  <td className="num">
                    <ConfirmButton
                      small
                      danger
                      label="Kill"
                      title="Kill this session?"
                      message={`The worker sends unbind to ${s.remote} and closes the connection. In-flight DLRs are re-routed to another receiver or queued.`}
                      confirmLabel="Kill session"
                      onConfirm={async () => {
                        await kill.mutateAsync(s.id);
                        toast(`Session ${s.remote} killed`);
                      }}
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

function History({ id }: { id: number }) {
  const audit = useAudit({ entity_type: "gateway", entity_id: String(id), limit: 20 });
  return (
    <Card title="Change history">
      <ErrorBox error={audit.error} />
      {!audit.data?.length ? (
        <p className="muted" style={{ margin: 0 }}>
          No changes recorded.
        </p>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>When</th>
                <th>Who</th>
                <th>What</th>
                <th>Changes</th>
              </tr>
            </thead>
            <tbody>
              {audit.data.map((a) => (
                <tr key={a.id}>
                  <td title={fmtTime(a.ts)}>{fmtAgo(a.ts)}</td>
                  <td>{a.actor}</td>
                  <td className="mono">{a.action}</td>
                  <td className="small">
                    {a.action === "gateway.update" && a.details
                      ? Object.entries(a.details).map(([k, v]) => (
                          <div key={k}>
                            <span className="muted">{k}:</span> {Array.isArray(v) ? `${JSON.stringify(v[0])} → ${JSON.stringify(v[1])}` : JSON.stringify(v)}
                          </div>
                        ))
                      : a.action === "session.kill"
                        ? `session ${String((a.details as { session?: number })?.session)}`
                        : ""}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

export default function GatewayDetail() {
  const id = Number(useParams().id);
  const q = useGateway(id);
  const rule = useGatewayRule(id);
  const defaults = useDefaults();
  const save = useSaveGateway(id);
  const remove = useDeleteGateway();
  const { history, latest } = useLiveRates();
  const navigate = useNavigate();
  const toast = useToast();

  if (q.isLoading) return <Loading />;
  if (!q.data) return <ErrorBox error={q.error ?? "Gateway not found."} />;
  const g = q.data;
  const r = latest?.gateways[g.id];
  const times = history.map((s) => s.ts);
  const series = (k: "accepted" | "throttled" | "rejected") => history.map((s) => s.gateways[g.id]?.[k] ?? 0);

  return (
    <>
      <PageHead
        title={
          <span className="row" style={{ gap: 10 }}>
            {g.name} <RuntimeBadge gw={g} />
          </span>
        }
        sub={
          <>
            <span className="mono">
              {g.ip}:{g.port}
            </span>{" "}
            · system ID <span className="mono">{g.system_id}</span> · {g.worker_name}
          </>
        }
        actions={
          <>
            <button
              type="button"
              className="btn"
              disabled={save.isPending || g.status === "suspended"}
              onClick={() =>
                save.mutate(
                  { status: g.status === "active" ? "disabled" : "active" },
                  {
                    onSuccess: (gw) => toast(gw.status === "active" ? `${gw.name} enabled` : `${gw.name} disabled; port closed`),
                    onError: (e) => toast(errorMessage(e), true),
                  },
                )
              }
            >
              {g.status === "active" ? "Disable" : "Enable"}
            </button>
            <Link className="btn primary" to={`/gateways/${g.id}/edit`}>
              Edit
            </Link>
            <ConfirmButton
              danger
              label="Delete"
              title={`Delete ${g.name}?`}
              message="The listener stops, bound sessions are unbound, and the gateway's configuration is removed. This cannot be undone."
              confirmLabel="Delete gateway"
              onConfirm={async () => {
                await remove.mutateAsync(g.id);
                toast(`Deleted ${g.name}`);
                navigate("/gateways");
              }}
            />
          </>
        }
      />
      <div className="stack">
        {g.runtime?.state === "error" && <div className="alert critical">The worker could not start this listener: {g.runtime.error}</div>}

        <div className="grid grid-4">
          <div className="tile">
            <div className="tile-label">Accepted</div>
            <div className="tile-value">{fmtRate(r?.accepted ?? 0)}/s</div>
          </div>
          <div className="tile">
            <div className="tile-label">Throttled</div>
            <div className="tile-value">{fmtRate(r?.throttled ?? 0)}/s</div>
          </div>
          <div className="tile">
            <div className="tile-label">DLRs sent</div>
            <div className="tile-value">{fmtRate(r?.dlr ?? 0)}/s</div>
          </div>
          <div className="tile">
            <div className="tile-label">Bound sessions</div>
            <div className="tile-value">{fmtNum(r?.sessions ?? 0)}</div>
          </div>
        </div>

        <Card title="Traffic">
          <LineChart
            ariaLabel={`${g.name} submit_sm per second`}
            times={times}
            height={180}
            series={[
              { name: "Accepted", color: "var(--series-1)", values: series("accepted") },
              { name: "Throttled", color: "var(--series-2)", values: series("throttled") },
              { name: "Rejected", color: "var(--series-3)", values: series("rejected") },
            ]}
          />
        </Card>

        <Sessions gw={g} />

        <div className="grid grid-2">
          <Card title="Connection">
            <dl className="kv">
              <dt>
                <Term info="ip">Host</Term>
              </dt>
              <dd className="mono">{g.ip}</dd>
              <dt>
                <Term info="port">Port</Term>
              </dt>
              <dd className="mono">{g.port}</dd>
              <dt>
                <Term info="systemId">System ID</Term>
              </dt>
              <dd className="mono">{g.system_id}</dd>
              <dt>Password</dt>
              <dd>
                <Password id={g.id} />
              </dd>
              <dt>Status</dt>
              <dd>{g.status}</dd>
              {g.notes && (
                <>
                  <dt>Notes</dt>
                  <dd style={{ whiteSpace: "pre-wrap" }}>{g.notes}</dd>
                </>
              )}
              <dt>
                <Term info="configVersion">Config version</Term>
              </dt>
              <dd>{g.version}</dd>
            </dl>
          </Card>
          <Card title="Settings">
            <dl className="kv">
              {SETTING_LABELS.map(([k, label, info, show]) => (
                <div key={k} style={{ display: "contents" }}>
                  <dt>
                    <Term info={info}>{label}</Term>
                  </dt>
                  <dd>
                    {show(g.effective)} {!isCustom(g, k) && <span className="default-chip">· default</span>}
                  </dd>
                </div>
              ))}
            </dl>
            {defaults.data && <p className="small muted" style={{ marginBottom: 0 }}>“default” values follow Gateway Defaults.</p>}
          </Card>
        </div>

        <Card
          title={
            <>
              <Term info="dlrRule">DLR Rule</Term>{" "}
              <span className="muted small" style={{ fontWeight: 400 }}>
                {g.dlr_rule_name}
                {g.settings.dlr_rule_id === null && " (from Gateway Defaults)"}
              </span>
            </>
          }
          actions={
            rule.data ? (
              <Link className="btn sm" to={`/dlr-rules/${rule.data.id}`}>
                Open rule
              </Link>
            ) : undefined
          }
        >
          {rule.data ? (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Entry</th>
                    <th>Response</th>
                    <th>DLR</th>
                    {rule.data.mode === "weighted" && <th className="num">Share</th>}
                  </tr>
                </thead>
                <tbody>
                  {rule.data.entries.map((e, i) => (
                    <tr key={i}>
                      <td>{e.name || `Entry ${i + 1}`}</td>
                      <td>{e.command_status === 0 ? "Accept" : `Reject ${hex(e.command_status)}`}</td>
                      <td className="mono">{e.command_status === 0 ? `stat:${e.dlr_status} err:${e.dlr_error}` : "—"}</td>
                      {rule.data.mode === "weighted" && <td className="num">{e.weight_bp / 100}%</td>}
                    </tr>
                  ))}
                </tbody>
              </table>
              <p className="small muted" style={{ marginBottom: 0 }}>
                Mode: {rule.data.mode}
                {rule.data.mode !== "fixed" && `, ${rule.data.distribution}`}
                {rule.data.seed !== null && `, seed ${rule.data.seed}`}
              </p>
            </div>
          ) : (
            <Loading />
          )}
        </Card>

        <MoCard gatewayId={g.id} active={g.status === "active"} />

        <Card title="Live overrides">
          <Empty title="Planned for a later phase">
            <p>Force a result, reject binds, drop sessions, black-hole or close the gateway, with optional auto-revert.</p>
          </Empty>
        </Card>

        <History id={g.id} />
      </div>
    </>
  );
}
