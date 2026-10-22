// Small dependency-free SVG charts: a multi-series line chart with a crosshair
// tooltip, a bar chart with per-bar tooltips, and a sparkline. Lines are 2 px, the grid is recessive, colours come
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
  formatTime = clock,
  legendValue,
  emptyText = "Collecting data…",
}: {
  series: Series[];
  times: number[];
  height?: number;
  unit?: string;
  ariaLabel: string;
  formatTime?: (ts: number) => string;
  /** What the legend shows next to each name; default: the latest value. */
  legendValue?: (s: Series) => string;
  emptyText?: string;
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
              <b className="mono">{legendValue ? legendValue(s) : `${fmtRate(s.values[s.values.length - 1] ?? 0)}${unit}`}</b>
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
                {formatTime(times[0])}
              </text>
              <text className="axis" x={W - pad.r} y={height - 4} textAnchor="end">
                {formatTime(times[n - 1])}
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
            <div className="muted">{formatTime(times[hover])}</div>
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
            {emptyText}
          </div>
        )}
      </div>
    </div>
  );
}

export interface Bar {
  label: string; // under the bar
  title: string; // tooltip heading
  value: number;
}

/** Vertical bars for one series (no legend: the card title names it). Bars
 * have 4 px rounded tops on the baseline and 2 px gaps; each whole column is
 * the hover target. */
export function BarChart({
  bars,
  height = 200,
  ariaLabel,
  color = "var(--series-1)",
  formatValue = (v: number) => fmtRate(v),
}: {
  bars: Bar[];
  height?: number;
  ariaLabel: string;
  color?: string;
  formatValue?: (v: number) => string;
}) {
  const [ref, W] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const pad = { l: 48, r: 8, t: 8, b: 22 };
  const max = niceMax(Math.max(0, ...bars.map((b) => b.value)));
  const total = bars.reduce((a, b) => a + b.value, 0);
  const slot = (W - pad.l - pad.r) / Math.max(1, bars.length);
  const bw = Math.max(2, slot - 2); // 2 px surface gap between bars
  const base = height - pad.b;
  const y = (v: number) => pad.t + (1 - v / max) * (base - pad.t);
  const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => f * max);
  const every = Math.ceil(44 / slot); // label every n-th bar so labels never collide

  const barPath = (x: number, top: number) => {
    const r = Math.min(4, bw / 2, base - top);
    return `M${x},${base} V${top + r} Q${x},${top} ${x + r},${top} H${x + bw - r} Q${x + bw},${top} ${x + bw},${top + r} V${base} Z`;
  };

  return (
    <div className="chart">
      <div ref={ref} onPointerLeave={() => setHover(null)} style={{ position: "relative" }}>
        <svg width={W} height={height} role="img" aria-label={ariaLabel}>
          {ticks.map((t) => (
            <g key={t}>
              <line className="gridline" x1={pad.l} x2={W - pad.r} y1={y(t)} y2={y(t)} />
              <text className="axis" x={pad.l - 8} y={y(t) + 4} textAnchor="end">
                {axisLabel(t)}
              </text>
            </g>
          ))}
          {bars.map((b, i) => {
            const x = pad.l + i * slot + 1;
            return (
              <g key={b.title} onPointerEnter={() => setHover(i)}>
                <rect x={pad.l + i * slot} y={pad.t} width={slot} height={base - pad.t} fill="transparent" />
                {b.value > 0 && <path d={barPath(x, y(b.value))} fill={color} opacity={hover === null || hover === i ? 1 : 0.55} />}
                {i % every === 0 && (
                  <text className="axis" x={x + bw / 2} y={height - 4} textAnchor="middle">
                    {b.label}
                  </text>
                )}
              </g>
            );
          })}
        </svg>
        {hover !== null && (
          <div
            className="tooltip"
            style={{
              left: pad.l + hover * slot + slot / 2,
              top: 8,
              transform: hover > bars.length * 0.6 ? "translateX(calc(-100% - 12px))" : "translateX(12px)",
            }}
          >
            <div className="muted">{bars[hover].title}</div>
            <div className="tooltip-row">
              <span>{formatValue(bars[hover].value)}</span>
              <b>{total ? `${((100 * bars[hover].value) / total).toFixed(1)}%` : "—"}</b>
            </div>
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
