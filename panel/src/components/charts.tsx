// Small dependency-free SVG charts: a multi-series line chart with a crosshair
// tooltip, and a sparkline. Lines are 2 px, the grid is recessive, colours come
// from the validated --series-N tokens in fixed order, and every chart with two
// or more series has a legend (identity is never colour alone).

import { useEffect, useMemo, useRef, useState } from "react";
import { fmtRate } from "../lib/format";

export interface Series {
  name: string;
  color: string; // CSS colour, e.g. "var(--series-1)"
  values: number[];
}

/** A "nice" axis maximum whose quarters are round numbers. */
function niceMax(v: number): number {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  for (const m of [1, 2, 4, 8, 10]) if (v <= m * p) return m * p;
  return 10 * p;
}

function axisLabel(v: number): string {
  if (v >= 1000) return `${+(v / 1000).toFixed(1)}k`;
  return String(+v.toFixed(2));
}

function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(320); // measured on mount
  useEffect(() => {
    if (!ref.current) return;
    const ro = new ResizeObserver(([e]) => setWidth(Math.max(200, Math.floor(e.contentRect.width))));
    ro.observe(ref.current);
    return () => ro.disconnect();
  }, []);
  return [ref, width] as const;
}

const clock = (ts: number) => new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });

export function LineChart({
  series,
  times,
  height = 200,
  unit = "/s",
  ariaLabel,
}: {
  series: Series[];
  times: number[];
  height?: number;
  unit?: string;
  ariaLabel: string;
}) {
  const [ref, W] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const n = times.length;
  const pad = { l: 48, r: 8, t: 8, b: 22 };
  const max = useMemo(() => niceMax(Math.max(0, ...series.flatMap((s) => s.values))), [series]);
  const x = (i: number) => pad.l + (n <= 1 ? 0 : (i / (n - 1)) * (W - pad.l - pad.r));
  const y = (v: number) => pad.t + (1 - v / max) * (height - pad.t - pad.b);
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => f * max);

  const onMove = (e: React.PointerEvent) => {
    if (!ref.current || n < 2) return;
    const px = e.clientX - ref.current.getBoundingClientRect().left;
    const i = Math.round(((px - pad.l) / (W - pad.l - pad.r)) * (n - 1));
    setHover(Math.min(n - 1, Math.max(0, i)));
  };

  return (
    <div className="chart">
      {series.length > 1 && (
        <div className="legend">
          {series.map((s) => (
            <span key={s.name} className="legend-item">
              <span className="legend-swatch" style={{ background: s.color }} />
              {s.name}
              <b className="mono">
                {fmtRate(s.values[s.values.length - 1] ?? 0)}
                {unit}
              </b>
            </span>
          ))}
        </div>
      )}
      <div ref={ref} onPointerMove={onMove} onPointerLeave={() => setHover(null)} style={{ position: "relative" }}>
        <svg width={W} height={height} role="img" aria-label={ariaLabel}>
          {ticks.map((t) => (
            <g key={t}>
              <line className="gridline" x1={pad.l} x2={W - pad.r} y1={y(t)} y2={y(t)} />
              <text className="axis" x={pad.l - 8} y={y(t) + 4} textAnchor="end">
                {axisLabel(t)}
              </text>
            </g>
          ))}
          {n > 1 && (
            <>
              <text className="axis" x={pad.l} y={height - 4}>
                {clock(times[0])}
              </text>
              <text className="axis" x={W - pad.r} y={height - 4} textAnchor="end">
                {clock(times[n - 1])}
              </text>
            </>
          )}
          {n > 1 &&
            series.map((s) => (
              <polyline
                key={s.name}
                fill="none"
                stroke={s.color}
                strokeWidth={2}
                strokeLinejoin="round"
                strokeLinecap="round"
                points={s.values.map((v, i) => `${x(i)},${y(v)}`).join(" ")}
              />
            ))}
          {hover !== null && (
            <>
              <line x1={x(hover)} x2={x(hover)} y1={pad.t} y2={height - pad.b} stroke="var(--border-strong)" />
              {series.map((s) => (
                <circle key={s.name} cx={x(hover)} cy={y(s.values[hover] ?? 0)} r={4} fill={s.color} stroke="var(--surface)" strokeWidth={2} />
              ))}
            </>
          )}
        </svg>
        {hover !== null && (
          <div
            className="tooltip"
            style={{ left: x(hover), top: 8, transform: x(hover) > W * 0.6 ? "translateX(calc(-100% - 12px))" : "translateX(12px)" }}
          >
            <div className="muted">{clock(times[hover])}</div>
            {series.map((s) => (
              <div key={s.name} className="tooltip-row">
                <span className="legend-item">
                  <span className="legend-swatch" style={{ background: s.color }} />
                  {s.name}
                </span>
                <b>
                  {fmtRate(s.values[hover] ?? 0)}
                  {unit}
                </b>
              </div>
            ))}
          </div>
        )}
        {n < 2 && (
          <div className="muted small" style={{ position: "absolute", left: pad.l + 8, top: height / 2 - 20 }}>
            Collecting data…
          </div>
        )}
      </div>
    </div>
  );
}

export function Sparkline({ values, label, width = 96, height = 24 }: { values: number[]; label: string; width?: number; height?: number }) {
  const max = Math.max(1, ...values);
  const n = values.length;
  if (n < 2) return <span className="muted small">—</span>;
  const pts = values.map((v, i) => `${(i / (n - 1)) * width},${height - 2 - (v / max) * (height - 4)}`).join(" ");
  return (
    <svg width={width} height={height} role="img" aria-label={label} style={{ display: "block" }}>
      <title>{label}</title>
      <polyline fill="none" stroke="var(--series-1)" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" points={pts} />
    </svg>
  );
}
