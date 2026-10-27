const nf = new Intl.NumberFormat();

export const fmtNum = (n: number | null | undefined) => (n == null ? "—" : nf.format(n));

/** Rates: whole numbers from 10 up, one decimal below. */
export function fmtRate(n: number | null | undefined): string {
  if (n == null) return "—";
  if (n >= 10) return nf.format(Math.round(n));
  return n.toFixed(1).replace(/\.0$/, "");
}

export function fmtMs(ms: number): string {
  if (ms >= 60_000 && ms % 60_000 === 0) return `${ms / 60_000} min`;
  if (ms >= 1000 && ms % 1000 === 0) return `${ms / 1000} s`;
  return `${nf.format(ms)} ms`;
}

export const hex = (code: number) => `0x${code.toString(16).toUpperCase().padStart(code > 0xff ? 3 : 2, "0")}`;

export function fmtTime(iso: string | number | null | undefined): string {
  if (iso == null) return "—";
  const d = typeof iso === "number" ? new Date(iso) : new Date(iso.endsWith("Z") || iso.includes("+") ? iso : `${iso}Z`);
  return d.toLocaleString();
}

export function fmtAgo(iso: string | null | undefined): string {
  if (!iso) return "never";
  const d = new Date(iso.endsWith("Z") || iso.includes("+") ? iso : `${iso}Z`);
  const s = Math.max(0, Math.round((Date.now() - d.getTime()) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} d ago`;
}

export function fmtDuration(fromIso: string): string {
  const s = Math.max(0, Math.round((Date.now() - new Date(fromIso).getTime()) / 1000));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
}

/** Random printable password of the given length (max 8 for SMPP). */
export function randomPassword(len = 8): string {
  const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789";
  const buf = new Uint32Array(len);
  crypto.getRandomValues(buf);
  return Array.from(buf, (n) => chars[n % chars.length]).join("");
}
