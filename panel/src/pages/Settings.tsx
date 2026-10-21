import { useEffect, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import QRCode from "qrcode";
import { post } from "../api/client";
import { keys, useAudit, useMe, type AuditFilter } from "../api/queries";
import type { AuditEntry, Me } from "../api/types";
import { useToast } from "../components/toast";
import { Card, ErrorBox, Field, Loading, PageHead } from "../components/ui";
import { fmtTime } from "../lib/format";

function PasswordCard() {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const mismatch = confirm.length > 0 && next !== confirm;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await post("/auth/password", { current_password: current, new_password: next });
      toast("Password changed; other sessions were signed out");
      setCurrent("");
      setNext("");
      setConfirm("");
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Password">
      <form className="stack" onSubmit={submit} style={{ maxWidth: 380 }}>
        <Field label="Current password">{(id) => <input id={id} className="input" type="password" autoComplete="current-password" value={current} onChange={(e) => setCurrent(e.target.value)} required />}</Field>
        <Field label="New password" hint="At least 10 characters.">
          {(id) => <input id={id} className="input" type="password" autoComplete="new-password" minLength={10} value={next} onChange={(e) => setNext(e.target.value)} required />}
        </Field>
        <Field label="Repeat new password" error={mismatch ? "Passwords do not match." : undefined}>
          {(id) => <input id={id} className="input" type="password" autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required />}
        </Field>
        <ErrorBox error={error} />
        <div>
          <button className="btn primary" disabled={busy || mismatch || next.length < 10}>
            Change password
          </button>
        </div>
      </form>
    </Card>
  );
}

function TotpCard({ me }: { me: Me }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [setup, setSetup] = useState<{ secret: string; otpauth_uri: string } | null>(null);
  const [qr, setQr] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (setup) QRCode.toDataURL(setup.otpauth_uri, { margin: 1, width: 180 }).then(setQr);
  }, [setup]);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  const done = (m: Me, msg: string) => {
    qc.setQueryData(keys.me, m);
    toast(msg);
    setSetup(null);
    setPassword("");
    setCode("");
  };

  return (
    <Card title="Two-factor authentication (TOTP)">
      <div className="stack" style={{ maxWidth: 420 }}>
        {me.totp_enabled ? (
          <>
            <div className="alert good">TOTP is on. Sign-in asks for a code from your authenticator app.</div>
            <Field label="Password">{(id) => <input id={id} className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
            <Field label="Current code">{(id) => <input id={id} className="input mono" inputMode="numeric" maxLength={6} value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))} />}</Field>
            <ErrorBox error={error} />
            <div>
              <button
                type="button"
                className="btn danger"
                disabled={busy || !password || code.length !== 6}
                onClick={() => run(async () => done(await post<Me>("/auth/totp/disable", { password, code }), "TOTP turned off"))}
              >
                Turn off TOTP
              </button>
            </div>
          </>
        ) : !setup ? (
          <>
            <p className="muted" style={{ margin: 0 }}>
              Add a second step to sign-in with any authenticator app (Google Authenticator, 1Password, Authy…).
            </p>
            <Field label="Confirm your password">{(id) => <input id={id} className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />}</Field>
            <ErrorBox error={error} />
            <div>
              <button type="button" className="btn primary" disabled={busy || !password} onClick={() => run(async () => setSetup(await post("/auth/totp/setup", { password })))}>
                Set up TOTP
              </button>
            </div>
          </>
        ) : (
          <>
            <p style={{ margin: 0 }}>Scan this code with your authenticator app, then enter the 6-digit code it shows.</p>
            {qr ? <img src={qr} width={180} height={180} alt="TOTP QR code" style={{ borderRadius: 6, background: "#fff" }} /> : <Loading />}
            <div className="small muted">
              Or enter this key manually: <span className="mono">{setup.secret}</span>
            </div>
            <Field label="Code">{(id) => <input id={id} className="input mono" inputMode="numeric" maxLength={6} value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))} autoFocus />}</Field>
            <ErrorBox error={error} />
            <div className="row">
              <button
                type="button"
                className="btn primary"
                disabled={busy || code.length !== 6}
                onClick={() => run(async () => done(await post<Me>("/auth/totp/enable", { code }), "TOTP is on"))}
              >
                Turn on TOTP
              </button>
              <button type="button" className="btn" onClick={() => setSetup(null)}>
                Cancel
              </button>
            </div>
          </>
        )}
      </div>
    </Card>
  );
}

function summarize(a: AuditEntry): string {
  if (!a.details) return "";
  if (a.action.endsWith(".update")) {
    return Object.entries(a.details)
      .map(([k, v]) => (Array.isArray(v) && v.length === 2 ? `${k}: ${JSON.stringify(v[0])} → ${JSON.stringify(v[1])}` : `${k}: ${JSON.stringify(v)}`))
      .join("; ");
  }
  const d = a.details as Record<string, unknown>;
  return ["name", "label", "port", "session"].filter((k) => k in d).map((k) => `${k}: ${JSON.stringify(d[k])}`).join("; ");
}

function AuditCard() {
  const [action, setAction] = useState("");
  const [entity, setEntity] = useState("");
  const [pages, setPages] = useState<number[]>([]);
  const filter: AuditFilter = { action: action || undefined, entity_type: entity || undefined, limit: 50, before_id: pages[pages.length - 1] };
  const q = useAudit(filter);
  const rows = q.data ?? [];

  return (
    <Card title="Audit log">
      <div className="row" style={{ marginBottom: 12 }}>
        <select className="input" style={{ maxWidth: 200 }} value={entity} onChange={(e) => { setEntity(e.target.value); setPages([]); }} aria-label="Entity type">
          <option value="">All entities</option>
          {["gateway", "profile", "command_status", "dlr_error", "defaults", "worker", "admin"].map((x) => (
            <option key={x}>{x}</option>
          ))}
        </select>
        <input className="input" style={{ maxWidth: 220 }} placeholder="Action starts with… (e.g. auth.)" value={action} onChange={(e) => { setAction(e.target.value); setPages([]); }} aria-label="Action prefix" />
      </div>
      <ErrorBox error={q.error} />
      {q.isLoading ? (
        <Loading />
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Time</th>
                <th>Who</th>
                <th>Action</th>
                <th>Entity</th>
                <th>Details</th>
                <th>IP</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((a) => (
                <tr key={a.id}>
                  <td style={{ whiteSpace: "nowrap" }}>{fmtTime(a.ts)}</td>
                  <td>{a.actor}</td>
                  <td className="mono">{a.action}</td>
                  <td className="mono">{a.entity_type ? `${a.entity_type} ${a.entity_id ?? ""}` : "—"}</td>
                  <td className="small" style={{ maxWidth: 420, overflowWrap: "anywhere" }}>
                    {summarize(a)}
                  </td>
                  <td className="mono small">{a.ip ?? "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {rows.length === 0 && <p className="muted">No entries.</p>}
        </div>
      )}
      <div className="row" style={{ marginTop: 12 }}>
        <button type="button" className="btn sm" disabled={pages.length === 0} onClick={() => setPages(pages.slice(0, -1))}>
          Newer
        </button>
        <button type="button" className="btn sm" disabled={rows.length < 50} onClick={() => setPages([...pages, rows[rows.length - 1].id])}>
          Older
        </button>
      </div>
    </Card>
  );
}

export default function SettingsPage() {
  const me = useMe();
  return (
    <>
      <PageHead title="Settings & audit" sub={`Signed in as ${me.data?.username ?? ""}.`} />
      <div className="stack">
        <div className="grid grid-2">
          <PasswordCard />
          {me.data && <TotpCard me={me.data} />}
        </div>
        <AuditCard />
      </div>
    </>
  );
}
