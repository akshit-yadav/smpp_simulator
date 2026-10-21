// Gateway settings (DLR and limits). In "inherit" mode every field can follow
// the global default (sent as null) or hold a custom value; the defaults page
// uses the same fields without the inherit switch.

import type { ReactNode } from "react";
import type { BindType, Defaults, Settings, SettingKey } from "../api/types";
import { fmtMs } from "../lib/format";
import { Check } from "./ui";

type Values = { [K in SettingKey]?: Settings[K] };
type OnChange = <K extends SettingKey>(key: K, value: Settings[K]) => void;

interface Props {
  values: Values;
  defaults: Defaults;
  inherit: boolean;
  onChange: OnChange;
}

function Row({
  label,
  hint,
  k,
  props,
  display,
  children,
}: {
  label: string;
  hint?: ReactNode;
  k: SettingKey;
  props: Props;
  display: string;
  children: (value: NonNullable<Settings[SettingKey]>) => ReactNode;
}) {
  const { values, defaults, inherit, onChange } = props;
  const own = values[k];
  const useDefault = inherit && (own === null || own === undefined);
  const current = (useDefault ? defaults[k] : (own ?? defaults[k])) as NonNullable<Settings[SettingKey]>;
  return (
    <div className="field">
      <div className="row" style={{ justifyContent: "space-between" }}>
        <span className="field-label">{label}</span>
        {inherit && (
          <Check checked={useDefault} onChange={(d) => onChange(k, d ? null : (defaults[k] as never))}>
            <span className="small muted">Default</span>
          </Check>
        )}
      </div>
      {useDefault ? (
        <div className="input" aria-disabled style={{ display: "flex", alignItems: "center", background: "var(--surface-2)", color: "var(--text-2)" }}>
          {display}
        </div>
      ) : (
        children(current)
      )}
      {hint && <div className="field-hint">{hint}</div>}
    </div>
  );
}

function num(v: string): number {
  const n = Number(v);
  return Number.isFinite(n) && n >= 0 ? Math.floor(n) : 0;
}

const bool = (b: boolean) => (b ? "Yes" : "No");

export function DlrFields(p: Props) {
  const d = p.defaults;
  const eff = <K extends SettingKey>(k: K) => (p.values[k] ?? d[k]) as NonNullable<Settings[K]>;
  const timing = eff("dlr_timing");
  return (
    <div className="form-grid">
      <Row label="Send DLRs" k="dlr_enabled" props={p} display={bool(d.dlr_enabled)}>
        {(v) => (
          <select className="input" value={String(v)} onChange={(e) => p.onChange("dlr_enabled", e.target.value === "true")}>
            <option value="true">Yes</option>
            <option value="false">No</option>
          </select>
        )}
      </Row>
      <Row label="Timing" k="dlr_timing" props={p} display={d.dlr_timing}>
        {(v) => (
          <select className="input" value={String(v)} onChange={(e) => p.onChange("dlr_timing", e.target.value as Settings["dlr_timing"])}>
            <option value="immediate">Immediate</option>
            <option value="fixed">Fixed delay</option>
            <option value="random">Random between min and max</option>
          </select>
        )}
      </Row>
      {timing === "fixed" && (
        <Row label="Delay (ms)" k="dlr_delay_ms" props={p} display={fmtMs(d.dlr_delay_ms)}>
          {(v) => <input className="input" type="number" min={0} value={Number(v)} onChange={(e) => p.onChange("dlr_delay_ms", num(e.target.value))} />}
        </Row>
      )}
      {timing === "random" && (
        <>
          <Row label="Minimum delay (ms)" k="dlr_min_ms" props={p} display={fmtMs(d.dlr_min_ms)}>
            {(v) => <input className="input" type="number" min={0} value={Number(v)} onChange={(e) => p.onChange("dlr_min_ms", num(e.target.value))} />}
          </Row>
          <Row label="Maximum delay (ms)" k="dlr_max_ms" props={p} display={fmtMs(d.dlr_max_ms)}>
            {(v) => <input className="input" type="number" min={0} value={Number(v)} onChange={(e) => p.onChange("dlr_max_ms", num(e.target.value))} />}
          </Row>
        </>
      )}
      <Row label="Message ID in DLR text" k="dlr_id_format" props={p} display={d.dlr_id_format}>
        {(v) => (
          <select className="input" value={String(v)} onChange={(e) => p.onChange("dlr_id_format", e.target.value as Settings["dlr_id_format"])}>
            <option value="hex">Hex (as in submit_sm_resp)</option>
            <option value="decimal">Decimal</option>
          </select>
        )}
      </Row>
      <Row label="Dates with seconds" k="dlr_date_seconds" props={p} display={bool(d.dlr_date_seconds)} hint="YYMMDDhhmmss instead of YYMMDDhhmm">
        {(v) => (
          <select className="input" value={String(v)} onChange={(e) => p.onChange("dlr_date_seconds", e.target.value === "true")}>
            <option value="false">No</option>
            <option value="true">Yes</option>
          </select>
        )}
      </Row>
      <Row label="Include text:" k="dlr_include_text" props={p} display={bool(d.dlr_include_text)}>
        {(v) => (
          <select className="input" value={String(v)} onChange={(e) => p.onChange("dlr_include_text", e.target.value === "true")}>
            <option value="true">Yes</option>
            <option value="false">No</option>
          </select>
        )}
      </Row>
      <Row label="Include TLVs" k="dlr_include_tlvs" props={p} display={bool(d.dlr_include_tlvs)} hint="receipted_message_id, message_state, network_error_code">
        {(v) => (
          <select className="input" value={String(v)} onChange={(e) => p.onChange("dlr_include_tlvs", e.target.value === "true")}>
            <option value="true">Yes</option>
            <option value="false">No</option>
          </select>
        )}
      </Row>
    </div>
  );
}

const BIND_TYPES: BindType[] = ["tx", "rx", "trx"];

export function LimitFields(p: Props) {
  const d = p.defaults;
  return (
    <div className="form-grid">
      <Row label="TPS limit" k="tps_limit" props={p} display={d.tps_limit ? `${d.tps_limit}/s` : "Unlimited"} hint="0 = unlimited. Over the limit answers ESME_RTHROTTLED (0x58).">
        {(v) => <input className="input" type="number" min={0} value={Number(v)} onChange={(e) => p.onChange("tps_limit", num(e.target.value))} />}
      </Row>
      <Row label="TPS limit applies" k="tps_scope" props={p} display={d.tps_scope === "bind" ? "Per bind" : "Per gateway"}>
        {(v) => (
          <select className="input" value={String(v)} onChange={(e) => p.onChange("tps_scope", e.target.value as Settings["tps_scope"])}>
            <option value="bind">Per bind (each session gets the limit)</option>
            <option value="gateway">Per gateway (shared by all sessions)</option>
          </select>
        )}
      </Row>
      <Row label="Max binds" k="max_binds" props={p} display={d.max_binds ? String(d.max_binds) : "Unlimited"} hint="0 = unlimited">
        {(v) => <input className="input" type="number" min={0} value={Number(v)} onChange={(e) => p.onChange("max_binds", num(e.target.value))} />}
      </Row>
      <Row label="Allowed bind types" k="bind_types" props={p} display={d.bind_types.map((b) => b.toUpperCase()).join(", ")}>
        {(v) => {
          const sel = v as BindType[];
          return (
            <div className="row" style={{ minHeight: 34 }}>
              {BIND_TYPES.map((b) => (
                <Check
                  key={b}
                  checked={sel.includes(b)}
                  onChange={(on) => {
                    const next = on ? [...sel, b] : sel.filter((x) => x !== b);
                    if (next.length) p.onChange("bind_types", BIND_TYPES.filter((x) => next.includes(x)));
                  }}
                >
                  {b.toUpperCase()}
                </Check>
              ))}
            </div>
          );
        }}
      </Row>
      <Row
        label="Response latency (ms)"
        k="response_latency_ms"
        props={p}
        display={fmtMs(d.response_latency_ms)}
        hint="Stored now; the worker applies it from milestone M6."
      >
        {(v) => <input className="input" type="number" min={0} value={Number(v)} onChange={(e) => p.onChange("response_latency_ms", num(e.target.value))} />}
      </Row>
    </div>
  );
}
