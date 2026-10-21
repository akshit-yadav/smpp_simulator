import { useState } from "react";
import { previewProfile, useCommandStatuses, useDlrErrors } from "../api/queries";
import type { DlrStatus, OutcomeEntry, Preview, ProfileInput } from "../api/types";
import { fmtNum, hex } from "../lib/format";
import { ErrorBox, Segmented } from "./ui";

const DLR_STATUSES: DlrStatus[] = ["DELIVRD", "UNDELIV", "EXPIRED", "REJECTD", "DELETED", "UNKNOWN", "ACCEPTD", "ENROUTE"];

export const emptyEntry = (): OutcomeEntry => ({
  name: "",
  command_status: 0,
  dlr_status: "DELIVRD",
  dlr_error: "000",
  weight_bp: 0,
  dlr_delay_min_ms: null,
  dlr_delay_max_ms: null,
});

export const newProfile = (): ProfileInput => ({
  name: "",
  description: "",
  mode: "weighted",
  distribution: "probabilistic",
  seed: null,
  entries: [{ ...emptyEntry(), name: "Delivered", weight_bp: 10000 }],
});

/** Problems the API would reject, shown before saving. */
export function profileProblems(p: ProfileInput): string[] {
  const out: string[] = [];
  if (p.mode === "weighted") {
    const total = p.entries.reduce((s, e) => s + e.weight_bp, 0);
    if (total !== 10000) out.push(`Weights total ${(total / 100).toFixed(2)}%; they must total 100%.`);
  }
  p.entries.forEach((e, i) => {
    if (e.command_status === 0 && !e.dlr_status) out.push(`Row ${i + 1}: choose a DLR status.`);
    if ((e.dlr_delay_min_ms === null) !== (e.dlr_delay_max_ms === null))
      out.push(`Row ${i + 1}: set both delay values or neither.`);
    if (e.dlr_delay_min_ms !== null && e.dlr_delay_max_ms !== null && e.dlr_delay_min_ms > e.dlr_delay_max_ms)
      out.push(`Row ${i + 1}: minimum delay is above maximum.`);
  });
  return out;
}

export default function ProfileEditor({ value, onChange }: { value: ProfileInput; onChange: (p: ProfileInput) => void }) {
  const statuses = useCommandStatuses();
  const errors = useDlrErrors();
  const [preview, setPreview] = useState<Preview | null>(null);
  const [previewError, setPreviewError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const set = (patch: Partial<ProfileInput>) => onChange({ ...value, ...patch });
  const setEntry = (i: number, patch: Partial<OutcomeEntry>) =>
    set({ entries: value.entries.map((e, j) => (j === i ? { ...e, ...patch } : e)) });

  const total = value.entries.reduce((s, e) => s + e.weight_bp, 0);
  const rejects = (statuses.data ?? []).filter((s) => s.code !== 0);

  const runPreview = async () => {
    setBusy(true);
    setPreviewError(null);
    try {
      setPreview(await previewProfile({ ...value, name: value.name || "preview" }));
    } catch (e) {
      setPreviewError(e);
      setPreview(null);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="stack">
      <div className="row" style={{ gap: 20 }}>
        <div className="field">
          <span className="field-label">Mode</span>
          <Segmented
            label="Mode"
            value={value.mode}
            onChange={(mode) => set({ mode })}
            options={[
              { value: "fixed", label: "Fixed" },
              { value: "random", label: "Random" },
              { value: "weighted", label: "Weighted" },
            ]}
          />
        </div>
        {value.mode !== "fixed" && (
          <div className="field">
            <span className="field-label">Distribution</span>
            <Segmented
              label="Distribution"
              value={value.distribution}
              onChange={(distribution) => set({ distribution })}
              options={[
                { value: "probabilistic", label: "Probabilistic" },
                { value: "exact", label: "Exact per 10,000" },
              ]}
            />
          </div>
        )}
        <div className="field" style={{ width: 160 }}>
          <span className="field-label">Seed (optional)</span>
          <input
            className="input"
            type="number"
            min={0}
            placeholder="random"
            value={value.seed ?? ""}
            onChange={(e) => set({ seed: e.target.value === "" ? null : Math.max(0, Math.floor(Number(e.target.value))) })}
          />
        </div>
      </div>
      <p className="small muted" style={{ margin: 0 }}>
        {value.mode === "fixed" && "Every message gets the first outcome."}
        {value.mode === "random" && "Each outcome is equally likely."}
        {value.mode === "weighted" && "Each outcome gets its share of traffic; shares must total 100%."}
        {value.mode !== "fixed" && value.distribution === "exact" && " Exact: every block of 10,000 messages matches the shares exactly."}
      </p>

      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th style={{ minWidth: 130 }}>Name</th>
              <th style={{ minWidth: 190 }}>Response</th>
              <th style={{ minWidth: 120 }}>DLR status</th>
              <th style={{ minWidth: 190 }}>DLR error (err:)</th>
              {value.mode === "weighted" && <th className="num" style={{ minWidth: 90 }}>Share %</th>}
              <th style={{ minWidth: 170 }}>DLR delay override (ms)</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {value.entries.map((e, i) => {
              const accepted = e.command_status === 0;
              return (
                <tr key={i}>
                  <td>
                    <input className="input" value={e.name} placeholder={`Outcome ${i + 1}`} onChange={(ev) => setEntry(i, { name: ev.target.value })} />
                  </td>
                  <td>
                    <select
                      className="input"
                      value={e.command_status}
                      onChange={(ev) => {
                        const cs = Number(ev.target.value);
                        setEntry(i, cs === 0 ? { command_status: 0, dlr_status: "DELIVRD", dlr_error: "000" } : { command_status: cs, dlr_status: null, dlr_error: null, dlr_delay_min_ms: null, dlr_delay_max_ms: null });
                      }}
                    >
                      <option value={0}>Accept (ESME_ROK)</option>
                      <optgroup label="Reject with">
                        {rejects.map((s) => (
                          <option key={s.code} value={s.code}>
                            {hex(s.code)} {s.name}
                          </option>
                        ))}
                      </optgroup>
                    </select>
                  </td>
                  <td>
                    {accepted ? (
                      <select className="input" value={e.dlr_status ?? ""} onChange={(ev) => setEntry(i, { dlr_status: ev.target.value as DlrStatus })}>
                        {DLR_STATUSES.map((s) => (
                          <option key={s}>{s}</option>
                        ))}
                      </select>
                    ) : (
                      <span className="muted small">No DLR</span>
                    )}
                  </td>
                  <td>
                    {accepted && (
                      <select
                        className="input"
                        value={e.dlr_error ?? "000"}
                        onChange={(ev) => {
                          const err = errors.data?.find((x) => x.code === ev.target.value);
                          setEntry(i, { dlr_error: ev.target.value, ...(err && ev.target.value !== "000" ? { dlr_status: err.suggested_status } : {}) });
                        }}
                      >
                        {(errors.data ?? []).map((x) => (
                          <option key={x.code} value={x.code}>
                            {x.code} · {x.label}
                          </option>
                        ))}
                      </select>
                    )}
                  </td>
                  {value.mode === "weighted" && (
                    <td>
                      <input
                        className="input num"
                        type="number"
                        min={0}
                        max={100}
                        step={0.01}
                        value={e.weight_bp / 100}
                        onChange={(ev) => setEntry(i, { weight_bp: Math.round(Math.max(0, Number(ev.target.value)) * 100) })}
                      />
                    </td>
                  )}
                  <td>
                    {accepted && (
                      <div className="row" style={{ flexWrap: "nowrap", gap: 6 }}>
                        <input
                          className="input"
                          type="number"
                          min={0}
                          placeholder="min"
                          aria-label="Minimum delay"
                          value={e.dlr_delay_min_ms ?? ""}
                          onChange={(ev) => setEntry(i, { dlr_delay_min_ms: ev.target.value === "" ? null : Math.max(0, Math.floor(Number(ev.target.value))) })}
                        />
                        <input
                          className="input"
                          type="number"
                          min={0}
                          placeholder="max"
                          aria-label="Maximum delay"
                          value={e.dlr_delay_max_ms ?? ""}
                          onChange={(ev) => setEntry(i, { dlr_delay_max_ms: ev.target.value === "" ? null : Math.max(0, Math.floor(Number(ev.target.value))) })}
                        />
                      </div>
                    )}
                  </td>
                  <td>
                    <button
                      type="button"
                      className="btn sm ghost"
                      aria-label={`Remove outcome ${i + 1}`}
                      disabled={value.entries.length === 1}
                      onClick={() => set({ entries: value.entries.filter((_, j) => j !== i) })}
                    >
                      ✕
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <div className="row" style={{ justifyContent: "space-between" }}>
        <div className="row">
          <button type="button" className="btn sm" disabled={value.entries.length >= 50} onClick={() => set({ entries: [...value.entries, emptyEntry()] })}>
            Add outcome
          </button>
          <button type="button" className="btn sm" onClick={runPreview} disabled={busy}>
            {busy ? "Simulating…" : "Preview 10,000 messages"}
          </button>
        </div>
        {value.mode === "weighted" && (
          <span className={total === 10000 ? "weight-ok" : "weight-bad"}>
            {total === 10000 ? "✓" : "✗"} Total {(total / 100).toFixed(2)}%
          </span>
        )}
      </div>
      <ErrorBox error={previewError} />
      {preview && <PreviewResult preview={preview} />}
    </div>
  );
}

function PreviewResult({ preview }: { preview: Preview }) {
  return (
    <div className="card" style={{ background: "var(--surface-2)", boxShadow: "none" }}>
      <h3>
        Simulated {fmtNum(preview.messages)} messages: {fmtNum(preview.accepted)} accepted, {fmtNum(preview.rejected)} rejected
      </h3>
      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Outcome</th>
              <th className="num">Messages</th>
              <th className="num">Configured</th>
              <th className="num">Actual</th>
              <th style={{ width: "35%" }}>Share</th>
            </tr>
          </thead>
          <tbody>
            {preview.entries.map((e, i) => (
              <tr key={i}>
                <td>
                  {e.name || `Outcome ${i + 1}`}{" "}
                  <span className="muted small">
                    {e.command_status === 0 ? `${e.dlr_status} err:${e.dlr_error}` : `rejected ${hex(e.command_status)}`}
                  </span>
                </td>
                <td className="num">{fmtNum(e.count)}</td>
                <td className="num">{e.configured_pct.toFixed(2)}%</td>
                <td className="num">{e.actual_pct.toFixed(2)}%</td>
                <td>
                  <div className="bar-track" role="img" aria-label={`${e.actual_pct.toFixed(2)} percent`}>
                    <div className="bar-fill" style={{ width: `${Math.min(100, e.actual_pct)}%` }} />
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
