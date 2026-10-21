// Every panel action goes through the control-plane REST API.

export type Readiness = {
  status: "ok" | "degraded";
  checks: Record<string, string>;
};

export type Worker = {
  worker_id: number;
  ip: string;
  version: string;
  port_range: string;
  started_at: string;
  ts: string;
};

async function getJSON<T>(path: string): Promise<T> {
  const resp = await fetch(path);
  // /readyz returns 503 with a useful body when a store is down.
  if (!resp.ok && resp.status !== 503) throw new Error(`${path}: HTTP ${resp.status}`);
  return resp.json() as Promise<T>;
}

export const getReadiness = () => getJSON<Readiness>("/readyz");
export const getWorkers = async () => {
  const resp = await fetch("/api/v1/workers");
  if (!resp.ok) throw new Error(`workers: HTTP ${resp.status}`);
  return (await resp.json()) as Worker[];
};
