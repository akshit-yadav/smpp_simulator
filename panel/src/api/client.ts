// Thin fetch wrapper. Every panel action goes through the REST API; the
// session lives in an HttpOnly cookie, so requests are same-origin only.

export class ApiError extends Error {
  status: number;
  detail: unknown;

  constructor(status: number, message: string, detail: unknown) {
    super(message);
    this.status = status;
    this.detail = detail;
  }

  /** The suggested free port on a port conflict, if the API offered one. */
  get suggestedPort(): number | null {
    const d = this.detail as { suggested_port?: number } | undefined;
    return d && typeof d === "object" && typeof d.suggested_port === "number" ? d.suggested_port : null;
  }
}

function messageOf(detail: unknown, status: number): string {
  if (typeof detail === "string") return detail;
  if (Array.isArray(detail)) {
    // FastAPI validation errors: [{loc: [...], msg: "..."}]
    return detail
      .map((e: { loc?: (string | number)[]; msg?: string }) => {
        const field = (e.loc ?? []).filter((p) => p !== "body").join(".");
        return field ? `${field}: ${e.msg}` : (e.msg ?? "Invalid value");
      })
      .join("; ");
  }
  if (detail && typeof detail === "object" && "message" in detail) {
    return String((detail as { message: unknown }).message);
  }
  return `Request failed (HTTP ${status})`;
}

let onUnauthorized: () => void = () => {};
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

export async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const resp = await fetch(`/api/v1${path}`, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (resp.status === 204) return undefined as T;
  const data = await resp.json().catch(() => null);
  if (!resp.ok) {
    const detail = data?.detail;
    if (resp.status === 401 && !path.startsWith("/auth/login")) onUnauthorized();
    const err = new ApiError(resp.status, messageOf(detail, resp.status), detail);
    if (data && typeof data === "object" && "totp_required" in data) {
      (err as ApiError & { totpRequired?: boolean }).totpRequired = Boolean(data.totp_required);
    }
    throw err;
  }
  return data as T;
}

export const get = <T>(path: string) => api<T>("GET", path);
export const post = <T>(path: string, body?: unknown) => api<T>("POST", path, body ?? {});
export const put = <T>(path: string, body: unknown) => api<T>("PUT", path, body);
export const patch = <T>(path: string, body: unknown) => api<T>("PATCH", path, body);
export const del = (path: string) => api<void>("DELETE", path);

export function qs(params: Record<string, string | number | boolean | null | undefined>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== null && v !== undefined && v !== "") p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}
