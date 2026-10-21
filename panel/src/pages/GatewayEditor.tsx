import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError } from "../api/client";
import { checkPort, nextFreePort, useDefaults, useGateway, useGatewayProfile, useProfiles, useSaveGateway, useWorkers } from "../api/queries";
import type { Gateway, PortCheck, ProfileInput, Settings, SettingKey } from "../api/types";
import ProfileEditor, { newProfile, profileProblems } from "../components/ProfileEditor";
import { DlrFields, LimitFields } from "../components/SettingsForm";
import { useToast } from "../components/toast";
import { Card, ErrorBox, Field, Loading, PageHead, Segmented, Tabs } from "../components/ui";
import { randomPassword } from "../lib/format";

type Tab = "connection" | "outcomes" | "dlr" | "limits" | "mo";
type OutcomeChoice = "default" | "shared" | "custom";

const SETTING_KEYS: SettingKey[] = [
  "max_binds", "bind_types", "tps_limit", "tps_scope", "dlr_enabled", "dlr_timing", "dlr_delay_ms",
  "dlr_min_ms", "dlr_max_ms", "dlr_id_format", "dlr_date_seconds", "dlr_include_text",
  "dlr_include_tlvs", "response_latency_ms",
];

interface Form {
  name: string;
  worker_id: number | null;
  port: string;
  system_id: string;
  password: string;
  status: "active" | "disabled";
  notes: string;
  settings: Partial<Settings>;
  outcome: OutcomeChoice;
  profile_id: number | null;
  custom: ProfileInput;
}

function fromGateway(g: Gateway, custom: ProfileInput | null): Form {
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
    settings,
    outcome: g.custom_outcome ? "custom" : g.settings.outcome_profile_id ? "shared" : "default",
    profile_id: g.custom_outcome ? null : g.settings.outcome_profile_id,
    custom: custom ?? newProfile(),
  };
}

export default function GatewayEditor() {
  const { id } = useParams();
  const editing = id !== undefined;
  const gwId = Number(id);
  const existing = useGateway(editing ? gwId : 0);
  const existingProfile = useGatewayProfile(editing ? gwId : 0);
  const defaults = useDefaults();
  const workers = useWorkers(0);
  const profiles = useProfiles();
  const save = useSaveGateway(editing ? gwId : undefined);
  const navigate = useNavigate();
  const toast = useToast();

  const [tab, setTab] = useState<Tab>("connection");
  const [form, setForm] = useState<Form | null>(null);
  const [showPw, setShowPw] = useState(!editing);
  const [portCheck, setPortCheck] = useState<PortCheck | null>(null);
  const [error, setError] = useState<unknown>(null);

  // Initialise the form once its data is in.
  useEffect(() => {
    if (form) return;
    if (editing) {
      if (existing.data && existingProfile.data) {
        const p = existingProfile.data;
        const custom = existing.data.custom_outcome
          ? { name: p.name, description: p.description, mode: p.mode, distribution: p.distribution, seed: p.seed, entries: p.entries }
          : null;
        setForm(fromGateway(existing.data, custom));
      }
    } else if (workers.data) {
      const active = workers.data.filter((w) => w.status === "active");
      setForm({
        name: "", worker_id: active.length === 1 ? active[0].id : null, port: "", system_id: "",
        password: randomPassword(), status: "active", notes: "", settings: {}, outcome: "default",
        profile_id: null, custom: newProfile(),
      });
      if (active.length === 1) nextFreePort(active[0].id).then((r) => r.port && setForm((f) => (f && !f.port ? { ...f, port: String(r.port) } : f)));
    }
  }, [editing, existing.data, existingProfile.data, workers.data, form]);

  if (!form || !defaults.data) {
    return existing.error ? <ErrorBox error={existing.error} /> : <Loading />;
  }

  const set = (patch: Partial<Form>) => setForm({ ...form, ...patch });
  const setSetting = <K extends SettingKey>(k: K, v: Settings[K]) => setForm({ ...form, settings: { ...form.settings, [k]: v } });
  const worker = workers.data?.find((w) => w.id === form.worker_id);
  const port = Number(form.port);

  const runCheck = async (p = port) => {
    if (!form.worker_id || !p) return;
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

  const problems: string[] = [];
  if (!form.name.trim()) problems.push("Name is required.");
  if (!form.worker_id) problems.push("Choose a worker.");
  if (!port) problems.push("Port is required.");
  if (!form.system_id) problems.push("System ID is required.");
  if (!editing && !form.password) problems.push("Password is required.");
  if (form.outcome === "shared" && !form.profile_id) problems.push("Choose an outcome profile.");
  if (form.outcome === "custom") problems.push(...profileProblems(form.custom));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);
    if (problems.length) return;
    const body: Record<string, unknown> = {
      name: form.name.trim(), worker_id: form.worker_id, port, system_id: form.system_id,
      status: form.status, notes: form.notes,
    };
    for (const k of SETTING_KEYS) body[k] = form.settings[k] ?? null;
    if (form.password) body.password = form.password;
    if (form.outcome === "custom") {
      body.custom_outcome = { ...form.custom, name: form.custom.name || "custom" };
      body.outcome_profile_id = null;
    } else {
      body.outcome_profile_id = form.outcome === "shared" ? form.profile_id : null;
      if (editing && existing.data?.custom_outcome) body.custom_outcome = null;
    }
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
  const shared = (profiles.data ?? []).filter((p) => p.owner_gateway_id === null);
  const defaultProfile = shared.find((p) => p.id === defaults.data.outcome_profile_id);

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
            <button
              type="button"
              className="btn sm"
              onClick={() => {
                set({ port: String(suggested) });
                setError(null);
              }}
            >
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
          { value: "outcomes", label: "Outcomes" },
          { value: "dlr", label: "DLR" },
          { value: "limits", label: "Limits" },
          { value: "mo", label: "MO", disabled: true, title: "MO traffic arrives in milestone M7" },
        ]}
      />

      {tab === "connection" && (
        <Card>
          <div className="form-grid">
            <Field label="Name" hint="Must be unique.">
              {(fid) => <input id={fid} className="input" value={form.name} maxLength={128} onChange={(e) => set({ name: e.target.value })} required />}
            </Field>
            <Field label="Status">
              {(fid) => (
                <select id={fid} className="input" value={form.status} onChange={(e) => set({ status: e.target.value as Form["status"] })}>
                  <option value="active">Active (listening)</option>
                  <option value="disabled">Disabled (port closed)</option>
                </select>
              )}
            </Field>
            <Field label="Worker" hint={workers.data?.length ? undefined : "No worker has registered yet."}>
              {(fid) => (
                <select
                  id={fid}
                  className="input"
                  value={form.worker_id ?? ""}
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
            <Field label="IP" hint="Filled in from the worker.">
              {(fid) => <input id={fid} className="input mono" value={worker?.ip ?? ""} disabled />}
            </Field>
            <Field
              label="Port"
              hint={worker ? `Range ${worker.port_min}–${worker.port_max}${worker.port_exclude.length ? `, excluding ${worker.port_exclude.join(", ")}` : ""}` : undefined}
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
                      onChange={(e) => {
                        set({ port: e.target.value });
                        setPortCheck(null);
                      }}
                      onBlur={() => !editing || port !== existing.data?.port ? runCheck() : undefined}
                      required
                    />
                    <button type="button" className="btn" onClick={() => runCheck()} disabled={!form.worker_id || !port}>
                      Check
                    </button>
                    <button type="button" className="btn" onClick={suggest} disabled={!form.worker_id}>
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
            <Field label="System ID" hint="Up to 15 characters, no spaces.">
              {(fid) => (
                <input id={fid} className="input mono" value={form.system_id} maxLength={15} onChange={(e) => set({ system_id: e.target.value.replace(/\s/g, "") })} required />
              )}
            </Field>
            <Field label="Password" hint={editing ? "Leave empty to keep the current password. Up to 8 characters." : "Up to 8 characters. Stored encrypted."}>
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

      {tab === "outcomes" && (
        <Card>
          <div className="stack">
            <Segmented
              label="Outcome source"
              value={form.outcome}
              onChange={(outcome) => set({ outcome })}
              options={[
                { value: "default", label: `Default (${defaultProfile?.name ?? "—"})` },
                { value: "shared", label: "Shared profile" },
                { value: "custom", label: "Custom for this gateway" },
              ]}
            />
            {form.outcome === "default" && (
              <p className="muted" style={{ margin: 0 }}>
                Follows the default profile set on the <Link to="/defaults">Defaults</Link> page.
              </p>
            )}
            {form.outcome === "shared" && (
              <Field label="Profile">
                {(fid) => (
                  <select id={fid} className="input" style={{ maxWidth: 360 }} value={form.profile_id ?? ""} onChange={(e) => set({ profile_id: e.target.value ? Number(e.target.value) : null })}>
                    <option value="">Choose…</option>
                    {shared.map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name} ({p.mode})
                      </option>
                    ))}
                  </select>
                )}
              </Field>
            )}
            {form.outcome === "custom" && <ProfileEditor value={form.custom} onChange={(custom) => set({ custom })} />}
          </div>
        </Card>
      )}

      {tab === "dlr" && (
        <Card title="Delivery receipts">
          <p className="muted small" style={{ marginTop: -4 }}>
            A DLR is sent only when DLRs are on, the message was accepted, and the client asked for one in registered_delivery.
          </p>
          <DlrFields values={form.settings} defaults={defaults.data} inherit onChange={setSetting} />
        </Card>
      )}

      {tab === "limits" && (
        <Card title="Limits">
          <LimitFields values={form.settings} defaults={defaults.data} inherit onChange={setSetting} />
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
