// React Query hooks, one per resource. Mutations invalidate what they change.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { del, get, patch, post, put, qs } from "./client";
import type {
  AuditEntry,
  CommandStatus,
  Defaults,
  DlrError,
  Gateway,
  Live,
  Me,
  PortCheck,
  Preview,
  Profile,
  ProfileInput,
  Session,
  Worker,
} from "./types";

export const keys = {
  me: ["me"] as const,
  gateways: ["gateways"] as const,
  gateway: (id: number) => ["gateways", id] as const,
  sessions: (id: number) => ["sessions", id] as const,
  profiles: ["profiles"] as const,
  profile: (id: number) => ["profiles", id] as const,
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
      qc.invalidateQueries({ queryKey: ["gateway-profile", gw.id] });
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

export const useGatewayProfile = (id: number) =>
  useQuery({ queryKey: ["gateway-profile", id], queryFn: () => get<Profile>(`/gateways/${id}/profile`), enabled: id > 0 });

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

// ---- profiles ------------------------------------------------------------------------

export const useProfiles = () =>
  useQuery({ queryKey: keys.profiles, queryFn: () => get<Profile[]>("/profiles") });

export const useProfile = (id: number) =>
  useQuery({ queryKey: keys.profile(id), queryFn: () => get<Profile>(`/profiles/${id}`), enabled: id > 0 });

export function useSaveProfile(id?: number) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: ProfileInput) => (id ? put<Profile>(`/profiles/${id}`, body) : post<Profile>("/profiles", body)),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.profiles }),
  });
}

export function useDeleteProfile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => del(`/profiles/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: keys.profiles }),
  });
}

export const previewProfile = (profile: ProfileInput, messages = 10_000) =>
  post<Preview>("/profiles/preview", { profile, messages });

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
