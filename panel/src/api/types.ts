// Shapes returned by the control-plane REST API (/api/v1).

export type BindType = "tx" | "rx" | "trx";
export type DlrStatus =
  | "ENROUTE"
  | "DELIVRD"
  | "EXPIRED"
  | "DELETED"
  | "UNDELIV"
  | "ACCEPTD"
  | "UNKNOWN"
  | "REJECTD";

export interface Me {
  username: string;
  totp_enabled: boolean;
}

/** Gateway settings. On a gateway, null means "follow the global default". */
export interface Settings {
  max_binds: number | null;
  bind_types: BindType[] | null;
  tps_limit: number | null;
  tps_scope: "bind" | "gateway" | null;
  dlr_enabled: boolean | null;
  dlr_timing: "immediate" | "fixed" | "random" | null;
  dlr_delay_ms: number | null;
  dlr_min_ms: number | null;
  dlr_max_ms: number | null;
  dlr_id_format: "hex" | "decimal" | null;
  dlr_date_seconds: boolean | null;
  dlr_include_text: boolean | null;
  dlr_include_tlvs: boolean | null;
  response_latency_ms: number | null;
  dlr_rule_id: number | null;
}

export type SettingKey = keyof Settings;
export type Defaults = { [K in SettingKey]: NonNullable<Settings[K]> } & { updated_at?: string };

export interface GatewayRuntime {
  state: "listening" | "stopped" | "error";
  port: number;
  error?: string;
  since: string;
}

export interface Gateway {
  id: number;
  name: string;
  worker_id: number;
  worker_name: string;
  ip: string;
  port: number;
  system_id: string;
  password: string;
  status: "active" | "disabled" | "suspended";
  notes: string;
  version: number;
  settings: Settings;
  effective: Defaults;
  dlr_rule_name: string;
  runtime: GatewayRuntime | null;
  warnings: string[];
  created_at: string;
  updated_at: string;
}

/** One possible result in a DLR rule. */
export interface RuleEntry {
  name: string;
  command_status: number;
  dlr_status: DlrStatus | null;
  dlr_error: string | null;
  weight_bp: number;
}

export interface DlrRuleInput {
  name: string;
  description: string;
  mode: "fixed" | "random" | "weighted";
  distribution: "probabilistic" | "exact";
  seed: number | null;
  entries: RuleEntry[];
}

export interface DlrRule extends DlrRuleInput {
  id: number;
  created_at: string;
  updated_at: string;
}

export interface Preview {
  messages: number;
  accepted: number;
  rejected: number;
  dlr_status: Record<string, number>;
  entries: (Pick<RuleEntry, "name" | "command_status" | "dlr_status" | "dlr_error"> & {
    configured_pct: number;
    count: number;
    actual_pct: number;
  })[];
}

export interface CommandStatus {
  code: number;
  hex: string;
  name: string;
  description: string;
  system: boolean;
}

export interface DlrError {
  code: string;
  label: string;
  suggested_status: DlrStatus;
  network_error_type: number | null;
  network_error_code: number | null;
  dlr_text: string | null;
  system: boolean;
}

export interface Worker {
  id: number;
  name: string;
  ip: string;
  port_min: number;
  port_max: number;
  port_exclude: number[];
  status: "active" | "draining";
  version: string | null;
  config_version: number;
  online: boolean;
  last_seen: string | null;
  gateways: number;
}

export interface AuditEntry {
  id: number;
  ts: string;
  actor: string;
  action: string;
  entity_type: string | null;
  entity_id: string | null;
  ip: string | null;
  details: Record<string, unknown> | null;
}

/** Cumulative counters for one gateway, published by its worker every second. */
export interface GatewayCounters {
  accepted: number;
  rejected: number;
  throttled: number;
  invalid: number;
  dlr_sent: number;
  dlr_acknowledged: number;
  dlr_retried: number;
  dlr_failed: number;
  dlr_queued: number;
  dlr_expired: number;
  bind_ok: number;
  bind_failed: number;
  sessions: Record<BindType, number>;
  ts: number;
  worker_id: number;
}

export interface Live {
  ts: number;
  workers: {
    id: number;
    name: string;
    online: boolean;
    ts: number | null;
    dlr_pending: number;
    dlr_queued: number;
  }[];
  gateways: Record<string, GatewayCounters>;
}

export interface Session {
  id: number;
  gateway_id: number;
  bind_type: BindType;
  system_id: string;
  remote: string;
  bound_at: string;
  submits: number;
  dlrs_in_flight: number;
}

export interface PortCheck {
  ok: boolean;
  message: string;
  suggested_port?: number | null;
  warnings?: string[];
}

// ---- reports (ClickHouse) ----------------------------------------------------------
// Times are Unix milliseconds. Message IDs are strings: they pass 2^53.

export interface TrafficRow {
  t: number;
  gateway_id: number;
  gateway: string | null; // null: gateway since deleted
  accepted: number;
  rejected: number;
  throttled: number;
  invalid: number;
  licence_throttled: number;
  dlr_acknowledged: number;
  dlr_retried: number;
  dlr_failed: number;
  dlr_queued: number;
  dlr_expired: number;
}

export interface TrafficReport {
  from: string;
  to: string;
  step: number; // seconds per row
  rows: TrafficRow[];
}

export interface MixRow {
  name: string;
  command_status: number;
  dlr_status: string | null;
  dlr_error: string | null;
  configured_pct: number | null; // null: no longer in the rule
  actual: number;
  actual_pct: number | null;
}

export interface MixReport {
  gateway_id: number;
  rule: { id: number; name: string; mode: string };
  picked: number;
  not_picked: { throttled: number; invalid: number; licence_throttled: number; receiver_only: number };
  rows: MixRow[];
}

export interface DelayReport {
  dlrs: number;
  avg_ms: number | null;
  rows: { from_ms: number; to_ms: number | null; dlrs: number }[];
}

export interface BindEvent {
  ts: number;
  gateway_id: number;
  gateway: string | null;
  worker_id: number;
  session_id: number;
  event: "bound" | "bind_failed" | "closed" | "killed";
  bind_type: string;
  system_id: string;
  remote: string;
  status: number;
  submits: number;
}

export interface BindReport {
  truncated: boolean;
  rows: BindEvent[];
}

export interface SubmitEvent {
  ts: number;
  worker_id: number;
  gateway_id: number;
  gateway: string | null;
  session_id: number;
  system_id: string;
  message_id: string; // hex, as in submit_sm_resp; "" unless accepted
  message_id_dec: string;
  source_addr: string;
  dest_addr: string;
  result: "accepted" | "rejected" | "throttled" | "invalid" | "licence_throttled";
  command_status: number;
  outcome: string;
  dlr_status: string;
  dlr_error: string;
  registered_delivery: number;
  data_coding: number;
  sm_length: number;
}

export interface DlrEvent {
  ts: number;
  worker_id: number;
  gateway_id: number;
  gateway: string | null;
  session_id: number;
  message_id: string;
  message_id_dec: string;
  source_addr: string;
  dest_addr: string;
  result: "acknowledged" | "retried" | "failed" | "queued" | "expired";
  dlr_status: string;
  dlr_error: string;
  attempt: number;
  submit_ts: number;
  delay_ms: number;
  resp_ms: number;
  command_status: number;
}

export interface MessageSearchResult {
  submits: SubmitEvent[];
  dlrs: DlrEvent[];
  truncated: boolean;
}
