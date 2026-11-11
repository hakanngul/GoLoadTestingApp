import { useMemo, type ReactNode } from 'react';
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis, type TooltipProps } from 'recharts';
import { useNavigate } from 'react-router-dom';
import type { SuiteRun } from '../api/types';
import { fmtMs, fmtPct } from '../lib/format';
import { fmtShortDate, runName } from '../lib/report';
import { suiteTrend, type SuiteTrendPoint } from '../lib/suite';
import { ChartCard, runColors, usePalette, yMs, type Palette, type Series } from './charts';

/** Lines drawn in the p95 chart; the rest are left out. */
const MAX_LINES = 8;
/** Finished runs shown in the trend. */
export const TREND_RUNS = 30;

type Row = { idx: number; point: SuiteTrendPoint; pass: number | null } & Record<string, unknown>;

function statusColor(status: SuiteTrendPoint['status'], pal: Palette): string {
  if (status === 'passed') return pal['--series-green'];
  if (status === 'stopped') return pal['--series-muted'];
  return pal['--series-red'];
}

function TrendTooltip({
  active,
  payload,
  series,
  fmt,
  showStatus,
}: TooltipProps<number, string> & { series: Series[]; fmt: (v: number) => string; showStatus?: boolean }) {
  if (!active || !payload || payload.length === 0) return null;
  const row = payload[0]?.payload as Row | undefined;
  if (!row) return null;
  const p = row.point;
  return (
    <div className="chart-tooltip">
      <div className="tt-head">
        {fmtShortDate(p.started_at, true)}
        {p.label ? ` · ${p.label}` : ''}
        {showStatus ? ` · ${p.status}` : ''}
      </div>
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

function TrendLines({
  rows,
  series,
  fmt,
  yFmt,
  domain,
  height,
  dotColor,
  onPoint,
  showStatus,
}: {
  rows: Row[];
  series: Series[];
  fmt: (v: number) => string;
  yFmt: (v: number) => string;
  domain?: [number, number];
  height: number;
  dotColor?: (p: SuiteTrendPoint) => string;
  onPoint: (p: SuiteTrendPoint) => void;
  showStatus?: boolean;
}) {
  const pal = usePalette();
  return (
    <div style={{ height }}>
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={rows} margin={{ top: 12, right: 16, bottom: 0, left: 4 }}>
          <CartesianGrid stroke={pal['--grid']} vertical={false} />
          <XAxis
            dataKey="idx"
            type="category"
            tickFormatter={(v: number) => (rows[v] ? runName(rows[v].point) : '')}
            stroke={pal['--border-strong']}
            tick={{ fill: pal['--text-3'], fontSize: 10.5 }}
            tickLine={false}
            interval="preserveStartEnd"
            minTickGap={16}
          />
          <YAxis
            width={52}
            domain={domain}
            allowDecimals
            tickFormatter={(v: number) => yFmt(v)}
            tick={{ fill: pal['--text-3'], fontSize: 11 }}
            axisLine={false}
            tickLine={false}
          />
          <Tooltip
            isAnimationActive={false}
            cursor={{ stroke: pal['--text-3'], strokeWidth: 1, strokeDasharray: '3 3' }}
            content={<TrendTooltip series={series} fmt={fmt} showStatus={showStatus} />}
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
              dot={(props: { cx?: number; cy?: number; payload?: Row; index?: number }) => {
                const { cx, cy, payload, index } = props;
                const k = `d-${s.key}-${index}`;
                if (cx === undefined || cy === undefined || !payload || !Number.isFinite(cy)) return <g key={k} />;
                const color = dotColor ? dotColor(payload.point) : s.color;
                return (
                  <g key={k} style={{ cursor: 'pointer' }} onClick={() => onPoint(payload.point)}>
                    <circle cx={cx} cy={cy} r={9} fill="transparent" />
                    <circle cx={cx} cy={cy} r={3.5} fill={color} stroke={pal['--surface']} strokeWidth={1} />
                  </g>
                );
              }}
              activeDot={{ r: 5, strokeWidth: 2, stroke: pal['--surface'], cursor: 'pointer' }}
            />
          ))}
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}

/** Pass rate and per-test p95 across a suite's finished runs (runs newest first). */
export function SuiteTrend({ runs }: { runs: readonly SuiteRun[] }): ReactNode {
  const nav = useNavigate();
  const pal = usePalette();
  const trend = useMemo(() => {
    const t = suiteTrend(runs);
    return { ...t, points: t.points.slice(-TREND_RUNS) };
  }, [runs]);
  const { points, cases } = trend;

  const shown = useMemo(() => {
    // Only cases with at least one data point in the window, capped.
    const withData = cases.filter((c) => points.some((p) => p.cases[c.key]));
    return { list: withData.slice(0, MAX_LINES), hidden: Math.max(0, withData.length - MAX_LINES) };
  }, [cases, points]);

  if (points.length < 2) return null;

  const colors = runColors(pal);
  const passSeries: Series[] = [{ key: 'pass', label: 'Pass rate', color: pal['--accent'] }];
  const p95Series: Series[] = shown.list.map((c, i) => ({
    key: `p95:${c.key}`,
    label: c.name || 'deleted test',
    color: colors[i % colors.length],
    dashed: i >= colors.length,
  }));
  const rows: Row[] = points.map((p, idx) => {
    const row: Row = { idx, point: p, pass: p.passRate };
    for (const c of shown.list) row[`p95:${c.key}`] = p.cases[c.key]?.p95_ms;
    return row;
  });
  const open = (p: SuiteTrendPoint) => nav(`/suite-runs/${p.id}`);

  return (
    <div className="stack" style={{ marginTop: 16, gap: 12 }}>
      <div className="card-head" style={{ padding: 0, border: 'none' }}>
        <h2>Trend</h2>
        <span className="muted" style={{ fontSize: 12 }}>
          last {points.length} finished runs · click a point to open the run
        </span>
      </div>
      <div className="grid-2" style={{ gap: 12 }}>
        <ChartCard title="Pass rate" series={passSeries}>
          <TrendLines
            rows={rows}
            series={passSeries}
            fmt={(v) => fmtPct(v)}
            yFmt={(v) => `${Math.round(v * 100)}%`}
            domain={[0, 1]}
            height={200}
            dotColor={(p) => statusColor(p.status, pal)}
            onPoint={open}
            showStatus
          />
        </ChartCard>
        <ChartCard title="p95 latency per test (ms)" series={p95Series}>
          {p95Series.length === 0 ? (
            <div style={{ height: 200 }}>
              <div className="chart-empty">No finished test summaries</div>
            </div>
          ) : (
            <TrendLines rows={rows} series={p95Series} fmt={fmtMs} yFmt={yMs} height={200} onPoint={open} />
          )}
          {shown.hidden > 0 && (
            <p className="hint" style={{ margin: '6px 0 0' }}>
              Showing the first {MAX_LINES} test cases; {shown.hidden} more not charted.
            </p>
          )}
        </ChartCard>
      </div>
    </div>
  );
}
