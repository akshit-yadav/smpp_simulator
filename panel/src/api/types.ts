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
  dlr_intermediate: "off" | "requested" | "always" | null;
  dlr_intermediate_status: "ENROUTE" | "ACCEPTD" | null;
  dlr_intermediate_delay_ms: number | null;
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
  licence_throttled?: number;
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
  simulator?: number;
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
  simulator?: number;
}

export interface MessageSearchResult {
  submits: SubmitEvent[];
  dlrs: DlrEvent[];
  mos: MoEvent[];
  truncated: boolean;
}

// ---- MO generator (M7) -------------------------------------------------------------

export interface MOSources {
  kind: "list" | "range" | "prefix";
  numbers: string[];
  range_from: string | null;
  range_to: string | null;
  prefix: string;
  digits: number;
}

export interface MOSettings {
  mode: "rate" | "burst";
  rate: number;
  burst: number;
  start_at: string | null; // UTC; null = when started
  stop_after_s: number; // 0 = no limit
  stop_after_count: number;
  sources: MOSources;
  destinations: string[];
  dest_order: "round_robin" | "random";
  templates: { text: string; weight: number }[];
  keywords: string[];
  encoding: "gsm7" | "ucs2";
  long_pct: number;
  no_receiver: "queue" | "drop";
}

export interface MOStatus {
  state: "stopped" | "scheduled" | "running" | "finished";
  run_id: number;
  run_sent: number;
  sent: number;
  acknowledged: number;
  retried: number;
  failed: number;
  queued: number;
  expired: number;
  dropped: number;
  licence_skipped?: number; // generator parts not sent: over the licence TPS cap
}

export interface MOOut extends MOSettings {
  configured: boolean;
  enabled: boolean;
  run_id: number;
  updated_at: string | null;
  status: MOStatus | null;
}

export interface MOTest {
  source: string;
  destination: string;
  text: string;
  encoding: "gsm7" | "ucs2";
}

export interface MoEvent {
  ts: number;
  worker_id: number;
  gateway_id: number;
  gateway: string | null;
  session_id: number;
  message_id: string;
  message_id_dec: string;
  source_addr: string;
  dest_addr: string;
  result: "acknowledged" | "retried" | "failed" | "queued" | "expired" | "dropped";
  test: number;
  encoding: string;
  part: number;
  parts: number;
  sm_length: number;
  text: string;
  attempt: number;
  resp_ms: number;
  command_status: number;
  simulator?: number;
}

export interface MoRow {
  t: number;
  gateway_id: number;
  gateway: string | null;
  acknowledged: number;
  retried: number;
  failed: number;
  queued: number;
  expired: number;
  dropped: number;
  test: number;
}

export interface MoReport {
  from: string;
  to: string;
  step: number;
  rows: MoRow[];
}

// ---- licence (M8) --------------------------------------------------------------------

export interface LicenceWorker {
  id: number;
  name: string;
  online: boolean;
  machine_id: string | null;
  mac: string | null;
  match: "full" | "partial" | "none" | null; // null: no licence to match
}

export interface LicenceSummary {
  status: "licensed" | "free" | "expired" | "invalid" | "wrong_machine";
  reason: string;
  warning: string;
  days_left: number | null;
  max_gateways: number;
  max_tps: number;
  free_gateways: number;
  free_tps: number;
  require_both: boolean;
  gateways_used: number;
  suspended: { id: number; name: string }[];
  kept: number[];
  workers: LicenceWorker[];
  licence: null | {
    licence_id: string;
    licensee: string;
    issued_at: string;
    expires_at: string;
    max_gateways: number;
    max_tps: number;
    machines: { machine_id: string; mac: string; label?: string }[];
    features: string[];
    uploaded_at: string | null;
    uploaded_by: string | null;
  };
}

// ---- SMPP client simulator (M10) ---------------------------------------------------

export interface Tester {
  id: number;
  name: string;
  ip: string;
  version: string | null;
  online: boolean;
  mem_available_mb: number | null;
  running_run: number | null;
  last_seen: string | null;
}

export interface DelaySpec {
  min_ms: number;
  max_ms: number;
}

export interface TestSpec {
  tester_id?: number;
  gateway_ids: number[];
  sessions: number;
  bind_type: "trx" | "tx_rx";
  tps: number;
  duration_s: number;
  ramp_up_s: number;
  window: number;
  dlr: "none" | "final" | "intermediate";
  text: string;
  encoding: "gsm7" | "ucs2";
  net_delay: DelaySpec;
  resp_delay: DelaySpec;
  keep_errors: boolean;
  trace: boolean;
}

export interface TestSecond {
  t: number;
  sent: number;
  ok: number;
  throttled: number;
  rejected: number;
  errors: number;
  dlrs: number;
  p50_us: number;
  p99_us: number;
}

export interface TestLatency {
  p50_us: number;
  p90_us: number;
  p99_us: number;
  p999_us: number;
  max_us: number;
}

export interface TestResult {
  started: string;
  finished: string;
  stopped?: string;
  offered_tps: number;
  achieved_tps: number;
  sessions: number;
  bind_failed: number;
  bind_rtt_ms: number;
  sent: number;
  ok: number;
  throttled: number;
  rejected: number;
  timeouts: number;
  unanswered: number;
  not_sent_window_or_queue_full: number;
  disconnects: number;
  unexpected: number;
  statuses: Record<string, number>;
  latency: TestLatency;
  dlrs: number;
  dlrs_missing: number;
  dlrs_unmatched: number;
  dlrs_earlier_run?: number; // DLRs for an earlier run's messages, in no other count
  intermediate_dlrs: number;
  dlr_status: Record<string, number>;
  dlr_delay_p50_ms: number;
  dlr_delay_p99_ms: number;
  mo: number;
  errors: number;
  errors_logged: number;
  targets: {
    gateway_id: number;
    name: string;
    addr: string;
    sessions: number;
    bind_failed: number;
    sent: number;
    ok: number;
    throttled: number;
    rejected: number;
    timeouts: number;
    dlrs: number;
    latency: TestLatency;
  }[];
  per_session?: { session: number; gateway: string; bind_type: string; sent: number; ok: number; not_ok: number; deliver_sm: number; p50_us: number; p99_us: number }[];
  trace_full?: boolean;
  timeline: TestSecond[];
}

export interface TestRunOut {
  id: number;
  tester_id: number | null;
  status: "queued" | "running" | "done" | "stopped" | "failed";
  spec: Omit<TestSpec, "tester_id" | "keep_errors"> & { targets: { gateway_id: number; name: string; port: number }[] };
  keep_errors: boolean;
  stop_requested: boolean;
  created_by: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
  error: string | null;
  summary: { sent: number; ok: number; errors: number; achieved_tps: number; stopped?: string } | null;
  result?: TestResult | null;
  progress?: { progress: { phase: string; elapsed_ms: number; sent: number; ok: number; errors: number; dlrs: number }; mem_available_mb: number } | null;
  timeline?: TestSecond[];
  timeline_since?: number; // the timeline holds only the points after this t
  warnings?: string[];
}

export interface TestError {
  ts: string;
  gateway: string;
  session: number;
  kind: string;
  command?: string;
  seq?: number;
  status?: string;
  detail?: string;
}

// ---- footer resource usage -----------------------------------------------------------

export interface SystemUsage {
  ts: number;
  server: {
    cpus: number;
    cpu_pct: number | null; // null until a second sample exists
    load1: number;
    mem_total_mb: number;
    mem_used_mb: number;
    mem_pct: number;
    swap_total_mb: number;
    swap_used_mb: number;
  };
  psg: { available: boolean; reason: string | null; cpu_pct: number | null; mem_mb: number | null };
}
