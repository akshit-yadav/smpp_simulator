import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useGateways, useTest, useTestActions, useTestErrors, useTesters, useTests } from "../api/queries";
import type { DelaySpec, TestResult, TestRunOut, TestSecond, TestSpec } from "../api/types";
import { LineChart } from "../components/charts";
import { useToast } from "../components/toast";
import { Badge, Card, Check, ConfirmButton, Empty, ErrorBox, InfoTip, Loading, PageHead, Segmented, Term, errorMessage } from "../components/ui";
import type { GlossaryKey } from "../lib/glossary";
import { fmtNum, fmtTime } from "../lib/format";

const STATUS = {
  queued: ["Queued", "neutral"],
  running: ["Running", "good"],
  done: ["Done", "good"],
  stopped: ["Stopped", "warning"],
  failed: ["Failed", "critical"],
} as const;

const us = (v: number) => (v >= 1000 ? `${(v / 1000).toFixed(v >= 10_000 ? 0 : 1)} ms` : `${v} µs`);
const clockFmt = (t: number) => new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });

export default function TesterPage() {
  const { id } = useParams();
  const runId = id ? Number(id) : null;
  const [creating, setCreating] = useState(false);
  return (
    <>
      <PageHead
        title="Gateway tester"
        sub={<Term info="tester">Stress-test PSG's own gateways with its SMPP client simulator.</Term>}
        actions={
          !creating && (
            <button type="button" className="btn primary" onClick={() => setCreating(true)}>
              New test
            </button>
          )
        }
      />
      <div className="stack">
        {creating && <NewTest onClose={() => setCreating(false)} />}
        {runId !== null && <RunView id={runId} />}
        <Testers />
        <History current={runId} />
      </div>
    </>
  );
}

// ---- testers -----------------------------------------------------------------------

function Testers() {
  const q = useTesters();
  return (
    <Card title={<Term info="tester">Testers</Term>}>
      {q.isLoading ? (
        <Loading />
      ) : !q.data?.length ? (
        <Empty title="No tester has registered">
          <p className="muted">Start the psg-tester container (docker compose up -d tester), here or on another machine. It appears within a few seconds.</p>
        </Empty>
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Tester</th>
                <th>IP</th>
                <th>State</th>
                <th className="num">Free memory</th>
                <th>Version</th>
                <th>Last seen</th>
              </tr>
            </thead>
            <tbody>
              {q.data.map((t) => (
                <tr key={t.id}>
                  <td>
                    {t.name} <span className="muted small">#{t.id}</span>
                  </td>
                  <td className="mono">{t.ip || "—"}</td>
                  <td>
                    {!t.online ? (
                      <Badge>Offline</Badge>
                    ) : t.running_run ? (
                      <Link to={`/tester/${t.running_run}`}>
                        <Badge tone="good">Running test {t.running_run}</Badge>
                      </Link>
                    ) : (
                      <Badge tone="good">Idle</Badge>
                    )}
                  </td>
                  <td className="num mono">{t.mem_available_mb != null ? `${fmtNum(t.mem_available_mb)} MB` : "—"}</td>
                  <td className="mono small">{t.version || "—"}</td>
                  <td>{fmtTime(t.last_seen)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}

// ---- new test ----------------------------------------------------------------------

type DelayMode = "off" | "fixed" | "random";

function Field({ label, info, hint, children }: { label: string; info?: GlossaryKey; hint?: string; children: React.ReactNode }) {
  return (
    <div className="field">
      <span className="term">
        <span className="field-label">{label}</span>
        {info && <InfoTip term={info} label={label} />}
      </span>
      {children}
      {hint && <div className="field-hint">{hint}</div>}
    </div>
  );
}

const int = (v: string) => Math.max(0, Math.floor(Number(v) || 0));

function DelayInput({ label, info, value, onChange }: { label: string; info: GlossaryKey; value: DelaySpec; onChange: (d: DelaySpec) => void }) {
  const mode: DelayMode = value.max_ms === 0 ? "off" : value.min_ms === value.max_ms ? "fixed" : "random";
  return (
    <Field label={label} info={info}>
      <div className="row" style={{ flexWrap: "nowrap" }}>
        <Segmented<DelayMode>
          label={label}
          value={mode}
          onChange={(m) => onChange(m === "off" ? { min_ms: 0, max_ms: 0 } : m === "fixed" ? { min_ms: value.max_ms || 50, max_ms: value.max_ms || 50 } : { min_ms: value.min_ms || 10, max_ms: Math.max(value.max_ms, (value.min_ms || 10) + 40) })}
          options={[
            { value: "off", label: "Off" },
            { value: "fixed", label: "Fixed" },
            { value: "random", label: "Random" },
          ]}
        />
        {mode === "fixed" && (
          <input className="input" style={{ width: 100 }} type="number" min={1} max={10000} aria-label={`${label} (ms)`} value={value.max_ms} onChange={(e) => onChange({ min_ms: int(e.target.value), max_ms: int(e.target.value) })} />
        )}
        {mode === "random" && (
          <>
            <input className="input" style={{ width: 90 }} type="number" min={0} aria-label={`${label} minimum (ms)`} value={value.min_ms} onChange={(e) => onChange({ ...value, min_ms: int(e.target.value) })} />
            <span className="muted">to</span>
            <input className="input" style={{ width: 90 }} type="number" min={1} aria-label={`${label} maximum (ms)`} value={value.max_ms} onChange={(e) => onChange({ ...value, max_ms: int(e.target.value) })} />
          </>
        )}
        {mode !== "off" && <span className="muted small">ms</span>}
      </div>
    </Field>
  );
}

const DEFAULT_SPEC: TestSpec = {
  gateway_ids: [],
  sessions: 4,
  bind_type: "trx",
  tps: 1000,
  duration_s: 60,
  ramp_up_s: 0,
  window: 100,
  dlr: "final",
  text: "PSG test {seq}",
  encoding: "gsm7",
  net_delay: { min_ms: 0, max_ms: 0 },
  resp_delay: { min_ms: 0, max_ms: 0 },
  keep_errors: true,
  trace: true,
};

function NewTest({ onClose }: { onClose: () => void }) {
  const testers = useTesters();
  const gateways = useGateways();
  const act = useTestActions();
  const toast = useToast();
  const navigate = useNavigate();
  const [f, setF] = useState<TestSpec>(DEFAULT_SPEC);
  const [error, setError] = useState<unknown>(null);
  const set = <K extends keyof TestSpec>(k: K, v: TestSpec[K]) => setF((x) => ({ ...x, [k]: v }));
  const online = (testers.data ?? []).filter((t) => t.online);
  const listening = (gateways.data ?? []).filter((g) => g.status === "active" && g.runtime?.state === "listening");

  const start = async () => {
    setError(null);
    try {
      const run = await act.start.mutateAsync({ ...f, tester_id: f.tester_id ?? online[0]?.id });
      (run.warnings ?? []).forEach((w) => toast(w, true));
      toast(`Test ${run.id} queued`);
      onClose();
      navigate(`/tester/${run.id}`);
    } catch (e) {
      setError(e);
    }
  };

  return (
    <Card title="New test">
      <div className="stack" style={{ gap: 14 }}>
        <div className="form-grid">
          <Field label="Tester" info="tester">
            <select className="input" value={f.tester_id ?? online[0]?.id ?? ""} onChange={(e) => set("tester_id", Number(e.target.value))}>
              {online.length === 0 && <option value="">No tester online</option>}
              {online.map((t) => (
                <option key={t.id} value={t.id} disabled={!!t.running_run}>
                  {t.name} ({t.ip}){t.running_run ? " – busy" : ""}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Sessions per gateway" info="testSessions">
            <div className="row" style={{ flexWrap: "nowrap" }}>
              <input className="input" style={{ width: 90 }} type="number" min={1} max={100} value={f.sessions} onChange={(e) => set("sessions", Math.max(1, int(e.target.value)))} />
              <Segmented<"trx" | "tx_rx">
                label="Bind type"
                value={f.bind_type}
                onChange={(v) => set("bind_type", v)}
                options={[
                  { value: "trx", label: "TRX" },
                  { value: "tx_rx", label: "TX + RX" },
                ]}
              />
            </div>
          </Field>
        </div>

        <Field label="Gateways (listening)">
          {listening.length === 0 ? (
            <span className="muted">No gateway is listening.</span>
          ) : (
            <div className="keep-grid">
              {listening.map((g) => (
                <Check
                  key={g.id}
                  checked={f.gateway_ids.includes(g.id)}
                  onChange={(on) => set("gateway_ids", on ? [...f.gateway_ids, g.id] : f.gateway_ids.filter((x) => x !== g.id))}
                >
                  {g.name} <span className="muted small mono">:{g.port} {g.system_id}</span>
                </Check>
              ))}
            </div>
          )}
        </Field>

        <div className="form-grid">
          <Field label="TPS (total)" info="testTps">
            <input className="input" type="number" min={1} max={50000} value={f.tps} onChange={(e) => set("tps", Math.max(1, Number(e.target.value) || 0))} />
          </Field>
          <Field label="Duration (s)" hint="Up to 2 hours">
            <input className="input" type="number" min={1} max={7200} value={f.duration_s} onChange={(e) => set("duration_s", Math.max(1, int(e.target.value)))} />
          </Field>
          <Field label="Ramp-up (s)" info="testRamp">
            <input className="input" type="number" min={0} max={600} value={f.ramp_up_s} onChange={(e) => set("ramp_up_s", int(e.target.value))} />
          </Field>
          <Field label="Window per session" info="testWindow">
            <input className="input" type="number" min={1} max={1000} value={f.window} onChange={(e) => set("window", Math.max(1, int(e.target.value)))} />
          </Field>
          <Field label="DLRs" info="testDlr">
            <select className="input" value={f.dlr} onChange={(e) => set("dlr", e.target.value as TestSpec["dlr"])}>
              <option value="final">Request the final DLR</option>
              <option value="intermediate">Final + intermediate (0x11)</option>
              <option value="none">No DLR</option>
            </select>
          </Field>
          <Field label="Text" hint="{seq} becomes the message number">
            <div className="row" style={{ flexWrap: "nowrap" }}>
              <input className="input" value={f.text} onChange={(e) => set("text", e.target.value)} />
              <select className="input" style={{ width: 110 }} aria-label="Encoding" value={f.encoding} onChange={(e) => set("encoding", e.target.value as TestSpec["encoding"])}>
                <option value="gsm7">GSM7</option>
                <option value="ucs2">UCS2</option>
              </select>
            </div>
          </Field>
        </div>

        <div className="form-grid">
          <DelayInput label="Network delay (each way)" info="netDelay" value={f.net_delay} onChange={(d) => set("net_delay", d)} />
          <DelayInput label="Client response delay (deliver_sm)" info="respDelay" value={f.resp_delay} onChange={(d) => set("resp_delay", d)} />
        </div>
        <div className="row">
          <Check checked={f.keep_errors} onChange={(v) => set("keep_errors", v)}>
            <Term info="keepErrors">Keep errors 24 h</Term>
          </Check>
          <Check checked={f.trace} onChange={(v) => set("trace", v)}>
            <Term info="pduTrace">Temporary PDU trace</Term>
          </Check>
        </div>
        <ErrorBox error={error} />
        <div className="row">
          <button type="button" className="btn primary" disabled={act.start.isPending || !f.gateway_ids.length || !online.length} onClick={start}>
            {act.start.isPending ? "Starting…" : "Start test"}
          </button>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <span className="muted small">
            {fmtNum(f.gateway_ids.length * f.sessions * (f.bind_type === "tx_rx" ? 2 : 1))} sessions, about {fmtNum(Math.round(f.tps * Math.max(0, f.duration_s - f.ramp_up_s / 2)))} submit_sm
          </span>
        </div>
      </div>
    </Card>
  );
}

// ---- one run -----------------------------------------------------------------------

function RunView({ id }: { id: number }) {
  const q = useTest(id);
  const act = useTestActions();
  const toast = useToast();
  if (q.isLoading) return <Loading />;
  if (q.error || !q.data) return <ErrorBox error={q.error} />;
  const r = q.data;
  const res = r.result;
  const timeline: TestSecond[] = res?.timeline ?? r.timeline ?? [];
  const live = r.progress?.progress;
  const spec = r.spec;
  const [label, tone] = STATUS[r.status];

  return (
    <Card
      title={
        <span className="row" style={{ gap: 8 }}>
          Test {r.id} <Badge tone={tone}>{label}</Badge>
          {r.stop_requested && r.status === "running" && <span className="muted small">stopping…</span>}
        </span>
      }
      actions={
        r.status === "running" || r.status === "queued" ? (
          <button type="button" className="btn sm" onClick={() => act.stop.mutate(r.id, { onError: (e) => toast(errorMessage(e), true) })}>
            Stop
          </button>
        ) : (
          res && <DownloadButtons r={r} res={res} />
        )
      }
    >
      <div className="stack" style={{ gap: 14 }}>
        <p className="small muted" style={{ margin: 0 }}>
          {spec.targets.map((t) => `${t.name} (:${t.port})`).join(", ")} · {fmtNum(spec.tps)} TPS for {fmtNum(spec.duration_s)} s · {spec.sessions}{" "}
          {spec.bind_type === "tx_rx" ? "TX + RX" : "TRX"} sessions each · window {spec.window} · DLR {spec.dlr}
          {spec.net_delay.max_ms > 0 && ` · network delay ${spec.net_delay.min_ms}–${spec.net_delay.max_ms} ms`}
          {spec.resp_delay.max_ms > 0 && ` · response delay ${spec.resp_delay.min_ms}–${spec.resp_delay.max_ms} ms`}
        </p>
        {r.error && <div className="alert critical">{r.error}</div>}
        {res?.stopped && <div className="alert warning">Ended early: {res.stopped}.</div>}

        <div className="grid grid-4">
          <div className="tile">
            <div className="tile-label">Sent</div>
            <div className="tile-value">{fmtNum(res?.sent ?? live?.sent ?? 0)}</div>
            <div className="tile-sub">{res ? `${fmtNum(Math.round(res.achieved_tps))} TPS achieved` : live ? `${live.phase}, ${Math.round(live.elapsed_ms / 1000)} s` : "waiting for the tester"}</div>
          </div>
          <div className="tile">
            <div className="tile-label">Accepted</div>
            <div className="tile-value">{fmtNum(res?.ok ?? live?.ok ?? 0)}</div>
            <div className="tile-sub">{res ? `${fmtNum(res.throttled)} throttled · ${fmtNum(res.rejected)} rejected` : ""}</div>
          </div>
          <div className="tile">
            <div className="tile-label">Response time</div>
            <div className="tile-value small-value">{res ? `${us(res.latency.p50_us)} / ${us(res.latency.p99_us)}` : timeline.length ? `${us(timeline[timeline.length - 1].p50_us)} / ${us(timeline[timeline.length - 1].p99_us)}` : "—"}</div>
            <div className="tile-sub">
              p50 / p99{res && <> · <Term info="bindRtt">network {res.bind_rtt_ms.toFixed(1)} ms</Term></>}
            </div>
          </div>
          <div className="tile">
            <div className="tile-label">Errors</div>
            <div className="tile-value">{fmtNum(res?.errors ?? live?.errors ?? 0)}</div>
            <div className="tile-sub">{res ? `${fmtNum(res.timeouts)} timeouts · ${fmtNum(res.disconnects)} disconnects` : r.progress ? `tester memory ${fmtNum(r.progress.mem_available_mb)} MB free` : ""}</div>
          </div>
        </div>

        <div className="grid grid-2">
          <LineChart
            ariaLabel="submit_sm per second: sent, accepted and errors"
            times={timeline.map((s) => s.t)}
            unit="/s"
            formatTime={clockFmt}
            emptyText="Waiting for the first second…"
            series={[
              { name: "Sent", color: "var(--series-1)", values: timeline.map((s) => s.sent) },
              { name: "Accepted", color: "var(--series-3)", values: timeline.map((s) => s.ok) },
              { name: "Errors", color: "var(--series-2)", values: timeline.map((s) => s.errors) },
            ]}
          />
          <LineChart
            ariaLabel="submit_sm_resp time per second, p50 and p99, in milliseconds"
            times={timeline.map((s) => s.t)}
            unit=" ms"
            formatTime={clockFmt}
            emptyText="Waiting for the first second…"
            series={[
              { name: "p50", color: "var(--series-1)", values: timeline.map((s) => s.p50_us / 1000) },
              { name: "p99", color: "var(--series-2)", values: timeline.map((s) => s.p99_us / 1000) },
            ]}
          />
        </div>

        {res && <ResultDetails res={res} />}
        {(r.status !== "queued") && <Errors run={r} />}
      </div>
    </Card>
  );
}

function ResultDetails({ res }: { res: TestResult }) {
  const [sessions, setSessions] = useState(false);
  const statuses = Object.entries(res.statuses ?? {});
  const dlrStat = Object.entries(res.dlr_status ?? {});
  return (
    <>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Gateway</th>
              <th className="num">Sessions</th>
              <th className="num">Sent</th>
              <th className="num">Accepted</th>
              <th className="num">Throttled</th>
              <th className="num">Rejected</th>
              <th className="num">Timeouts</th>
              <th className="num">DLRs</th>
              <th className="num">p50</th>
              <th className="num">p99</th>
              <th className="num">max</th>
            </tr>
          </thead>
          <tbody>
            {res.targets.map((t) => (
              <tr key={t.gateway_id}>
                <td>
                  {t.name} <span className="muted small mono">{t.addr}</span>
                  {t.bind_failed > 0 && <Badge tone="critical">{t.bind_failed} binds failed</Badge>}
                </td>
                {[t.sessions, t.sent, t.ok, t.throttled, t.rejected, t.timeouts, t.dlrs].map((v, i) => (
                  <td key={i} className="num mono">
                    {fmtNum(v)}
                  </td>
                ))}
                <td className="num mono">{us(t.latency.p50_us)}</td>
                <td className="num mono">{us(t.latency.p99_us)}</td>
                <td className="num mono">{us(t.latency.max_us)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="grid grid-2">
        <dl className="kv">
          <dt>Response time</dt>
          <dd className="mono">
            p50 {us(res.latency.p50_us)} · p90 {us(res.latency.p90_us)} · p99 {us(res.latency.p99_us)} · p99.9 {us(res.latency.p999_us)} · max {us(res.latency.max_us)}
          </dd>
          <dt>Not sent</dt>
          <dd>{fmtNum(res.not_sent_window_or_queue_full)} (window full or sender behind)</dd>
          <dt>Unanswered</dt>
          <dd>{fmtNum(res.unanswered)}</dd>
          <dt>Error statuses</dt>
          <dd>{statuses.length ? statuses.map(([k, v]) => `${k}: ${fmtNum(v)}`).join(" · ") : "none"}</dd>
          <dt>MOs received</dt>
          <dd>{fmtNum(res.mo)}</dd>
        </dl>
        <dl className="kv">
          <dt>Final DLRs</dt>
          <dd>
            {fmtNum(res.dlrs)} received · {fmtNum(res.dlrs_missing)} missing · {fmtNum(res.dlrs_unmatched)} unmatched
          </dd>
          <dt>Intermediate DLRs</dt>
          <dd>{fmtNum(res.intermediate_dlrs)}</dd>
          <dt>DLR statuses</dt>
          <dd>{dlrStat.length ? dlrStat.map(([k, v]) => `${k || "?"}: ${fmtNum(v)}`).join(" · ") : "—"}</dd>
          <dt>DLR delay</dt>
          <dd>
            p50 {fmtNum(res.dlr_delay_p50_ms)} ms · p99 {fmtNum(res.dlr_delay_p99_ms)} ms
          </dd>
        </dl>
      </div>
      {res.per_session && res.per_session.length > 0 && (
        <div>
          <button type="button" className="btn sm" onClick={() => setSessions(!sessions)}>
            {sessions ? "Hide" : "Show"} {res.per_session.length} sessions
          </button>
          {res.trace_full && <span className="muted small"> The PDU trace hit its size cap: per-session figures cover the start of the run.</span>}
          {sessions && (
            <div className="table-wrap" style={{ marginTop: 8 }}>
              <table>
                <thead>
                  <tr>
                    <th className="num">#</th>
                    <th>Gateway</th>
                    <th>Bind</th>
                    <th className="num">Sent</th>
                    <th className="num">Accepted</th>
                    <th className="num">Not OK</th>
                    <th className="num">deliver_sm</th>
                    <th className="num">p50</th>
                    <th className="num">p99</th>
                  </tr>
                </thead>
                <tbody>
                  {res.per_session.map((s) => (
                    <tr key={s.session}>
                      <td className="num mono">{s.session}</td>
                      <td>{s.gateway}</td>
                      <td className="mono">{s.bind_type.toUpperCase()}</td>
                      {[s.sent, s.ok, s.not_ok, s.deliver_sm].map((v, i) => (
                        <td key={i} className="num mono">
                          {fmtNum(v)}
                        </td>
                      ))}
                      <td className="num mono">{us(s.p50_us)}</td>
                      <td className="num mono">{us(s.p99_us)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </>
  );
}

function Errors({ run }: { run: TestRunOut }) {
  const [open, setOpen] = useState(false);
  const q = useTestErrors(run.id, open);
  const count = run.result?.errors ?? run.progress?.progress.errors ?? 0;
  if (!count && run.status !== "running") return null;
  return (
    <div>
      <button type="button" className="btn sm" onClick={() => setOpen(!open)}>
        {open ? "Hide" : "Show"} error log
      </button>{" "}
      <span className="muted small">
        {run.keep_errors ? "Kept for 24 hours after the run." : run.status === "running" ? "Deleted when the result arrives." : "Not kept."}
        {q.data?.expires_in_s ? ` Expires in ${Math.round(q.data.expires_in_s / 3600)} h.` : ""}
      </span>
      {open &&
        (q.isLoading ? (
          <Loading />
        ) : !q.data?.errors.length ? (
          <p className="muted small">No errors kept.</p>
        ) : (
          <div className="table-wrap" style={{ marginTop: 8, maxHeight: 360, overflow: "auto" }}>
            <table>
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Kind</th>
                  <th>Gateway</th>
                  <th className="num">Session</th>
                  <th>PDU</th>
                  <th>Status</th>
                  <th>Detail</th>
                </tr>
              </thead>
              <tbody>
                {q.data.errors.map((e, i) => (
                  <tr key={i}>
                    <td className="nowrap small">{fmtTime(e.ts)}</td>
                    <td>
                      <Badge tone={e.kind === "throttled" ? "warning" : "critical"}>{e.kind.replace("_", " ")}</Badge>
                    </td>
                    <td>{e.gateway || "—"}</td>
                    <td className="num mono">{e.session ?? "—"}</td>
                    <td className="mono small">
                      {e.command || "—"}
                      {e.seq ? ` #${e.seq}` : ""}
                    </td>
                    <td className="mono small">{e.status || "—"}</td>
                    <td className="small">{e.detail || ""}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ))}
    </div>
  );
}

function download(name: string, body: string, type: string) {
  const url = URL.createObjectURL(new Blob([body], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

function DownloadButtons({ r, res }: { r: TestRunOut; res: TestResult }) {
  const csv = useMemo(() => {
    const head = "time,sent,ok,throttled,rejected,errors,dlrs,p50_us,p99_us";
    return [head, ...res.timeline.map((s) => [new Date(s.t).toISOString(), s.sent, s.ok, s.throttled, s.rejected, s.errors, s.dlrs, s.p50_us, s.p99_us].join(","))].join("\n");
  }, [res]);
  return (
    <div className="row" style={{ gap: 8 }}>
      <button type="button" className="btn sm" onClick={() => download(`psg-test-${r.id}-timeline.csv`, csv, "text/csv")}>
        Timeline CSV
      </button>
      <button type="button" className="btn sm" onClick={() => download(`psg-test-${r.id}.json`, JSON.stringify({ spec: r.spec, result: res }, null, 2), "application/json")}>
        Result JSON
      </button>
    </div>
  );
}

// ---- history -----------------------------------------------------------------------

function History({ current }: { current: number | null }) {
  const q = useTests();
  const act = useTestActions();
  const toast = useToast();
  const navigate = useNavigate();
  return (
    <Card title="Test history">
      {q.isLoading ? (
        <Loading />
      ) : !q.data?.length ? (
        <Empty title="No tests yet" />
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th className="num">#</th>
                <th>Started</th>
                <th>Gateways</th>
                <th className="num">TPS</th>
                <th className="num">Duration</th>
                <th>Status</th>
                <th className="num">Sent</th>
                <th className="num">Accepted</th>
                <th className="num">Errors</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {q.data.map((r) => (
                <tr key={r.id} className={r.id === current ? "selected" : ""}>
                  <td className="num mono">
                    <Link to={`/tester/${r.id}`}>{r.id}</Link>
                  </td>
                  <td className="nowrap">{fmtTime(r.started_at ?? r.created_at)}</td>
                  <td>{r.spec.targets.map((t) => t.name).join(", ")}</td>
                  <td className="num mono">{fmtNum(r.spec.tps)}</td>
                  <td className="num mono">{fmtNum(r.spec.duration_s)} s</td>
                  <td>
                    <Badge tone={STATUS[r.status][1]}>{STATUS[r.status][0]}</Badge>
                  </td>
                  <td className="num mono">{r.summary ? fmtNum(r.summary.sent) : "—"}</td>
                  <td className="num mono">{r.summary ? fmtNum(r.summary.ok) : "—"}</td>
                  <td className="num mono">{r.summary ? fmtNum(r.summary.errors) : "—"}</td>
                  <td>
                    {r.status !== "queued" && r.status !== "running" && (
                      <ConfirmButton
                        small
                        danger
                        label="Delete"
                        title={`Delete test ${r.id}?`}
                        confirmLabel="Delete"
                        message="Its result and any kept errors are deleted."
                        onConfirm={() =>
                          act.remove
                            .mutateAsync(r.id)
                            .then(() => current === r.id && navigate("/tester"))
                            .catch((e) => toast(errorMessage(e), true))
                        }
                      />
                    )}
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
