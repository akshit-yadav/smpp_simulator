// Live rates. Workers publish cumulative counters every second; the panel polls
// /api/v1/live every 2 s and turns consecutive snapshots into per-second rates,
// keeping ~5 minutes of history shared by every page.

import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { useLive } from "../api/queries";
import type { GatewayCounters, Live } from "../api/types";

export interface Rates {
  accepted: number;
  rejected: number;
  throttled: number;
  dlr: number;
  sessions: number;
}

export interface Sample {
  ts: number;
  total: Rates;
  gateways: Record<string, Rates>;
}

const HISTORY = 150;
const zero = (): Rates => ({ accepted: 0, rejected: 0, throttled: 0, dlr: 0, sessions: 0 });

function sessionsOf(c: GatewayCounters) {
  return (c.sessions?.tx ?? 0) + (c.sessions?.rx ?? 0) + (c.sessions?.trx ?? 0);
}

function rates(prev: GatewayCounters | undefined, cur: GatewayCounters): Rates {
  const r = zero();
  r.sessions = sessionsOf(cur);
  if (!prev) return r;
  const dt = (cur.ts - prev.ts) / 1000;
  if (dt <= 0) return r;
  const d = (k: keyof GatewayCounters) => Math.max(0, ((cur[k] as number) - (prev[k] as number)) / dt);
  r.accepted = d("accepted");
  r.rejected = d("rejected") + d("invalid");
  r.throttled = d("throttled") + (cur.licence_throttled === undefined ? 0 : d("licence_throttled"));
  r.dlr = d("dlr_sent");
  return r;
}

interface LiveState {
  live: Live | undefined;
  history: Sample[];
  latest: Sample | undefined;
  error: unknown;
}

const Ctx = createContext<LiveState>({ live: undefined, history: [], latest: undefined, error: null });

export function LiveProvider({ children }: { children: ReactNode }) {
  const q = useLive();
  const prev = useRef<Live | undefined>(undefined);
  const [history, setHistory] = useState<Sample[]>([]);

  useEffect(() => {
    const cur = q.data;
    if (!cur || cur === prev.current) return;
    const before = prev.current;
    prev.current = cur;
    if (!before) return; // need two snapshots for a rate
    const sample: Sample = { ts: cur.ts, total: zero(), gateways: {} };
    for (const [id, c] of Object.entries(cur.gateways)) {
      const r = rates(before.gateways[id], c);
      sample.gateways[id] = r;
      for (const k of Object.keys(r) as (keyof Rates)[]) sample.total[k] += r[k];
    }
    setHistory((h) => [...h.slice(-(HISTORY - 1)), sample]);
  }, [q.data]);

  return (
    <Ctx.Provider value={{ live: q.data, history, latest: history[history.length - 1], error: q.error }}>
      {children}
    </Ctx.Provider>
  );
}

export const useLiveRates = () => useContext(Ctx);
