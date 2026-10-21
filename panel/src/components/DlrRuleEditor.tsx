import { useState } from "react";
import { previewRule, useCommandStatuses, useDlrErrors } from "../api/queries";
import type { DlrRuleInput, DlrStatus, Preview, RuleEntry } from "../api/types";
import { fmtNum, hex } from "../lib/format";
import { ErrorBox, Segmented, Term } from "./ui";

const DLR_STATUSES: DlrStatus[] = ["DELIVRD", "UNDELIV", "EXPIRED", "REJECTD", "DELETED", "UNKNOWN", "ACCEPTD", "ENROUTE"];

export const emptyEntry = (): RuleEntry => ({ name: "", command_status: 0, dlr_status: "DELIVRD", dlr_error: "000", weight_bp: 0 });

export const newRule = (): DlrRuleInput => ({
  name: "",
  description: "",
  mode: "weighted",
  distribution: "probabilistic",
  seed: null,
  entries: [{ ...emptyEntry(), name: "Delivered", weight_bp: 10000 }],
});

/** Entries that Fixed mode ignores (all but the first). */
export const ignoredEntries = (r: DlrRuleInput) => (r.mode === "fixed" ? Math.max(0, r.entries.length - 1) : 0);

/** The rule as it is saved: Fixed mode keeps only its first entry and no seed. */
export function forSave(r: DlrRuleInput): DlrRuleInput {
  return r.mode === "fixed" ? { ...r, seed: null, entries: r.entries.slice(0, 1) } : r;
}

/** Problems the API would reject, shown before saving. */
export function ruleProblems(r: DlrRuleInput): string[] {
  const out: string[] = [];
  const used = r.mode === "fixed" ? r.entries.slice(0, 1) : r.entries;
  if (r.mode === "weighted") {
    const total = r.entries.reduce((s, e) => s + e.weight_bp, 0);
    if (total !== 10000) out.push(`Shares total ${(total / 100).toFixed(2)}%; they must total 100%.`);
  }
  used.forEach((e, i) => {
    if (e.command_status === 0 && !e.dlr_status) out.push(`Entry ${i + 1}: choose a DLR status.`);
  });
  return out;
}

export default function DlrRuleEditor({ value, onChange }: { value: DlrRuleInput; onChange: (r: DlrRuleInput) => void }) {
  const statuses = useCommandStatuses();
  const errors = useDlrErrors();
  const [preview, setPreview] = useState<Preview | null>(null);
  const [previewError, setPreviewError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const set = (patch: Partial<DlrRuleInput>) => onChange({ ...value, ...patch });
  const setEntry = (i: number, patch: Partial<RuleEntry>) => set({ entries: value.entries.map((e, j) => (j === i ? { ...e, ...patch } : e)) });

  const fixed = value.mode === "fixed";
  const weighted = value.mode === "weighted";
  const ignored = ignoredEntries(value);
  const shown = fixed ? value.entries.slice(0, 1) : value.entries;
  const total = value.entries.reduce((s, e) => s + e.weight_bp, 0);
  const rejects = (statuses.data ?? []).filter((s) => s.code !== 0);

  const runPreview = async () => {
    setBusy(true);
    setPreviewError(null);
    try {
      setPreview(await previewRule(forSave({ ...value, name: value.name || "preview" })));
    } catch (e) {
      setPreviewError(e);
      setPreview(null);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="stack">
      <div className="row" style={{ gap: 20, alignItems: "flex-start" }}>
        <div className="field">
          <Term info="mode">
            <span className="field-label">Mode</span>
          </Term>
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
        {!fixed && (
          <>
            <div className="field">
              <Term info="distribution">
                <span className="field-label">Distribution</span>
              </Term>
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
            <div className="field" style={{ width: 160 }}>
              <Term info="seed">
                <span className="field-label">Seed (optional)</span>
              </Term>
              <input
                className="input"
                type="number"
                min={0}
                placeholder="random"
                aria-label="Seed"
                value={value.seed ?? ""}
                onChange={(e) => set({ seed: e.target.value === "" ? null : Math.max(0, Math.floor(Number(e.target.value))) })}
              />
            </div>
          </>
        )}
      </div>
      <p className="small muted" style={{ margin: 0 }}>
        {fixed && "Every message gets this one result."}
        {value.mode === "random" && "Each entry is equally likely."}
        {weighted && "Each entry gets its share of traffic; shares must total 100%."}
        {!fixed && value.distribution === "exact" && " Exact: every block of 10,000 messages matches the shares exactly."}
      </p>

      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th style={{ minWidth: 140 }}>
                <Term info="entryName">Name</Term>
              </th>
              <th style={{ minWidth: 200 }}>
                <Term info="response">Response</Term>
              </th>
              <th style={{ minWidth: 130 }}>
                <Term info="dlrStatus">DLR status</Term>
              </th>
              <th style={{ minWidth: 220 }}>
                <Term info="dlrError">DLR error (err:)</Term>
              </th>
              {weighted && (
                <th className="num" style={{ minWidth: 100 }}>
                  <Term info="share">Share %</Term>
                </th>
              )}
              {!fixed && <th />}
            </tr>
          </thead>
          <tbody>
            {shown.map((e, i) => {
              const accepted = e.command_status === 0;
              return (
                <tr key={i}>
                  <td>
                    <input className="input" aria-label={`Entry ${i + 1} name`} value={e.name} placeholder={`Entry ${i + 1}`} onChange={(ev) => setEntry(i, { name: ev.target.value })} />
                  </td>
                  <td>
                    <select
                      className="input"
                      aria-label={`Entry ${i + 1} response`}
                      value={e.command_status}
                      onChange={(ev) => {
                        const cs = Number(ev.target.value);
                        setEntry(i, cs === 0 ? { command_status: 0, dlr_status: "DELIVRD", dlr_error: "000" } : { command_status: cs, dlr_status: null, dlr_error: null });
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
                      <select className="input" aria-label={`Entry ${i + 1} DLR status`} value={e.dlr_status ?? ""} onChange={(ev) => setEntry(i, { dlr_status: ev.target.value as DlrStatus })}>
                        {DLR_STATUSES.map((s) => (
                          <option key={s}>{s}</option>
                        ))}
                      </select>
                    ) : (
                      <span className="muted small">No DLR (rejected)</span>
                    )}
                  </td>
                  <td>
                    {accepted && (
                      <select
                        className="input"
                        aria-label={`Entry ${i + 1} DLR error`}
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
                  {weighted && (
                    <td>
                      <input
                        className="input num"
                        type="number"
                        min={0}
                        max={100}
                        step={0.01}
                        aria-label={`Entry ${i + 1} share percent`}
                        value={e.weight_bp / 100}
                        onChange={(ev) => setEntry(i, { weight_bp: Math.round(Math.max(0, Number(ev.target.value)) * 100) })}
                      />
                    </td>
                  )}
                  {!fixed && (
                    <td>
                      <button
                        type="button"
                        className="btn sm ghost"
                        aria-label={`Remove entry ${i + 1}`}
                        disabled={value.entries.length === 1}
                        onClick={() => set({ entries: value.entries.filter((_, j) => j !== i) })}
                      >
                        ✕
                      </button>
                    </td>
                  )}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {ignored > 0 && (
        <div className="alert warning note">
          <span>
            {ignored} {ignored === 1 ? "entry is" : "entries are"} ignored in Fixed mode. They stay if you switch back to Random or Weighted.
          </span>
          <button type="button" className="btn sm" onClick={() => set({ entries: value.entries.slice(0, 1) })}>
            Remove them
          </button>
        </div>
      )}

      <div className="row" style={{ justifyContent: "space-between" }}>
        <div className="row">
          {!fixed && (
            <button type="button" className="btn sm" disabled={value.entries.length >= 50} onClick={() => set({ entries: [...value.entries, emptyEntry()] })}>
              Add entry
            </button>
          )}
          <button type="button" className="btn sm" onClick={runPreview} disabled={busy}>
            {busy ? "Simulating…" : "Preview 10,000 messages"}
          </button>
        </div>
        {weighted && (
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
              <th>Entry</th>
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
                  {e.name || `Entry ${i + 1}`}{" "}
                  <span className="muted small">{e.command_status === 0 ? `${e.dlr_status} err:${e.dlr_error}` : `rejected ${hex(e.command_status)}`}</span>
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
