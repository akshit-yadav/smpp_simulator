import { useRef, useState } from "react";
import { Link } from "react-router-dom";
import { useGateways, useLicence, useLicenceAction } from "../api/queries";
import type { LicenceSummary } from "../api/types";
import { useToast } from "../components/toast";
import { Badge, Card, Check, ConfirmButton, ErrorBox, Loading, PageHead, Term, errorMessage } from "../components/ui";
import { fmtNum, fmtTime } from "../lib/format";

const STATUS = {
  licensed: { label: "Licensed", tone: "good" },
  free: { label: "Free tier", tone: "neutral" },
  expired: { label: "Expired", tone: "critical" },
  invalid: { label: "Invalid licence", tone: "critical" },
  wrong_machine: { label: "Wrong machine", tone: "critical" },
} as const;

const MATCH = { full: ["Matches", "good"], partial: ["Partial match", "warning"], none: ["Not in licence", "critical"] } as const;

/** The licence banner shown on every page when something needs attention. */
export function LicenceBanner({ s }: { s: LicenceSummary | undefined }) {
  if (!s) return null;
  const bad = s.status === "expired" || s.status === "invalid" || s.status === "wrong_machine";
  if (!bad && !s.warning && s.suspended.length === 0) return null;
  return (
    <div className={`alert ${bad ? "critical" : "warning"}`} style={{ marginBottom: 16 }}>
      {bad && `${s.reason} The free tier applies (${s.free_gateways} gateways, ${fmtNum(s.free_tps)} TPS). `}
      {!bad && s.warning && `${s.warning} `}
      {s.suspended.length > 0 && `${s.suspended.length} gateway${s.suspended.length === 1 ? " is" : "s are"} suspended by the licence. `}
      <Link to="/licence">Open Licence</Link>
    </div>
  );
}

export default function Licence() {
  const q = useLicence();
  const act = useLicenceAction();
  const toast = useToast();
  const file = useRef<HTMLInputElement>(null);
  const [uploadError, setUploadError] = useState<unknown>(null);

  if (q.isLoading) return <Loading />;
  if (q.error || !q.data) return <ErrorBox error={q.error} />;
  const s = q.data;
  const l = s.licence;

  const upload = async (f: File | undefined) => {
    if (!f) return;
    setUploadError(null);
    try {
      const r = await act.upload.mutateAsync(await f.text());
      toast(`Licence ${r.licence?.licence_id} installed`);
    } catch (e) {
      setUploadError(e);
    } finally {
      if (file.current) file.current.value = "";
    }
  };

  return (
    <>
      <PageHead
        title="Licence"
        sub="Limits on gateways and total TPS, tied to the worker machines."
        actions={
          <>
            <a className="btn" href="/api/v1/licence/request" download>
              <Term info="licenceRequest">Request licence</Term>
            </a>
            <input ref={file} type="file" accept=".psglic,.json,.txt,application/json" hidden onChange={(e) => upload(e.target.files?.[0])} />
            <button type="button" className="btn primary" disabled={act.upload.isPending} onClick={() => file.current?.click()}>
              {act.upload.isPending ? "Uploading…" : l ? "Replace licence" : "Upload licence"}
            </button>
          </>
        }
      />
      <div className="stack">
        <ErrorBox error={uploadError} />
        <LicenceBanner s={s} />
        <div className="grid grid-4">
          <div className="tile">
            <div className="tile-label">
              <Term info="licenceStatus">Status</Term>
            </div>
            <div className="tile-value small-value">
              <Badge tone={STATUS[s.status].tone}>{STATUS[s.status].label}</Badge>
            </div>
            <div className="tile-sub">{s.reason}</div>
          </div>
          <div className="tile">
            <div className="tile-label">
              <Term info="licenceGateways">Gateways</Term>
            </div>
            <div className="tile-value">
              {fmtNum(s.gateways_used)} / {fmtNum(s.max_gateways)}
            </div>
            <div className="tile-sub">{s.suspended.length ? `${s.suspended.length} suspended` : "none suspended"}</div>
          </div>
          <div className="tile">
            <div className="tile-label">
              <Term info="licenceTps">TPS cap</Term>
            </div>
            <div className="tile-value">{fmtNum(s.max_tps)}</div>
            <div className="tile-sub">across all workers</div>
          </div>
          <div className="tile">
            <div className="tile-label">Expires</div>
            <div className="tile-value small-value">{l ? new Date(l.expires_at).toLocaleDateString() : "—"}</div>
            <div className="tile-sub">{s.days_left !== null ? `in ${fmtNum(s.days_left)} days` : l ? "" : "no licence"}</div>
          </div>
        </div>

        {l && (
          <Card
            title="Installed licence"
            actions={
              <ConfirmButton
                small
                danger
                label="Remove"
                title="Remove the licence?"
                confirmLabel="Remove"
                message={`The free tier applies at once: ${s.free_gateways} gateways and ${fmtNum(s.free_tps)} TPS. Gateways over the limit are suspended and their sessions unbound.`}
                onConfirm={() => act.remove.mutateAsync().then(() => toast("Licence removed"))}
              />
            }
          >
            <dl className="kv">
              <dt>Licence ID</dt>
              <dd className="mono">{l.licence_id}</dd>
              <dt>Licensee</dt>
              <dd>{l.licensee}</dd>
              <dt>Issued</dt>
              <dd>{fmtTime(l.issued_at)}</dd>
              <dt>Limits</dt>
              <dd>
                {fmtNum(l.max_gateways)} gateways, {fmtNum(l.max_tps)} TPS
              </dd>
              {l.features.length > 0 && (
                <>
                  <dt>Features</dt>
                  <dd>{l.features.join(", ")}</dd>
                </>
              )}
              <dt>Machines</dt>
              <dd>{l.machines.map((m) => m.label || m.machine_id.slice(0, 12)).join(", ")}</dd>
              <dt>Installed</dt>
              <dd>
                {fmtTime(l.uploaded_at)} by {l.uploaded_by}
              </dd>
            </dl>
          </Card>
        )}

        <Card title={<Term info="licenceMatch">Worker machines</Term>}>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Worker</th>
                  <th>Online</th>
                  <th>Machine ID (SHA-256)</th>
                  <th>MAC (SHA-256)</th>
                  <th>Licence</th>
                </tr>
              </thead>
              <tbody>
                {s.workers.map((w) => (
                  <tr key={w.id}>
                    <td>{w.name}</td>
                    <td>{w.online ? <Badge tone="good">Online</Badge> : <Badge>Offline</Badge>}</td>
                    <td className="mono small">{w.machine_id ? `${w.machine_id.slice(0, 16)}…` : "—"}</td>
                    <td className="mono small">{w.mac ? `${w.mac.slice(0, 16)}…` : "—"}</td>
                    <td>{w.match ? <Badge tone={MATCH[w.match][1]}>{MATCH[w.match][0]}</Badge> : <span className="muted">—</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className="muted small" style={{ marginBottom: 0 }}>
            {s.require_both ? "Both the machine ID and the MAC must match." : "A match on either the machine ID or the MAC is accepted (partial matches show a warning)."}
          </p>
        </Card>

        {s.gateways_used + s.suspended.length > s.max_gateways && <KeepCard s={s} onSave={(ids) => act.keep.mutateAsync(ids)} />}
      </div>
    </>
  );
}

function KeepCard({ s, onSave }: { s: LicenceSummary; onSave: (ids: number[]) => Promise<unknown> }) {
  const gateways = useGateways();
  const toast = useToast();
  const all = (gateways.data ?? []).slice().sort((a, b) => a.id - b.id);
  const current = s.kept.length ? s.kept : all.filter((g) => g.status !== "suspended").map((g) => g.id);
  const [picked, setPicked] = useState<number[] | null>(null);
  const sel = picked ?? current;
  const toggle = (id: number, on: boolean) => setPicked(on ? [...sel, id] : sel.filter((x) => x !== id));
  return (
    <Card title={<Term info="licenceKeep">Gateways that stay active</Term>}>
      <p className="small">
        There are {fmtNum(all.length)} gateways and the licence allows {fmtNum(s.max_gateways)}. Pick up to {fmtNum(s.max_gateways)} ({fmtNum(sel.length)}{" "}
        picked); the rest are suspended.
      </p>
      <div className="keep-grid">
        {all.map((g) => (
          <Check key={g.id} checked={sel.includes(g.id)} onChange={(on) => toggle(g.id, on)}>
            {g.name} {g.status === "suspended" && <Badge tone="warning">Suspended</Badge>}
          </Check>
        ))}
      </div>
      <div className="row" style={{ marginTop: 12 }}>
        <button
          type="button"
          className="btn primary"
          disabled={picked === null || sel.length === 0 || sel.length > s.max_gateways}
          onClick={() =>
            onSave(sel)
              .then(() => {
                setPicked(null);
                toast("Saved: the picked gateways stay active");
              })
              .catch((e) => toast(errorMessage(e), true))
          }
        >
          Save choice
        </button>
        {sel.length > s.max_gateways && <span className="field-error">Pick at most {fmtNum(s.max_gateways)}.</span>}
      </div>
    </Card>
  );
}
