// The MO generator on the gateway page: live state, Start/Stop, settings
// (in a modal) and "Send test MO".

import { useState } from "react";
import { useMO, useMOAction } from "../api/queries";
import type { MOOut, MOSettings, MOSources } from "../api/types";
import { fmtNum } from "../lib/format";
import type { GlossaryKey } from "../lib/glossary";
import { useToast } from "./toast";
import { Badge, Card, Empty, ErrorBox, InfoTip, Loading, Modal, Segmented, Term, errorMessage } from "./ui";

const STATE = {
  stopped: { label: "Stopped", tone: "neutral" },
  scheduled: { label: "Scheduled", tone: "warning" },
  running: { label: "Running", tone: "good" },
  finished: { label: "Finished", tone: "neutral" },
} as const;

export default function MoCard({ gatewayId, active }: { gatewayId: number; active: boolean }) {
  const mo = useMO(gatewayId);
  const act = useMOAction(gatewayId);
  const toast = useToast();
  const [editing, setEditing] = useState(false);
  const [testing, setTesting] = useState(false);
  const d = mo.data;

  const run = (m: typeof act.start, ok: string) =>
    m.mutate(undefined, { onSuccess: () => toast(ok), onError: (e) => toast(errorMessage(e), true) });

  return (
    <Card
      title={<Term info="moGenerator">MO traffic</Term>}
      actions={
        d?.configured && (
          <div className="row" style={{ gap: 8 }}>
            {d.enabled ? (
              <button type="button" className="btn sm" disabled={act.stop.isPending} onClick={() => run(act.stop, "MO generator stopped")}>
                Stop
              </button>
            ) : (
              <button
                type="button"
                className="btn sm primary"
                disabled={act.start.isPending || !active}
                title={active ? undefined : "Enable the gateway first"}
                onClick={() => run(act.start, "MO generator started")}
              >
                Start
              </button>
            )}
            <button type="button" className="btn sm" onClick={() => setEditing(true)}>
              Edit settings
            </button>
            <button type="button" className="btn sm" onClick={() => setTesting(true)}>
              Send test MO
            </button>
          </div>
        )
      }
    >
      {mo.isLoading ? (
        <Loading />
      ) : mo.error || !d ? (
        <ErrorBox error={mo.error} />
      ) : !d.configured ? (
        <Empty title="No MO traffic set up">
          <p className="muted">Send MOs to this gateway's receivers at a steady rate or in a burst.</p>
          <div className="row" style={{ justifyContent: "center" }}>
            <button type="button" className="btn primary" onClick={() => setEditing(true)}>
              Set up MO traffic
            </button>
            <button type="button" className="btn" onClick={() => setTesting(true)}>
              Send test MO
            </button>
          </div>
        </Empty>
      ) : (
        <MoSummary d={d} />
      )}
      {editing && d && <MoEditor initial={d} onClose={() => setEditing(false)} onSave={(b) => act.save.mutateAsync(b)} />}
      {testing && d && (
        <MoTestModal
          initial={d}
          onClose={() => setTesting(false)}
          onSend={async (b) => {
            const r = await act.test.mutateAsync(b);
            toast(`Test MO sent (message ID ${r.message_id})`);
          }}
        />
      )}
    </Card>
  );
}

function plan(d: MOSettings): string {
  const parts = [d.mode === "rate" ? `${fmtNum(d.rate)} per second` : `burst of ${fmtNum(d.burst)}`];
  if (d.stop_after_count && d.mode === "rate") parts.push(`stop after ${fmtNum(d.stop_after_count)} messages`);
  if (d.stop_after_s) parts.push(`stop after ${fmtNum(d.stop_after_s)} s`);
  if (d.start_at) parts.push(`starts ${new Date(`${d.start_at}Z`).toLocaleString()}`);
  parts.push(d.encoding === "ucs2" ? "UCS2" : "GSM7");
  if (d.long_pct) parts.push(`${d.long_pct}% long`);
  parts.push(d.no_receiver === "drop" ? "dropped without a receiver" : "queued without a receiver");
  return parts.join(" · ");
}

function MoSummary({ d }: { d: MOOut }) {
  const s = d.status;
  const state = s && s.run_id === d.run_id ? s.state : d.enabled ? "scheduled" : "stopped";
  return (
    <div className="stack" style={{ gap: 12 }}>
      <div className="row">
        <Badge tone={STATE[state].tone}>{STATE[state].label}</Badge>
        {d.run_id > 0 && (
          <span className="muted small">
            Run #{d.run_id}: {fmtNum(s && s.run_id === d.run_id ? s.run_sent : 0)} sent
          </span>
        )}
      </div>
      <div className="small">{plan(d)}</div>
      {s && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th className="num">Sent</th>
                <th className="num">Acknowledged</th>
                <th className="num">Retried</th>
                <th className="num">Failed</th>
                <th className="num">Queued</th>
                <th className="num">Dropped</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                {[s.sent, s.acknowledged, s.retried, s.failed, s.queued, s.dropped].map((v, i) => (
                  <td key={i} className="num mono">
                    {fmtNum(v)}
                  </td>
                ))}
              </tr>
            </tbody>
          </table>
          <p className="muted small" style={{ marginBottom: 0 }}>
            Since the worker started, all runs and test MOs.
          </p>
        </div>
      )}
    </div>
  );
}

// ---- settings editor ---------------------------------------------------------------

const lines = (s: string) =>
  s
    .split(/[\n,]/)
    .map((x) => x.trim())
    .filter(Boolean);

/** UTC "YYYY-MM-DDTHH:MM:SS" from the API <-> local datetime-local value. */
function toLocalInput(utc: string | null): string {
  if (!utc) return "";
  const d = new Date(`${utc}Z`);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

function Field({ label, info, children, hint }: { label: string; info?: GlossaryKey; children: React.ReactNode; hint?: string }) {
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

function MoEditor({ initial, onClose, onSave }: { initial: MOOut; onClose: () => void; onSave: (b: MOSettings) => Promise<unknown> }) {
  const [f, setF] = useState<MOSettings>(() => ({ ...initial, sources: { ...initial.sources } }));
  const [startAt, setStartAt] = useState(toLocalInput(initial.start_at));
  const [numbers, setNumbers] = useState(initial.sources.numbers.join("\n"));
  const [dests, setDests] = useState(initial.destinations.join("\n"));
  const [keywords, setKeywords] = useState(initial.keywords.join(", "));
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const set = <K extends keyof MOSettings>(k: K, v: MOSettings[K]) => setF((x) => ({ ...x, [k]: v }));
  const setSrc = <K extends keyof MOSources>(k: K, v: MOSources[K]) => setF((x) => ({ ...x, sources: { ...x.sources, [k]: v } }));

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      await onSave({
        ...f,
        start_at: startAt ? new Date(startAt).toISOString() : null,
        sources: { ...f.sources, numbers: lines(numbers) },
        destinations: lines(dests),
        keywords: lines(keywords),
      });
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title="MO traffic settings"
      onClose={onClose}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" disabled={busy} onClick={save}>
            {busy ? "Saving…" : "Save"}
          </button>
        </>
      }
    >
      <div className="stack mo-editor" style={{ gap: 14 }}>
        <div className="form-grid">
          <Field label="Mode" info="moMode">
            <Segmented<"rate" | "burst">
              label="Mode"
              value={f.mode}
              onChange={(v) => set("mode", v)}
              options={[
                { value: "rate", label: "Steady rate" },
                { value: "burst", label: "Burst" },
              ]}
            />
          </Field>
          {f.mode === "rate" ? (
            <Field label="Messages per second">
              <input className="input" type="number" min={0.1} step="any" value={f.rate} onChange={(e) => set("rate", Number(e.target.value) || 0)} />
            </Field>
          ) : (
            <Field label="Burst size (messages)">
              <input className="input" type="number" min={1} value={f.burst} onChange={(e) => set("burst", int(e.target.value))} />
            </Field>
          )}
          <Field label="Start at" info="moStart" hint="Empty: when you press Start. Your local time.">
            <input className="input" type="datetime-local" value={startAt} onChange={(e) => setStartAt(e.target.value)} />
          </Field>
          <div className="row" style={{ alignItems: "flex-start" }}>
            <Field label="Stop after (s)" info="moStop">
              <input className="input" type="number" min={0} value={f.stop_after_s} onChange={(e) => set("stop_after_s", int(e.target.value))} />
            </Field>
            {f.mode === "rate" && (
              <Field label="or messages">
                <input className="input" type="number" min={0} value={f.stop_after_count} onChange={(e) => set("stop_after_count", int(e.target.value))} />
              </Field>
            )}
          </div>
        </div>

        <Field label="Source numbers" info="moSources">
          <Segmented<MOSources["kind"]>
            label="Source numbers"
            value={f.sources.kind}
            onChange={(v) => setSrc("kind", v)}
            options={[
              { value: "prefix", label: "Prefix + random digits" },
              { value: "range", label: "Range" },
              { value: "list", label: "List" },
            ]}
          />
        </Field>
        {f.sources.kind === "prefix" && (
          <div className="form-grid">
            <Field label="Prefix">
              <input className="input mono" value={f.sources.prefix} onChange={(e) => setSrc("prefix", e.target.value.trim())} placeholder="9198" />
            </Field>
            <Field label="Random digits">
              <input className="input" type="number" min={1} max={15} value={f.sources.digits} onChange={(e) => setSrc("digits", int(e.target.value))} />
            </Field>
          </div>
        )}
        {f.sources.kind === "range" && (
          <div className="form-grid">
            <Field label="From">
              <input className="input mono" value={f.sources.range_from ?? ""} onChange={(e) => setSrc("range_from", e.target.value.trim() || null)} placeholder="919800000000" />
            </Field>
            <Field label="To">
              <input className="input mono" value={f.sources.range_to ?? ""} onChange={(e) => setSrc("range_to", e.target.value.trim() || null)} placeholder="919800009999" />
            </Field>
          </div>
        )}
        {f.sources.kind === "list" && (
          <Field label="Numbers, one per line">
            <textarea className="input mono" rows={3} value={numbers} onChange={(e) => setNumbers(e.target.value)} />
          </Field>
        )}

        <div className="form-grid">
          <Field label="Destinations, one per line" info="moDestinations">
            <textarea className="input mono" rows={2} value={dests} onChange={(e) => setDests(e.target.value)} />
          </Field>
          <Field label="Use destinations">
            <select className="input" value={f.dest_order} onChange={(e) => set("dest_order", e.target.value as MOSettings["dest_order"])}>
              <option value="round_robin">In turn</option>
              <option value="random">At random</option>
            </select>
          </Field>
        </div>

        <Field label="Texts" info="moTemplates">
          <div className="stack" style={{ gap: 6 }}>
            {f.templates.map((t, i) => (
              <div key={i} className="row" style={{ flexWrap: "nowrap" }}>
                <input
                  className="input"
                  aria-label={`Text ${i + 1}`}
                  value={t.text}
                  onChange={(e) => set("templates", f.templates.map((x, j) => (j === i ? { ...x, text: e.target.value } : x)))}
                />
                <input
                  className="input"
                  style={{ width: 80 }}
                  type="number"
                  min={1}
                  aria-label={`Weight of text ${i + 1}`}
                  title="Weight"
                  value={t.weight}
                  onChange={(e) => set("templates", f.templates.map((x, j) => (j === i ? { ...x, weight: Math.max(1, int(e.target.value)) } : x)))}
                />
                <button type="button" className="btn sm ghost" disabled={f.templates.length === 1} onClick={() => set("templates", f.templates.filter((_, j) => j !== i))}>
                  Remove
                </button>
              </div>
            ))}
            <div>
              <button type="button" className="btn sm" disabled={f.templates.length >= 20} onClick={() => set("templates", [...f.templates, { text: "", weight: 1 }])}>
                Add text
              </button>
            </div>
          </div>
        </Field>
        <div className="form-grid">
          <Field label="Keywords for {keyword}" hint="Comma-separated">
            <input className="input" value={keywords} onChange={(e) => setKeywords(e.target.value)} placeholder="STOP, HELP, BAL" />
          </Field>
          <Field label="Encoding">
            <select className="input" value={f.encoding} onChange={(e) => set("encoding", e.target.value as MOSettings["encoding"])}>
              <option value="gsm7">GSM7</option>
              <option value="ucs2">UCS2 (Unicode)</option>
            </select>
          </Field>
          <Field label="Long messages (%)" info="moLong">
            <input className="input" type="number" min={0} max={100} value={f.long_pct} onChange={(e) => set("long_pct", Math.min(100, int(e.target.value)))} />
          </Field>
          <Field label="No receiver bound" info="moNoReceiver">
            <select className="input" value={f.no_receiver} onChange={(e) => set("no_receiver", e.target.value as MOSettings["no_receiver"])}>
              <option value="queue">Queue until one binds</option>
              <option value="drop">Drop</option>
            </select>
          </Field>
        </div>
        {initial.enabled && <div className="alert warning">The generator is running: saved changes apply within a second.</div>}
        <ErrorBox error={error} />
      </div>
    </Modal>
  );
}

// ---- test MO -------------------------------------------------------------------------

function firstSource(s: MOSources): string {
  if (s.kind === "list") return s.numbers[0] ?? "";
  if (s.kind === "range") return s.range_from ?? "";
  return s.prefix + "0".repeat(s.digits);
}

function MoTestModal({ initial, onClose, onSend }: { initial: MOOut; onClose: () => void; onSend: (b: { source: string; destination: string; text: string; encoding: "gsm7" | "ucs2" }) => Promise<void> }) {
  const [source, setSource] = useState(firstSource(initial.sources));
  const [destination, setDestination] = useState(initial.destinations[0] ?? "");
  const [text, setText] = useState("TEST");
  const [encoding, setEncoding] = useState(initial.encoding);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const send = async () => {
    setBusy(true);
    setError(null);
    try {
      await onSend({ source, destination, text, encoding });
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      title="Send test MO"
      onClose={onClose}
      actions={
        <>
          <button type="button" className="btn" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn primary" disabled={busy || !source || !destination || !text} onClick={send}>
            {busy ? "Sending…" : "Send"}
          </button>
        </>
      }
    >
      <div className="stack" style={{ gap: 12 }}>
        <p className="muted small" style={{ margin: 0 }}>
          <Term info="moTest">Sent now to a bound RX or TRX session.</Term>
        </p>
        <div className="form-grid">
          <Field label="From (subscriber)">
            <input className="input mono" value={source} onChange={(e) => setSource(e.target.value.trim())} />
          </Field>
          <Field label="To (short code)">
            <input className="input mono" value={destination} onChange={(e) => setDestination(e.target.value.trim())} />
          </Field>
        </div>
        <Field label="Text" hint="Sent as typed; longer texts are split into parts.">
          <textarea className="input" rows={3} value={text} onChange={(e) => setText(e.target.value)} />
        </Field>
        <Field label="Encoding">
          <select className="input" value={encoding} onChange={(e) => setEncoding(e.target.value as "gsm7" | "ucs2")}>
            <option value="gsm7">GSM7</option>
            <option value="ucs2">UCS2 (Unicode)</option>
          </select>
        </Field>
        <ErrorBox error={error} />
      </div>
    </Modal>
  );
}
