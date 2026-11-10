import { useEffect, useMemo, useState, type ReactNode } from 'react';
import {
  Area,
  AreaChart,
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
  type TooltipProps,
} from 'recharts';
import type { MetricPoint } from '../api/types';
import { fmtClock, fmtCompact, fmtMs, fmtRps, fmtSeconds } from '../lib/format';
import { trimPartialTail } from '../lib/metrics';
import type { ProfilePoint } from '../lib/scenario';

// Recharts writes colours into SVG presentation attributes, where CSS
// variables do not resolve, so read the resolved tokens from :root.
const TOKENS = ['--series-1', '--series-2', '--series-3', '--series-red', '--series-muted', '--grid', '--text-3', '--border-strong', '--surface'] as const;
type Token = (typeof TOKENS)[number];
export type Palette = Record<Token, string>;

function readPalette(): Palette {
  const cs = getComputedStyle(document.documentElement);
  const out = {} as Palette;
  for (const t of TOKENS) out[t] = cs.getPropertyValue(t).trim() || '#888';
  return out;
}

export function usePalette(): Palette {
  const [p, setP] = useState<Palette>(readPalette);
  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const on = () => setP(readPalette());
    mq.addEventListener('change', on);
    return () => mq.removeEventListener('change', on);
  }, []);
  return p;
}

export interface Series {
  key: string;
  label: string;
  color: string;
  dashed?: boolean;
}

type Row = Record<string, number | null | undefined>;

function ChartTooltip({
  active,
  payload,
  label,
  series,
  fmt,
  xLabel,
}: TooltipProps<number, string> & { series: Series[]; fmt: (v: number) => string; xLabel: (x: number) => string }) {
  if (!active || !payload || payload.length === 0) return null;
  const row = payload[0]?.payload as Row | undefined;
  if (!row) return null;
  return (
    <div className="chart-tooltip">
      <div className="tt-head">{xLabel(Number(label))}</div>
      {series.map((s) => {
        const v = row[s.key];
        return (
          <div className="tt-row" key={s.key}>
            <i style={{ background: s.color }} />
            <span className="text-2">{s.label}</span>
            <span className="tt-v">{typeof v === 'number' && Number.isFinite(v) ? fmt(v) : '—'}</span>
          </div>
        );
      })}
    </div>
  );
}

export function Legend({ series }: { series: Series[] }) {
  if (series.length < 2) return null;
  return (
    <div className="chart-legend">
      {series.map((s) => (
        <span key={s.key}>
          <i className={s.dashed ? 'dashed' : ''} style={{ background: s.color, color: s.color }} />
          {s.label}
        </span>
      ))}
    </div>
  );
}

export function ChartCard({
  title,
  series,
  children,
  extra,
}: {
  title: string;
  series: Series[];
  children: ReactNode;
  extra?: ReactNode;
}) {
  return (
    <div className="card chart-card">
      <div className="card-head">
        <h2>{title}</h2>
        <span className="spacer" />
        {extra}
        <Legend series={series} />
      </div>
      <div className="card-body">{children}</div>
    </div>
  );
}

export function TimeSeriesChart({
  data,
  series,
  fmt,
  height = 220,
  xMax,
  yFmt = fmtCompact,
  integerY = false,
  emptyText = 'Waiting for data…',
}: {
  data: Row[];
  series: Series[];
  fmt: (v: number) => string;
  /** Y axis tick formatter. */
  yFmt?: (v: number) => string;
  /** Only integer y ticks (counts). */
  integerY?: boolean;
  height?: number;
  /** Fix the x domain end (e.g. planned duration during a live run). */
  xMax?: number;
  emptyText?: string;
}) {
  const pal = usePalette();
  if (data.length === 0) {
    return (
      <div style={{ height }}>
        <div className="chart-empty">{emptyText}</div>
      </div>
    );
  }
  const domainMax = xMax && xMax > 0 ? Math.max(xMax, Number(data[data.length - 1]?.elapsed ?? 0)) : 'dataMax';
  return (
    <div style={{ height }}>
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={data} margin={{ top: 6, right: 12, bottom: 0, left: 4 }}>
          <CartesianGrid stroke={pal['--grid']} vertical={false} />
          <XAxis
            dataKey="elapsed"
            type="number"
            domain={[0, domainMax]}
            tickFormatter={(v: number) => fmtClock(v)}
            stroke={pal['--border-strong']}
            tick={{ fill: pal['--text-3'], fontSize: 11 }}
            tickLine={false}
            minTickGap={40}
            allowDecimals={false}
          />
          <YAxis
            width={52}
            tickFormatter={(v: number) => yFmt(v)}
            tick={{ fill: pal['--text-3'], fontSize: 11 }}
            axisLine={false}
            tickLine={false}
            allowDecimals={!integerY}
          />
          <Tooltip
            isAnimationActive={false}
            cursor={{ stroke: pal['--text-3'], strokeWidth: 1, strokeDasharray: '3 3' }}
            content={<ChartTooltip series={series} fmt={fmt} xLabel={(x) => `t = ${fmtClock(x)}`} />}
          />
          {series.map((s) => (
            <Line
              key={s.key}
              dataKey={s.key}
              name={s.label}
              stroke={s.color}
              strokeWidth={s.dashed ? 1.5 : 2}
              strokeDasharray={s.dashed ? '4 3' : undefined}
              dot={false}
              activeDot={{ r: 4, strokeWidth: 2, stroke: pal['--surface'] }}
              isAnimationActive={false}
              connectNulls
              type="linear"
            />
          ))}
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}

// ---------- axis formatters ----------
export const yMs = (v: number) => (v >= 1000 ? `${+(v / 1000).toFixed(2)}s` : `${+v.toFixed(1)}`);
export const yPct = (v: number) => `${+(v * 100).toFixed(2)}%`;

// ---------- run charts (live + result) ----------

export function useRunSeries() {
  const pal = usePalette();
  return useMemo(
    () => ({
      rps: [
        { key: 'rps', label: 'RPS', color: pal['--series-1'] },
        { key: 'target', label: 'Target', color: pal['--series-muted'], dashed: true },
      ] as Series[],
      latency: [
        { key: 'p50_ms', label: 'p50', color: pal['--series-1'] },
        { key: 'p95_ms', label: 'p95', color: pal['--series-2'] },
        { key: 'p99_ms', label: 'p99', color: pal['--series-3'] },
      ] as Series[],
      errors: [{ key: 'errors', label: 'Errors/s', color: pal['--series-red'] }] as Series[],
    }),
    [pal],
  );
}

export function RunCharts({
  points,
  targetUnit,
  xMax,
  finished,
}: {
  points: MetricPoint[];
  targetUnit: string;
  xMax?: number;
  /** The run is over: hide a trailing partial-second point (display only). */
  finished?: boolean;
}) {
  const s = useRunSeries();
  const data = useMemo(() => (finished ? trimPartialTail(points) : points) as unknown as Row[], [points, finished]);
  const rpsSeries = s.rps.map((x) => (x.key === 'target' ? { ...x, label: `Target (${targetUnit})` } : x));
  return (
    <div className="stack" style={{ gap: 12 }}>
      <ChartCard title="Throughput — requests/s · target in iterations/s" series={rpsSeries}>
        <TimeSeriesChart data={data} series={rpsSeries} fmt={fmtRps} xMax={xMax} height={230} />
      </ChartCard>
      <div className="grid-2" style={{ gap: 12 }}>
        <ChartCard title="Latency percentiles (ms)" series={s.latency}>
          <TimeSeriesChart data={data} series={s.latency} fmt={fmtMs} yFmt={yMs} xMax={xMax} />
        </ChartCard>
        <ChartCard title="Errors per second" series={s.errors}>
          <TimeSeriesChart data={data} series={s.errors} fmt={(v) => fmtRps(v)} xMax={xMax} integerY />
        </ChartCard>
      </div>
    </div>
  );
}

// ---------- load profile preview ----------

export function ProfileChart({
  points,
  unit,
  height = 140,
}: {
  points: ProfilePoint[];
  unit: string;
  height?: number;
}) {
  const pal = usePalette();
  if (points.length < 2) {
    return (
      <div style={{ height }}>
        <div className="chart-empty">Add stages to see the profile</div>
      </div>
    );
  }
  return (
    <div style={{ height }}>
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={points} margin={{ top: 8, right: 10, bottom: 0, left: 0 }}>
          <defs>
            <linearGradient id="profileFill" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={pal['--series-1']} stopOpacity={0.28} />
              <stop offset="100%" stopColor={pal['--series-1']} stopOpacity={0.02} />
            </linearGradient>
          </defs>
          <CartesianGrid stroke={pal['--grid']} vertical={false} />
          <XAxis
            dataKey="t"
            type="number"
            domain={[0, 'dataMax']}
            tickFormatter={(v: number) => fmtSeconds(v)}
            tick={{ fill: pal['--text-3'], fontSize: 10 }}
            stroke={pal['--border-strong']}
            tickLine={false}
            minTickGap={24}
          />
          <YAxis
            width={36}
            tick={{ fill: pal['--text-3'], fontSize: 10 }}
            axisLine={false}
            tickLine={false}
            tickFormatter={(v: number) => fmtCompact(v)}
            allowDecimals={false}
          />
          <Tooltip
            isAnimationActive={false}
            cursor={{ stroke: pal['--text-3'], strokeDasharray: '3 3' }}
            content={
              <ChartTooltip
                series={[{ key: 'target', label: `Target (${unit})`, color: pal['--series-1'] }]}
                fmt={(v) => String(v)}
                xLabel={(x) => `t = ${fmtSeconds(x)}`}
              />
            }
          />
          <Area
            type="linear"
            dataKey="target"
            stroke={pal['--series-1']}
            strokeWidth={2}
            fill="url(#profileFill)"
            isAnimationActive={false}
            dot={{ r: 2.5, fill: pal['--series-1'], strokeWidth: 0 }}
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}
