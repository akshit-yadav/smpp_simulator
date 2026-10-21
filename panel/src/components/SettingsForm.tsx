// Gateway settings (DLR and limits), shared by the gateway editor and the
// Gateway Defaults page. On a gateway, a field shows its own value or, if it
// has none (null), the default; editing it makes it custom. The tab's
// "Set default" button clears every field back to following the defaults.

import { useId, type ReactNode } from "react";
import type { BindType, Defaults, Settings, SettingKey } from "../api/types";
import type { GlossaryKey } from "../lib/glossary";
import { Check, InfoTip } from "./ui";

type Values = { [K in SettingKey]?: Settings[K] };
type OnChange = <K extends SettingKey>(key: K, value: Settings[K]) => void;

interface Props {
  values: Values;
  defaults: Defaults;
  /** Gateway editor: mark fields that differ from the defaults. */
  showCustom: boolean;
  onChange: OnChange;
}

export const DLR_KEYS: SettingKey[] = [
  "dlr_enabled", "dlr_timing", "dlr_delay_ms", "dlr_min_ms", "dlr_max_ms",
  "dlr_id_format", "dlr_date_seconds", "dlr_include_text", "dlr_include_tlvs",
];
export const LIMIT_KEYS: SettingKey[] = ["tps_limit", "tps_scope", "max_binds", "bind_types", "response_latency_ms"];

function value<K extends SettingKey>(p: Props, k: K): NonNullable<Settings[K]> {
  return (p.values[k] ?? p.defaults[k]) as NonNullable<Settings[K]>;
}

function Row({
  p,
  k,
  label,
  info,
  hint,
  children,
}: {
  p: Props;
  k: SettingKey;
  label: string;
  info: GlossaryKey;
  hint?: ReactNode;
  children: (id: string) => ReactNode;
}) {
  const id = useId();
  const custom = p.showCustom && p.values[k] !== null && p.values[k] !== undefined;
  return (
    <div className="field">
      <span className="term">
        <label htmlFor={id}>{label}</label>
        <InfoTip term={info} label={label} />
        {custom && <span className="custom-mark">• custom</span>}
      </span>
      {children(id)}
      {hint && <div className="field-hint">{hint}</div>}
    </div>
  );
}

const num = (v: string) => {
  const n = Number(v);
  return Number.isFinite(n) && n >= 0 ? Math.floor(n) : 0;
};

function YesNo({ id, value, onChange }: { id: string; value: boolean; onChange: (v: boolean) => void }) {
  return (
    <select id={id} className="input" value={String(value)} onChange={(e) => onChange(e.target.value === "true")}>
      <option value="true">Yes</option>
      <option value="false">No</option>
    </select>
  );
}

export function DlrFields(p: Props) {
  const timing = value(p, "dlr_timing");
  const timingCustom = p.showCustom && ["dlr_timing", "dlr_delay_ms", "dlr_min_ms", "dlr_max_ms"].some((k) => p.values[k as SettingKey] != null);
  const timingId = useId();
  const minMax = value(p, "dlr_min_ms") > value(p, "dlr_max_ms");
  return (
    <div className="form-grid">
      <Row p={p} k="dlr_enabled" label="Send DLRs" info="dlrEnabled">
        {(id) => <YesNo id={id} value={value(p, "dlr_enabled")} onChange={(v) => p.onChange("dlr_enabled", v)} />}
      </Row>
      <div />

      <div className="span-2 timing-block">
        <div className="field">
          <span className="term">
            <label htmlFor={timingId}>Timing</label>
            <InfoTip term="dlrTiming" label="Timing" />
            {timingCustom && <span className="custom-mark">• custom</span>}
          </span>
          <select id={timingId} className="input" value={timing} onChange={(e) => p.onChange("dlr_timing", e.target.value as Settings["dlr_timing"])}>
            <option value="immediate">Immediate</option>
            <option value="fixed">Fixed delay</option>
            <option value="random">Random delay</option>
          </select>
        </div>
        <div className="delays">
          {timing === "immediate" && <span className="muted small" style={{ alignSelf: "center" }}>Sent right after the submit_sm_resp.</span>}
          {timing === "fixed" && (
            <div className="field">
              <span className="field-label">Delay (ms)</span>
              <input className="input" type="number" min={0} aria-label="Delay in milliseconds" value={value(p, "dlr_delay_ms")} onChange={(e) => p.onChange("dlr_delay_ms", num(e.target.value))} />
            </div>
          )}
          {timing === "random" && (
            <>
              <div className="field">
                <span className="field-label">Minimum (ms)</span>
                <input className={`input ${minMax ? "invalid" : ""}`} type="number" min={0} aria-label="Minimum delay in milliseconds" value={value(p, "dlr_min_ms")} onChange={(e) => p.onChange("dlr_min_ms", num(e.target.value))} />
              </div>
              <span className="muted" style={{ paddingBottom: 8 }}>to</span>
              <div className="field">
                <span className="field-label">Maximum (ms)</span>
                <input className={`input ${minMax ? "invalid" : ""}`} type="number" min={0} aria-label="Maximum delay in milliseconds" value={value(p, "dlr_max_ms")} onChange={(e) => p.onChange("dlr_max_ms", num(e.target.value))} />
              </div>
            </>
          )}
        </div>
        {minMax && <div className="field-error span-2">The minimum delay is above the maximum.</div>}
      </div>

      <Row p={p} k="dlr_id_format" label="Message ID in DLR" info="dlrIdFormat">
        {(id) => (
          <select id={id} className="input" value={value(p, "dlr_id_format")} onChange={(e) => p.onChange("dlr_id_format", e.target.value as Settings["dlr_id_format"])}>
            <option value="hex">Hex (as in submit_sm_resp)</option>
            <option value="decimal">Decimal</option>
          </select>
        )}
      </Row>
      <Row p={p} k="dlr_date_seconds" label="Dates with seconds" info="dlrDateSeconds">
        {(id) => <YesNo id={id} value={value(p, "dlr_date_seconds")} onChange={(v) => p.onChange("dlr_date_seconds", v)} />}
      </Row>
      <Row p={p} k="dlr_include_text" label="Include text:" info="dlrIncludeText">
        {(id) => <YesNo id={id} value={value(p, "dlr_include_text")} onChange={(v) => p.onChange("dlr_include_text", v)} />}
      </Row>
      <Row p={p} k="dlr_include_tlvs" label="Include TLVs" info="dlrIncludeTlvs">
        {(id) => <YesNo id={id} value={value(p, "dlr_include_tlvs")} onChange={(v) => p.onChange("dlr_include_tlvs", v)} />}
      </Row>
    </div>
  );
}

const BIND_TYPES: BindType[] = ["tx", "rx", "trx"];

export function LimitFields(p: Props) {
  const binds = value(p, "bind_types") as BindType[];
  return (
    <div className="form-grid">
      <Row p={p} k="tps_limit" label="TPS limit" info="tpsLimit" hint="0 = unlimited">
        {(id) => <input id={id} className="input" type="number" min={0} value={value(p, "tps_limit")} onChange={(e) => p.onChange("tps_limit", num(e.target.value))} />}
      </Row>
      <Row p={p} k="tps_scope" label="TPS limit applies" info="tpsScope">
        {(id) => (
          <select id={id} className="input" value={value(p, "tps_scope")} onChange={(e) => p.onChange("tps_scope", e.target.value as Settings["tps_scope"])}>
            <option value="bind">Per bind</option>
            <option value="gateway">Per gateway (shared)</option>
          </select>
        )}
      </Row>
      <Row p={p} k="max_binds" label="Max binds" info="maxBinds" hint="0 = unlimited">
        {(id) => <input id={id} className="input" type="number" min={0} value={value(p, "max_binds")} onChange={(e) => p.onChange("max_binds", num(e.target.value))} />}
      </Row>
      <Row p={p} k="bind_types" label="Allowed bind types" info="bindTypes">
        {() => (
          <div className="row" style={{ minHeight: 34 }}>
            {BIND_TYPES.map((b) => (
              <Check
                key={b}
                checked={binds.includes(b)}
                onChange={(on) => {
                  const next = on ? [...binds, b] : binds.filter((x) => x !== b);
                  if (next.length) p.onChange("bind_types", BIND_TYPES.filter((x) => next.includes(x)));
                }}
              >
                {b.toUpperCase()}
              </Check>
            ))}
          </div>
        )}
      </Row>
      <Row p={p} k="response_latency_ms" label="Response latency (ms)" info="responseLatency">
        {(id) => <input id={id} className="input" type="number" min={0} value={value(p, "response_latency_ms")} onChange={(e) => p.onChange("response_latency_ms", num(e.target.value))} />}
      </Row>
    </div>
  );
}

/** "Following Gateway Defaults" / "N settings are custom" + the Set default button. */
export function DefaultsBar({ values, keys, onReset }: { values: Values; keys: SettingKey[]; onReset: () => void }) {
  const custom = keys.filter((k) => values[k] !== null && values[k] !== undefined).length;
  return (
    <div className="defaults-bar">
      <span className={custom ? "" : "muted"}>
        {custom === 0 ? "All settings on this tab follow Gateway Defaults." : `${custom} setting${custom === 1 ? "" : "s"} on this tab differ${custom === 1 ? "s" : ""} from Gateway Defaults.`}
      </span>
      <button type="button" className="btn sm" disabled={custom === 0} onClick={onReset}>
        Set default
      </button>
    </div>
  );
}
