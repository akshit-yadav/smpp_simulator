// React Query hooks, one per resource. Mutations invalidate what they change.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, del, get, patch, post, put, qs } from "./client";
import type {
  AuditEntry,
  BindReport,
  DelayReport,
  MessageSearchResult,
  MixReport,
  MOOut,
  MoReport,
  MOSettings,
  MOTest,
  TrafficReport,
  CommandStatus,
  Defaults,
  DlrError,
  Gateway,
  LicenceSummary,
  SystemUsage,
  TestError,
  Tester,
  TestRunOut,
  TestSpec,
  Live,
  Me,
  DlrRule,
  DlrRuleInput,
  PortCheck,
  Preview,
  Session,
  Worker,
} from "./types";

export const keys = {
  me: ["me"] as const,
  gateways: ["gateways"] as const,
  gateway: (id: number) => ["gateways", id] as const,
  sessions: (id: number) => ["sessions", id] as const,
  rules: ["dlr-rules"] as const,
  rule: (id: number) => ["dlr-rules", id] as const,
  workers: ["workers"] as const,
  defaults: ["defaults"] as const,
  commandStatuses: ["catalog", "command-statuses"] as const,
  dlrErrors: ["catalog", "dlr-errors"] as const,
  audit: (f: object) => ["audit", f] as const,
  live: ["live"] as const,
};

// ---- auth ------------------------------------------------------------------------

export const useMe = () =>
  useQuery({ queryKey: keys.me, queryFn: () => get<Me>("/auth/me"), retry: false, staleTime: 60_000 });

export function useLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (b: { username: string; password: string; totp_code?: string }) =>
      post<Me>("/auth/login", b),
    onSuccess: (me) => qc.setQueryData(keys.me, me),
  });
}

export function useLogout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => post<void>("/auth/logout"),
    onSettled: () => {
      qc.clear();
      qc.setQueryData(keys.me, null);
    },
  });
}

// ---- gateways ------------------------------------------------------------------------

export const useGateways = (refetchMs?: number) =>
  useQuery({ queryKey: keys.gateways, queryFn: () => get<Gateway[]>("/gateways"), refetchInterval: refetchMs });

export const useGateway = (id: number) =>
  useQuery({ queryKey: keys.gateway(id), queryFn: () => get<Gateway>(`/gateways/${id}`), refetchInterval: 5000, enabled: id > 0 });

export function useSaveGateway(id?: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      id ? patch<Gateway>(`/gateways/${id}`, body) : post<Gateway>("/gateways", body),
    onSuccess: (gw) => {
      qc.invalidateQueries({ queryKey: keys.gateways });
      qc.invalidateQueries({ queryKey: keys.workers });
      qc.setQueryData(keys.gateway(gw.id), gw);
      qc.invalidateQueries({ queryKey: ["gateway-rule", gw.id] });
    },
  });
}

export function useDeleteGateway() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => del(`/gateways/${id}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: keys.gateways });
      qc.invalidateQueries({ queryKey: keys.workers });
    },
  });
}

export const revealPassword = (id: number) => post<{ password: string }>(`/gateways/${id}/reveal-password`);

export const useGatewayRule = (id: number) =>
  useQuery({ queryKey: ["gateway-rule", id], queryFn: () => get<DlrRule>(`/gateways/${id}/dlr-rule`), enabled: id > 0 });

export const useSessions = (id: number) =>
  useQuery({ queryKey: keys.sessions(id), queryFn: () => get<Session[]>(`/gateways/${id}/sessions`), refetchInterval: 2000 });

export function useKillSession(gatewayId: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (sessionId: number) => del(`/gateways/${gatewayId}/sessions/${sessionId}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.sessions(gatewayId) }),
  });
}

export const checkPort = (workerId: number, port: number, gatewayId?: number) =>
  get<PortCheck>(`/ports/check${qs({ worker_id: workerId, port, gateway_id: gatewayId })}`);

export const nextFreePort = (workerId: number, start?: number) =>
  get<{ port: number | null; used: number }>(`/workers/${workerId}/ports/next-free${qs({ start })}`);

// ---- DLR rules ------------------------------------------------------------------------

export const useRules = () => useQuery({ queryKey: keys.rules, queryFn: () => get<DlrRule[]>("/dlr-rules") });

export const useRule = (id: number) =>
  useQuery({ queryKey: keys.rule(id), queryFn: () => get<DlrRule>(`/dlr-rules/${id}`), enabled: id > 0 });

export function useSaveRule(id?: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: DlrRuleInput) => (id ? put<DlrRule>(`/dlr-rules/${id}`, body) : post<DlrRule>("/dlr-rules", body)),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.rules }),
  });
}

export function useDeleteRule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => del(`/dlr-rules/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.rules }),
  });
}

export const previewRule = (rule: DlrRuleInput, messages = 10_000) =>
  post<Preview>("/dlr-rules/preview", { rule, messages });

// ---- catalogs, defaults, workers, audit ------------------------------------------------

export const useCommandStatuses = () =>
  useQuery({ queryKey: keys.commandStatuses, queryFn: () => get<CommandStatus[]>("/catalog/command-statuses"), staleTime: 60_000 });

export const useDlrErrors = () =>
  useQuery({ queryKey: keys.dlrErrors, queryFn: () => get<DlrError[]>("/catalog/dlr-errors"), staleTime: 60_000 });

export function useCatalogMutation<T>(fn: (arg: T) => Promise<unknown>, key: readonly unknown[]) {
  const qc = useQueryClient();
  return useMutation({ mutationFn: fn, onSuccess: () => qc.invalidateQueries({ queryKey: key }) });
}

export const catalogApi = {
  createStatus: (b: { code: number; name: string; description: string }) => post("/catalog/command-statuses", b),
  updateStatus: (b: { code: number; name: string; description: string }) =>
    patch(`/catalog/command-statuses/${b.code}`, { name: b.name, description: b.description }),
  deleteStatus: (code: number) => del(`/catalog/command-statuses/${code}`),
  createError: (b: Omit<DlrError, "system">) => post("/catalog/dlr-errors", b),
  updateError: ({ code, ...b }: Omit<DlrError, "system">) => patch(`/catalog/dlr-errors/${code}`, b),
  deleteError: (code: string) => del(`/catalog/dlr-errors/${code}`),
};

export const useDefaults = () =>
  useQuery({ queryKey: keys.defaults, queryFn: () => get<Defaults>("/defaults") });

export function useSaveDefaults() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (b: Defaults) => put<Defaults>("/defaults", b),
    onSuccess: (d) => {
      qc.setQueryData(keys.defaults, d);
      qc.invalidateQueries({ queryKey: keys.gateways });
    },
  });
}

export const useWorkers = (refetchMs = 5000) =>
  useQuery({ queryKey: keys.workers, queryFn: () => get<Worker[]>("/workers"), refetchInterval: refetchMs || false });

export function useUpdateWorker() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...b }: { id: number; name?: string; status?: string }) => patch<Worker>(`/workers/${id}`, b),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.workers }),
  });
}

export function useDeleteWorker() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => del(`/workers/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.workers }),
  });
}

export interface AuditFilter {
  action?: string;
  entity_type?: string;
  entity_id?: string;
  before_id?: number;
  limit?: number;
}

export const useAudit = (f: AuditFilter) =>
  useQuery({ queryKey: keys.audit(f), queryFn: () => get<AuditEntry[]>(`/audit${qs({ ...f })}`) });

export const useLive = () =>
  useQuery({ queryKey: keys.live, queryFn: () => get<Live>("/live"), refetchInterval: 2000, refetchIntervalInBackground: false });

// ---- reports ---------------------------------------------------------------------

/** A report window; from/to are ISO strings fixed when the range is picked. */
export interface ReportQuery {
  from: string;
  to: string;
  gateway_id?: number;
  include_simulator?: boolean; // traffic from psg-tester; left out unless true
}

/** URL of a report, for fetching or (with format=csv) downloading. */
export const reportUrl = (path: string, params: Record<string, string | number | boolean | undefined>) => `/api/v1${path}${qs(params)}`;

const report = <T,>(path: string, params: Record<string, string | number | boolean | undefined>, enabled = true) =>
  useQuery({
    queryKey: ["reports", path, params],
    queryFn: () => get<T>(`${path}${qs(params)}`),
    enabled,
    staleTime: 30_000,
  });

export const useTrafficReport = (q: ReportQuery) => report<TrafficReport>("/reports/traffic", { ...q });
export const useMixReport = (q: ReportQuery) => report<MixReport>("/reports/outcomes", { ...q }, q.gateway_id !== undefined);
export const useDelayReport = (q: ReportQuery) => report<DelayReport>("/reports/dlr-delay", { ...q });
export const useMoReport = (q: ReportQuery) => report<MoReport>("/reports/mo", { ...q });
export const useBindReport = (q: ReportQuery) => report<BindReport>("/reports/binds", { ...q });

export const useMessageSearch = (params: { message_id?: string; destination?: string } | null) =>
  useQuery({
    queryKey: ["messages", params],
    queryFn: () => get<MessageSearchResult>(`/messages${qs(params ?? {})}`),
    enabled: params !== null,
    staleTime: 10_000,
  });

// ---- MO generator ----------------------------------------------------------------

export const useMO = (gatewayId: number) =>
  useQuery({ queryKey: ["mo", gatewayId], queryFn: () => get<MOOut>(`/gateways/${gatewayId}/mo`), refetchInterval: 2000 });

export function useMOAction(gatewayId: number) {
  const qc = useQueryClient();
  const done = (mo: MOOut) => qc.setQueryData(["mo", gatewayId], mo);
  return {
    save: useMutation({ mutationFn: (b: MOSettings) => put<MOOut>(`/gateways/${gatewayId}/mo`, b), onSuccess: done }),
    start: useMutation({ mutationFn: () => post<MOOut>(`/gateways/${gatewayId}/mo/start`), onSuccess: done }),
    stop: useMutation({ mutationFn: () => post<MOOut>(`/gateways/${gatewayId}/mo/stop`), onSuccess: done }),
    test: useMutation({ mutationFn: (b: MOTest) => post<{ message_id: string }>(`/gateways/${gatewayId}/mo/test`, b) }),
  };
}

// ---- licence -----------------------------------------------------------------------

export const useLicence = () =>
  useQuery({ queryKey: ["licence"], queryFn: () => get<LicenceSummary>("/licence"), refetchInterval: 15_000 });

export function useLicenceAction() {
  const qc = useQueryClient();
  const done = (s: LicenceSummary) => {
    qc.setQueryData(["licence"], s);
    qc.invalidateQueries({ queryKey: keys.gateways });
  };
  return {
    upload: useMutation({ mutationFn: (text: string) => put<LicenceSummary>("/licence", { licence: text }), onSuccess: done }),
    remove: useMutation({ mutationFn: () => api<LicenceSummary>("DELETE", "/licence"), onSuccess: done }),
    keep: useMutation({ mutationFn: (ids: number[]) => put<LicenceSummary>("/licence/keep", { gateway_ids: ids }), onSuccess: done }),
  };
}

// ---- SMPP client simulator -----------------------------------------------------------

export const useTesters = () => useQuery({ queryKey: ["testers"], queryFn: () => get<Tester[]>("/testers"), refetchInterval: 5000 });

export const useTests = () => useQuery({ queryKey: ["tests"], queryFn: () => get<TestRunOut[]>("/tests"), refetchInterval: 5000 });

export const useTest = (id: number | null) =>
  useQuery({
    queryKey: ["tests", id],
    queryFn: () => get<TestRunOut>(`/tests/${id}`),
    enabled: id !== null,
    refetchInterval: (q) => (q.state.data && ["queued", "running"].includes(q.state.data.status) ? 1000 : false),
  });

export const useTestErrors = (id: number, enabled: boolean) =>
  useQuery({
    queryKey: ["tests", id, "errors"],
    queryFn: () => get<{ errors: TestError[]; expires_in_s: number | null }>(`/tests/${id}/errors`),
    enabled,
  });

export function useTestActions() {
  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: ["tests"] });
  return {
    start: useMutation({ mutationFn: (b: TestSpec) => post<TestRunOut>("/tests", b), onSuccess: refresh }),
    stop: useMutation({ mutationFn: (id: number) => post<TestRunOut>(`/tests/${id}/stop`), onSuccess: refresh }),
    remove: useMutation({ mutationFn: (id: number) => del(`/tests/${id}`), onSuccess: refresh }),
  };
}

// ---- footer resource usage -------------------------------------------------------

/** Polled every 5 s, and not at all while the tab is hidden (React Query
 * pauses intervals in the background). */
export const useSystemUsage = () =>
  useQuery({ queryKey: ["system-usage"], queryFn: () => get<SystemUsage>("/system/usage"), refetchInterval: 5000, retry: false });
