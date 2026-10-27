import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError } from "../api/client";
import { checkPort, nextFreePort, useDefaults, useGateway, useRules, useSaveGateway, useWorkers } from "../api/queries";
import type { Gateway, PortCheck, Settings, SettingKey } from "../api/types";
import { DLR_KEYS, DefaultsBar, DlrFields, LIMIT_KEYS, LimitFields } from "../components/SettingsForm";
import { useToast } from "../components/toast";
import { Card, ErrorBox, Field, Loading, PageHead, Tabs } from "../components/ui";
import { randomPassword } from "../lib/format";

type Tab = "connection" | "dlr" | "limits" | "mo";

interface Form {
  name: string;
  worker_id: number | null;
  port: string;
  system_id: string;
  password: string;
  status: "active" | "disabled";
  notes: string;
  dlr_rule_id: number | null; // null = Gateway Defaults' rule
  settings: Partial<Settings>;
}

const SETTING_KEYS = [...DLR_KEYS, ...LIMIT_KEYS];

function fromGateway(g: Gateway): Form {
  const settings: Partial<Settings> = {};
  for (const k of SETTING_KEYS) (settings as Record<string, unknown>)[k] = g.settings[k];
  return {
    name: g.name,
    worker_id: g.worker_id,
    port: String(g.port),
    system_id: g.system_id,
    password: "",
    status: g.status === "disabled" ? "disabled" : "active",
    notes: g.notes,
    dlr_rule_id: g.settings.dlr_rule_id,
    settings,
  };
}

export default function GatewayEditor() {
  const { id } = useParams();
  const editing = id !== undefined;
  const gwId = Number(id);
  const existing = useGateway(editing ? gwId : 0);
  const defaults = useDefaults();
  const workers = useWorkers(0);
  const rules = useRules();
  const save = useSaveGateway(editing ? gwId : undefined);
  const navigate = useNavigate();
  const toast = useToast();

  const [tab, setTab] = useState<Tab>("connection");
  const [form, setForm] = useState<Form | null>(null);
  const [showPw, setShowPw] = useState(!editing);
  const [portCheck, setPortCheck] = useState<PortCheck | null>(null);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    if (form) return;
    if (editing) {
      if (existing.data) setForm(fromGateway(existing.data));
    } else if (workers.data) {
      const active = workers.data.filter((w) => w.status === "active");
      setForm({
        name: "", worker_id: active.length === 1 ? active[0].id : null, port: "", system_id: "",
        password: randomPassword(), status: "active", notes: "", dlr_rule_id: null, settings: {},
      });
      if (active.length === 1) nextFreePort(active[0].id).then((r) => r.port && setForm((f) => (f && !f.port ? { ...f, port: String(r.port) } : f)));
    }
  }, [editing, existing.data, workers.data, form]);

  if (!form || !defaults.data) {
    return existing.error ? <ErrorBox error={existing.error} /> : <Loading />;
  }

  const set = (patch: Partial<Form>) => setForm({ ...form, ...patch });
  const setSetting = <K extends SettingKey>(k: K, v: Settings[K]) => setForm({ ...form, settings: { ...form.settings, [k]: v } });
  const resetTab = (keys: SettingKey[]) => setForm({ ...form, settings: { ...form.settings, ...Object.fromEntries(keys.map((k) => [k, null])) } });
  const worker = workers.data?.find((w) => w.id === form.worker_id);
  const port = Number(form.port);
  // A running gateway keeps its address: stop it before changing port or worker.
  const addressLocked = editing && existing.data?.status === "active";
  const defaultRule = rules.data?.find((r) => r.id === defaults.data.dlr_rule_id);

  const runCheck = async (p = port) => {
    if (!form.worker_id || !p || addressLocked) return;
    setPortCheck(await checkPort(form.worker_id, p, editing ? gwId : undefined).catch((e) => ({ ok: false, message: String(e) })));
  };
  const suggest = async () => {
    if (!form.worker_id) return;
    const r = await nextFreePort(form.worker_id, port || undefined);
    if (r.port) {
      set({ port: String(r.port) });
      setPortCheck(null);
    }
  };

  const eff = <K extends SettingKey>(k: K) => (form.settings[k] ?? defaults.data[k]) as number;
  const problems: string[] = [];
  if (!form.name.trim()) problems.push("Name is required.");
  if (!form.worker_id) problems.push("Choose a worker.");
  if (!port) problems.push("Port is required.");
  if (!form.system_id) problems.push("System ID is required.");
  if (!editing && !form.password) problems.push("Password is required.");
  if (eff("dlr_min_ms") > eff("dlr_max_ms")) problems.push("DLR Settings: the minimum delay is above the maximum.");

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    if (problems.length) return;
    const body: Record<string, unknown> = { name: form.name.trim(), system_id: form.system_id, status: form.status, notes: form.notes, dlr_rule_id: form.dlr_rule_id };
    if (!addressLocked) Object.assign(body, { worker_id: form.worker_id, port });
    for (const k of SETTING_KEYS) body[k] = form.settings[k] ?? null;
    if (form.password) body.password = form.password;
    try {
      const gw = await save.mutateAsync(body);
      toast(editing ? `Saved ${gw.name}` : `Created ${gw.name}; listening on ${gw.ip}:${gw.port}`);
      gw.warnings.forEach((w) => toast(w, true));
      navigate(`/gateways/${gw.id}`);
    } catch (err) {
      setError(err);
      if (err instanceof ApiError && err.suggestedPort) setTab("connection");
    }
  };

  const suggested = error instanceof ApiError ? error.suggestedPort : null;

  return (
    <form onSubmit={submit}>
      <PageHead
        title={editing ? `Edit ${existing.data?.name}` : "New gateway"}
        sub={editing ? "Changes go live within a second. Existing sessions keep running; new binds use the new settings." : "The gateway starts listening as soon as you save it."}
        actions={
          <>
            <Link className="btn" to={editing ? `/gateways/${gwId}` : "/gateways"}>
              Cancel
            </Link>
            <button className="btn primary" disabled={save.isPending || problems.length > 0} title={problems.join(" ")}>
              {save.isPending ? "Saving…" : editing ? "Save changes" : "Create gateway"}
            </button>
          </>
        }
      />
      {error != null && (
        <div className="alert critical" style={{ marginBottom: 16 }}>
          {(error as Error).message}{" "}
          {suggested && (
            <button type="button" className="btn sm" onClick={() => { set({ port: String(suggested) }); setError(null); }}>
              Use port {suggested}
            </button>
          )}
        </div>
      )}
      <Tabs
        value={tab}
        onChange={setTab}
        tabs={[
          { value: "connection", label: "Connection" },
          { value: "dlr", label: "DLR Settings" },
          { value: "limits", label: "Limits" },
          { value: "mo", label: "MO", disabled: true, title: "MO traffic arrives in milestone M7" },
        ]}
      />

      {tab === "connection" && (
        <Card>
          <div className="form-grid">
            <Field label="Name" info="gatewayName">
              {(fid) => <input id={fid} className="input" value={form.name} maxLength={128} onChange={(e) => set({ name: e.target.value })} required />}
            </Field>
            <Field label="Status" info="status">
              {(fid) => (
                <select id={fid} className="input" value={form.status} onChange={(e) => set({ status: e.target.value as Form["status"] })}>
                  <option value="active">Active (listening)</option>
                  <option value="disabled">Disabled (port closed)</option>
                </select>
              )}
            </Field>
            {addressLocked && (
              <div className="alert warning span-2">
                This gateway is running, so its worker and port are locked. Stop it first (Disable on the gateway page, or set Status to Disabled and save), then change the port.
              </div>
            )}
            <Field label="Worker" info="worker" hint={workers.data?.length ? undefined : "No worker has registered yet."}>
              {(fid) => (
                <select
                  id={fid}
                  className="input"
                  value={form.worker_id ?? ""}
                  disabled={addressLocked}
                  onChange={(e) => {
                    set({ worker_id: e.target.value ? Number(e.target.value) : null });
                    setPortCheck(null);
                  }}
                >
                  <option value="">Choose…</option>
                  {workers.data?.map((w) => (
                    <option key={w.id} value={w.id} disabled={w.status !== "active" && w.id !== existing.data?.worker_id}>
                      {w.name} ({w.ip}){w.status === "draining" ? " · draining" : ""}
                      {w.online ? "" : " · offline"}
                    </option>
                  ))}
                </select>
              )}
            </Field>
            <Field label="IP" info="ip">
              {(fid) => <input id={fid} className="input mono" value={worker?.ip ?? ""} disabled />}
            </Field>
            <Field
              label="Port"
              info="port"
              hint={worker && !addressLocked ? `Range ${worker.port_min}–${worker.port_max}${worker.port_exclude.length ? `, excluding ${worker.port_exclude.join(", ")}` : ""}` : undefined}
              error={portCheck && !portCheck.ok ? portCheck.message : undefined}
            >
              {(fid) => (
                <>
                  <div className="row" style={{ flexWrap: "nowrap", gap: 8 }}>
                    <input
                      id={fid}
                      className={`input mono ${portCheck && !portCheck.ok ? "invalid" : ""}`}
                      type="number"
                      min={1}
                      max={65535}
                      value={form.port}
                      disabled={addressLocked}
                      onChange={(e) => {
                        set({ port: e.target.value });
                        setPortCheck(null);
                      }}
                      onBlur={() => (!editing || port !== existing.data?.port ? runCheck() : undefined)}
                      required
                    />
                    <button type="button" className="btn" onClick={() => runCheck()} disabled={addressLocked || !form.worker_id || !port}>
                      Check
                    </button>
                    <button type="button" className="btn" onClick={suggest} disabled={addressLocked || !form.worker_id}>
                      Next free
                    </button>
                  </div>
                  {portCheck?.ok && <div className="field-hint weight-ok">✓ {portCheck.message}</div>}
                  {portCheck?.warnings?.map((w) => (
                    <div key={w} className="field-hint">
                      {w}
                    </div>
                  ))}
                  {portCheck && !portCheck.ok && portCheck.suggested_port && (
                    <button type="button" className="btn sm" style={{ alignSelf: "flex-start" }} onClick={() => { set({ port: String(portCheck.suggested_port) }); setPortCheck(null); }}>
                      Use port {portCheck.suggested_port}
                    </button>
                  )}
                </>
              )}
            </Field>
            <Field label="DLR Rule" info="dlrRule" hint={<Link to="/dlr-rules">Manage DLR Rules</Link>}>
              {(fid) => (
                <select id={fid} className="input" value={form.dlr_rule_id ?? ""} onChange={(e) => set({ dlr_rule_id: e.target.value ? Number(e.target.value) : null })}>
                  <option value="">Gateway Defaults ({defaultRule?.name ?? "…"})</option>
                  {rules.data?.map((r) => (
                    <option key={r.id} value={r.id}>
                      {r.name} ({r.mode})
                    </option>
                  ))}
                </select>
              )}
            </Field>
            <Field label="System ID" info="systemId">
              {(fid) => <input id={fid} className="input mono" value={form.system_id} maxLength={15} onChange={(e) => set({ system_id: e.target.value.replace(/\s/g, "") })} required />}
            </Field>
            <Field label="Password" info="password" hint={editing ? "Leave empty to keep the current password." : undefined}>
              {(fid) => (
                <div className="row" style={{ flexWrap: "nowrap", gap: 8 }}>
                  <input
                    id={fid}
                    className="input mono"
                    type={showPw ? "text" : "password"}
                    autoComplete="new-password"
                    value={form.password}
                    maxLength={8}
                    placeholder={editing ? "unchanged" : ""}
                    onChange={(e) => set({ password: e.target.value })}
                    required={!editing}
                  />
                  <button type="button" className="btn" onClick={() => setShowPw(!showPw)}>
                    {showPw ? "Hide" : "Show"}
                  </button>
                  <button type="button" className="btn" onClick={() => { set({ password: randomPassword() }); setShowPw(true); }}>
                    Generate
                  </button>
                </div>
              )}
            </Field>
            <Field label="Notes" className="span-2">
              {(fid) => <textarea id={fid} className="input" value={form.notes} maxLength={2000} onChange={(e) => set({ notes: e.target.value })} />}
            </Field>
          </div>
        </Card>
      )}

      {tab === "dlr" && (
        <Card title="DLR Settings">
          <DefaultsBar values={form.settings} keys={DLR_KEYS} onReset={() => resetTab(DLR_KEYS)} />
          <DlrFields values={form.settings} defaults={defaults.data} showCustom onChange={setSetting} />
        </Card>
      )}

      {tab === "limits" && (
        <Card title="Limits">
          <DefaultsBar values={form.settings} keys={LIMIT_KEYS} onReset={() => resetTab(LIMIT_KEYS)} />
          <LimitFields values={form.settings} defaults={defaults.data} showCustom onChange={setSetting} />
        </Card>
      )}

      {problems.length > 0 && (
        <div className="alert warning" style={{ marginTop: 16 }}>
          {problems.map((p) => (
            <div key={p}>{p}</div>
          ))}
        </div>
      )}
    </form>
  );
}
