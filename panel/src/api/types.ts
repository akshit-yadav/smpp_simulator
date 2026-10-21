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
  outcome_profile_id: number | null;
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
  custom_outcome: boolean;
  runtime: GatewayRuntime | null;
  warnings: string[];
  created_at: string;
  updated_at: string;
}

export interface OutcomeEntry {
  name: string;
  command_status: number;
  dlr_status: DlrStatus | null;
  dlr_error: string | null;
  weight_bp: number;
  dlr_delay_min_ms: number | null;
  dlr_delay_max_ms: number | null;
}

export interface ProfileInput {
  name: string;
  description: string;
  mode: "fixed" | "random" | "weighted";
  distribution: "probabilistic" | "exact";
  seed: number | null;
  entries: OutcomeEntry[];
}

export interface Profile extends ProfileInput {
  id: number;
  owner_gateway_id: number | null;
  created_at: string;
  updated_at: string;
}

export interface Preview {
  messages: number;
  accepted: number;
  rejected: number;
  dlr_status: Record<string, number>;
  entries: (Pick<OutcomeEntry, "name" | "command_status" | "dlr_status" | "dlr_error"> & {
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
