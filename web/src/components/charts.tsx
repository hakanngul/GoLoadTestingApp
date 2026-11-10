import { useEffect, useMemo, useState, type ReactNode } from 'react';
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Line,
  LineChart,
  ReferenceArea,
  ReferenceLine,
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
const TOKENS = [
  '--series-1',
  '--series-2',
  '--series-3',
  '--series-4',
  '--series-5',
  '--series-6',
  '--series-red',
  '--series-green',
  '--series-muted',
  '--grid',
  '--text-3',
  '--border-strong',
  '--surface',
  '--accent',
  '--accent-soft',
] as const;
type Token = (typeof TOKENS)[number];
export type Palette = Record<Token, string>;

function readPalette(): Palette {
  const cs = getComputedStyle(document.documentElement);
  const out = {} as Palette;
  for (const t of TOKENS) out[t] = cs.getPropertyValue(t).trim() || 'currentColor';
  return out;
}

/** One colour per compared run (up to 6), in order. */
export function runColors(p: Palette): string[] {
  return [p['--series-1'], p['--series-2'], p['--series-3'], p['--series-4'], p['--series-5'], p['--series-6']];
}

export function usePalette(): Palette {
  const [p, setP] = useState<Palette>(readPalette);
  useEffect(() => {
    // The theme lives in <html data-theme> (src/lib/theme.ts); re-read the
    // tokens whenever it changes (user toggle or OS change under "system").
    const obs = new MutationObserver(() => setP(readPalette()));
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    return () => obs.disconnect();
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
  headText,
}: TooltipProps<number, string> & {
  series: Series[];
  fmt: (v: number) => string;
  xLabel?: (x: number) => string;
  /** Head text from the raw (string) category label instead of xLabel. */
  headText?: (label: string) => string;
}) {
  if (!active || !payload || payload.length === 0) return null;
  const row = payload[0]?.payload as Row | undefined;
  if (!row) return null;
  return (
    <div className="chart-tooltip">
      <div className="tt-head">{headText ? headText(String(label)) : xLabel ? xLabel(Number(label)) : String(label)}</div>
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
  highlight,
  onSelectRange,
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
  /** Shade this x range (seconds), e.g. the report window. */
  highlight?: { from: number; to: number; label?: string } | null;
  /** Enables drag-to-select on the chart; called with the dragged x range. */
  onSelectRange?: (a: number, b: number) => void;
}) {
  const pal = usePalette();
  const [drag, setDrag] = useState<{ a: number; b: number } | null>(null);
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
        <LineChart
          data={data}
          margin={{ top: 6, right: 12, bottom: 0, left: 4 }}
          style={onSelectRange ? { cursor: 'crosshair', userSelect: 'none' } : undefined}
          onMouseDown={
            onSelectRange
              ? (e) => {
                  const x = Number(e?.activeLabel);
                  if (Number.isFinite(x)) setDrag({ a: x, b: x });
                }
              : undefined
          }
          onMouseMove={
            onSelectRange
              ? (e) => {
                  const x = Number(e?.activeLabel);
                  if (drag && Number.isFinite(x)) setDrag({ a: drag.a, b: x });
                }
              : undefined
          }
          onMouseUp={
            onSelectRange
              ? () => {
                  if (drag && drag.a !== drag.b) onSelectRange(drag.a, drag.b);
                  setDrag(null);
                }
              : undefined
          }
          onMouseLeave={onSelectRange ? () => setDrag(null) : undefined}
        >
          <CartesianGrid stroke={pal['--grid']} vertical={false} />
          {highlight && !drag && (
            <ReferenceArea
              x1={highlight.from}
              x2={highlight.to}
              fill={pal['--accent']}
              fillOpacity={0.08}
              stroke={pal['--accent']}
              strokeOpacity={0.35}
              strokeDasharray="3 3"
              ifOverflow="extendDomain"
              label={
                highlight.label
                  ? { value: highlight.label, position: 'insideBottom', fill: pal['--text-3'], fontSize: 10.5 }
                  : undefined
              }
            />
          )}
          {drag && (
            <ReferenceArea
              x1={Math.min(drag.a, drag.b)}
              x2={Math.max(drag.a, drag.b)}
              fill={pal['--accent']}
              fillOpacity={0.18}
              stroke={pal['--accent']}
              strokeOpacity={0.6}
            />
          )}
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
  highlight,
  onSelectRange,
  throughputExtra,
}: {
  points: MetricPoint[];
  targetUnit: string;
  xMax?: number;
  /** The run is over: hide a trailing partial-second point (display only). */
  finished?: boolean;
  /** Report window shaded on every chart. */
  highlight?: { from: number; to: number; label?: string } | null;
  /** Drag-select on the throughput chart. */
  onSelectRange?: (a: number, b: number) => void;
  throughputExtra?: ReactNode;
}) {
  const s = useRunSeries();
  const data = useMemo(() => (finished ? trimPartialTail(points) : points) as unknown as Row[], [points, finished]);
  const rpsSeries = s.rps.map((x) => (x.key === 'target' ? { ...x, label: `Target (${targetUnit})` } : x));
  return (
    <div className="stack" style={{ gap: 12 }}>
      <ChartCard title="Throughput — requests/s · target in iterations/s" series={rpsSeries} extra={throughputExtra}>
        <TimeSeriesChart
          data={data}
          series={rpsSeries}
          fmt={fmtRps}
          xMax={xMax}
          height={230}
          highlight={highlight}
          onSelectRange={onSelectRange}
        />
      </ChartCard>
      <div className="grid-2" style={{ gap: 12 }}>
        <ChartCard title="Latency percentiles (ms)" series={s.latency}>
          <TimeSeriesChart data={data} series={s.latency} fmt={fmtMs} yFmt={yMs} xMax={xMax} highlight={highlight && { from: highlight.from, to: highlight.to }} />
        </ChartCard>
        <ChartCard title="Errors per second" series={s.errors}>
          <TimeSeriesChart
            data={data}
            series={s.errors}
            fmt={(v) => fmtRps(v)}
            xMax={xMax}
            integerY
            highlight={highlight && { from: highlight.from, to: highlight.to }}
          />
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

// ---------- trend across runs ----------

export interface TrendRow {
  /** Category key (run index). */
  idx: number;
  /** X tick text (label or date). */
  name: string;
  /** Tooltip head. */
  title: string;
  baseline?: boolean;
  [k: string]: number | string | boolean | undefined | null;
}

/** Lines across runs (x = run); the baseline run's values as dashed horizontal lines. */
export function TrendChart({
  data,
  series,
  fmt,
  yFmt = fmtCompact,
  height = 200,
  baselineIdx,
}: {
  data: TrendRow[];
  series: Series[];
  fmt: (v: number) => string;
  yFmt?: (v: number) => string;
  height?: number;
  baselineIdx?: number;
}) {
  const pal = usePalette();
  if (data.length === 0) {
    return (
      <div style={{ height }}>
        <div className="chart-empty">No finished runs</div>
      </div>
    );
  }
  const base = baselineIdx !== undefined ? data.find((d) => d.idx === baselineIdx) : undefined;
  return (
    <div style={{ height }}>
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={data} margin={{ top: 20, right: 16, bottom: 0, left: 4 }}>
          <CartesianGrid stroke={pal['--grid']} vertical={false} />
          <XAxis
            dataKey="idx"
            type="category"
            tickFormatter={(v: number) => data.find((d) => d.idx === v)?.name ?? ''}
            stroke={pal['--border-strong']}
            tick={{ fill: pal['--text-3'], fontSize: 10.5 }}
            tickLine={false}
            interval="preserveStartEnd"
            minTickGap={8}
          />
          <YAxis
            width={52}
            tickFormatter={(v: number) => yFmt(v)}
            tick={{ fill: pal['--text-3'], fontSize: 11 }}
            axisLine={false}
            tickLine={false}
          />
          {base && (
            <ReferenceLine
              x={base.idx}
              stroke={pal['--text-3']}
              strokeOpacity={0.5}
              strokeDasharray="2 3"
              label={{ value: 'baseline', position: 'top', fill: pal['--text-3'], fontSize: 10 }}
            />
          )}
          {base &&
            series.map((s) => {
              const v = base[s.key];
              return typeof v === 'number' && Number.isFinite(v) ? (
                <ReferenceLine key={`b-${s.key}`} y={v} stroke={s.color} strokeOpacity={0.55} strokeDasharray="5 4" />
              ) : null;
            })}
          <Tooltip
            isAnimationActive={false}
            cursor={{ stroke: pal['--text-3'], strokeWidth: 1, strokeDasharray: '3 3' }}
            content={
              <ChartTooltip
                series={series}
                fmt={fmt}
                xLabel={(x) => {
                  const r = data.find((d) => d.idx === x);
                  return r ? `${r.title}${r.baseline ? ' · baseline' : ''}` : '';
                }}
              />
            }
          />
          {series.map((s) => (
            <Line
              key={s.key}
              dataKey={s.key}
              name={s.label}
              stroke={s.color}
              strokeWidth={2}
              isAnimationActive={false}
              connectNulls
              type="linear"
              dot={(props: { cx?: number; cy?: number; payload?: TrendRow; index?: number }) => {
                const { cx, cy, payload, index } = props;
                if (cx === undefined || cy === undefined || !Number.isFinite(cy)) return <g key={`d-${s.key}-${index}`} />;
                if (payload?.baseline) {
                  return (
                    <rect
                      key={`d-${s.key}-${index}`}
                      x={cx - 4.5}
                      y={cy - 4.5}
                      width={9}
                      height={9}
                      transform={`rotate(45 ${cx} ${cy})`}
                      fill={pal['--surface']}
                      stroke={s.color}
                      strokeWidth={2}
                    />
                  );
                }
                return <circle key={`d-${s.key}-${index}`} cx={cx} cy={cy} r={2.5} fill={s.color} stroke="none" />;
              }}
              activeDot={{ r: 4, strokeWidth: 2, stroke: pal['--surface'] }}
            />
          ))}
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}

// ---------- runs per day (stacked) ----------

export function StackedBarChart({
  data,
  series,
  xKey,
  xFmt,
  height = 180,
}: {
  data: Record<string, number | string>[];
  series: Series[];
  xKey: string;
  xFmt: (v: string) => string;
  height?: number;
}) {
  const pal = usePalette();
  return (
    <div style={{ height }}>
      <ResponsiveContainer width="100%" height="100%">
        <BarChart data={data} margin={{ top: 6, right: 8, bottom: 0, left: -8 }} barCategoryGap="22%">
          <CartesianGrid stroke={pal['--grid']} vertical={false} />
          <XAxis
            dataKey={xKey}
            tickFormatter={(v: string) => xFmt(v)}
            stroke={pal['--border-strong']}
            tick={{ fill: pal['--text-3'], fontSize: 10.5 }}
            tickLine={false}
            minTickGap={16}
          />
          <YAxis
            width={40}
            allowDecimals={false}
            tick={{ fill: pal['--text-3'], fontSize: 11 }}
            axisLine={false}
            tickLine={false}
          />
          <Tooltip
            isAnimationActive={false}
            cursor={{ fill: pal['--grid'], fillOpacity: 0.6 }}
            content={
<ChartTooltip series={series} fmt={(v) => String(v)} headText={xFmt} />}
          />
          {series.map((s, i) => (
            <Bar
              key={s.key}
              dataKey={s.key}
              name={s.label}
              stackId="a"
              fill={s.color}
              isAnimationActive={false}
              radius={i === series.length - 1 ? [2, 2, 0, 0] : undefined}
            />
          ))}
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}
