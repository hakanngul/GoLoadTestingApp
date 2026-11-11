package api

import (
	"bytes"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hakanngul/GoLoadTestingApp/internal/model"
)

// handleRunReportHTML renders a self-contained, printable HTML report
// (inline CSS and server-drawn SVG charts, no external assets) as a
// download. Every dynamic value goes through html/template escaping.
func (s *server) handleRunReportHTML(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	win, err := parseWindow(q)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	tol, err := parseTolerance(q)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	id := r.PathValue("id")
	rep, err := s.reporter.RunReport(r.Context(), id, win, tol)
	if err != nil {
		s.writeErr(w, r, reportErr(err))
		return
	}
	pts, err := s.store.ListMetrics(r.Context(), id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	samples, err := s.store.ListErrorSamples(r.Context(), id)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	var buf bytes.Buffer
	if err := reportTmpl.Execute(&buf, newHTMLReport(rep, pts, samples, time.Now())); err != nil {
		s.writeErr(w, r, err)
		return
	}
	attachment(w, "text/html; charset=utf-8", exportName(rep.Run, ".html"))
	// The document is self-contained: forbid every external fetch.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:")
	_, _ = w.Write(buf.Bytes())
}

type htmlReport struct {
	R           model.RunReport
	Generated   string
	Started     string
	Finished    string
	Duration    string
	WindowText  string
	Steps       []namedSummary
	Charts      []svgChart
	Hist        svgHist
	BaseSteps   []namedDeltas
	Errors      []model.ErrorSample
	ErrorsTotal int
}

type namedSummary struct {
	Name string
	S    model.Summary
}

type namedDeltas struct {
	Name   string
	Deltas []model.MetricDelta
}

// maxHTMLErrors caps the error samples listed in the HTML report.
const maxHTMLErrors = 100

func newHTMLReport(rep model.RunReport, pts []model.MetricPoint, samples []model.ErrorSample, now time.Time) htmlReport {
	h := htmlReport{
		R:           rep,
		Generated:   now.UTC().Format("2006-01-02 15:04:05 UTC"),
		Started:     rep.Run.StartedAt.UTC().Format("2006-01-02 15:04:05 UTC"),
		ErrorsTotal: len(samples),
	}
	if rep.Run.FinishedAt != nil {
		h.Finished = rep.Run.FinishedAt.UTC().Format("2006-01-02 15:04:05 UTC")
		h.Duration = rep.Run.FinishedAt.Sub(rep.Run.StartedAt).Round(time.Second).String()
	}
	h.WindowText = fmt.Sprintf("%s (%s s – %s s)", rep.Window.Kind, fnum(rep.Window.From, 1), fnum(rep.Window.To, 1))
	for name, s := range rep.Summary.ByStep {
		h.Steps = append(h.Steps, namedSummary{name, s})
	}
	sort.Slice(h.Steps, func(i, j int) bool { return h.Steps[i].Name < h.Steps[j].Name })
	if rep.Baseline != nil {
		for name, d := range rep.Baseline.ByStep {
			h.BaseSteps = append(h.BaseSteps, namedDeltas{name, d})
		}
		sort.Slice(h.BaseSteps, func(i, j int) bool { return h.BaseSteps[i].Name < h.BaseSteps[j].Name })
	}
	h.Charts = []svgChart{
		buildChart("Throughput (requests/s)", pts, rep.Window, []seriesDef{
			{"rps", "#2563eb", func(p model.MetricPoint) float64 { return p.RPS }, false},
			{"target", "#9ca3af", func(p model.MetricPoint) float64 { return p.TargetRate }, false},
		}),
		buildChart("Latency (ms)", pts, rep.Window, []seriesDef{
			{"p50", "#16a34a", func(p model.MetricPoint) float64 { return p.P50Ms }, false},
			{"p95", "#d97706", func(p model.MetricPoint) float64 { return p.P95Ms }, false},
			{"p99", "#dc2626", func(p model.MetricPoint) float64 { return p.P99Ms }, false},
			{"active VUs", "#7c3aed", func(p model.MetricPoint) float64 { return float64(p.Active) }, true},
		}),
	}
	h.Hist = buildHist(rep.Histogram, rep.Summary)
	h.Errors = samples
	if len(h.Errors) > maxHTMLErrors {
		h.Errors = h.Errors[:maxHTMLErrors]
	}
	return h
}

// ---- SVG charts --------------------------------------------------------------

type seriesDef struct {
	name  string
	color string
	get   func(model.MetricPoint) float64
	right bool // plotted on the secondary (right) y axis, dashed
}

type svgChart struct {
	Title          string
	W, H           float64
	X0, Y0, X1, Y1 float64 // plot area (Y0 top, Y1 bottom)
	XTicks, YTicks []svgTick
	RTicks         []svgTick // right axis labels (secondary series), if any
	Series         []svgSeries
	BandX, BandW   float64 // report window highlight (BandW 0 = none)
	Empty          bool
}

type svgTick struct {
	Pos   float64
	Label string
}

type svgSeries struct {
	Name, Color, Points string
	LegendX             float64
	Dashed              bool
}

func buildChart(title string, pts []model.MetricPoint, win model.Window, defs []seriesDef) svgChart {
	c := svgChart{Title: title, W: 760, H: 240, X0: 56, Y0: 28, X1: 744, Y1: 208}
	if len(pts) == 0 {
		c.Empty = true
		return c
	}
	hasRight := false
	for _, d := range defs {
		hasRight = hasRight || d.right
	}
	if hasRight {
		c.X1 = 700 // room for the right axis labels
	}
	xMax, yMax, rMax := 0.0, 0.0, 0.0
	for _, p := range pts {
		xMax = math.Max(xMax, p.Elapsed)
		for _, d := range defs {
			if v := d.get(p); !math.IsNaN(v) && !math.IsInf(v, 0) {
				if d.right {
					rMax = math.Max(rMax, v)
				} else {
					yMax = math.Max(yMax, v)
				}
			}
		}
	}
	if xMax <= 0 {
		xMax = 1
	}
	yStep := niceStep(math.Max(yMax, 1e-9) / 4)
	if yMax <= 0 {
		yStep = 1
	}
	yTop := math.Max(yStep, math.Ceil(yMax/yStep)*yStep)
	xStep := niceStep(xMax / 6)
	sx := func(v float64) float64 { return c.X0 + v/xMax*(c.X1-c.X0) }
	sy := func(v float64) float64 { return c.Y1 - v/yTop*(c.Y1-c.Y0) }
	rTop := 1.0
	if hasRight {
		rStep := niceStep(math.Max(rMax, 1e-9) / 4)
		rTop = math.Max(rStep, math.Ceil(rMax/rStep)*rStep)
		for v := 0.0; v <= rTop+1e-9; v += rStep {
			c.RTicks = append(c.RTicks, svgTick{Pos: c.Y1 - v/rTop*(c.Y1-c.Y0), Label: fnum(v, -1)})
		}
	}
	for v := 0.0; v <= xMax+1e-9; v += xStep {
		c.XTicks = append(c.XTicks, svgTick{Pos: sx(v), Label: fnum(v, -1) + "s"})
	}
	for v := 0.0; v <= yTop+1e-9; v += yStep {
		c.YTicks = append(c.YTicks, svgTick{Pos: sy(v), Label: fnum(v, -1)})
	}
	if win.To > win.From && (win.From > 0 || win.To < xMax) {
		c.BandX = sx(win.From)
		c.BandW = sx(math.Min(win.To, xMax)) - c.BandX
	}
	for i, d := range defs {
		var b strings.Builder
		for _, p := range pts {
			v := d.get(p)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			y := sy(v)
			if d.right {
				y = c.Y1 - v/rTop*(c.Y1-c.Y0)
			}
			fmt.Fprintf(&b, "%.1f,%.1f ", sx(p.Elapsed), y)
		}
		c.Series = append(c.Series, svgSeries{Name: d.name, Color: d.color, Dashed: d.right,
			Points: strings.TrimSpace(b.String()), LegendX: c.X1 - float64(len(defs)-i)*78})
	}
	return c
}

// ---- latency histogram --------------------------------------------------------

type svgHist struct {
	W, H           float64
	X0, Y0, X1, Y1 float64
	Bars           []svgBar
	XTicks, YTicks []svgTick
	Marks          []svgMark
	Empty          bool
}

type svgBar struct {
	X, Y, W, H float64
	Title      string
}

type svgMark struct {
	Name, Color string
	X           float64
	LabelY      float64
}

// buildHist draws the (log-spaced, equal-width) buckets as bars with the
// p50/p95/p99 of sum as vertical markers.
func buildHist(buckets []model.HistBucket, sum model.Summary) svgHist {
	c := svgHist{W: 760, H: 240, X0: 56, Y0: 28, X1: 744, Y1: 196}
	if len(buckets) == 0 {
		c.Empty = true
		return c
	}
	var cMax, total int64
	for _, b := range buckets {
		cMax = max(cMax, b.Count)
		total += b.Count
	}
	yStep := niceStep(float64(max(cMax, 1)) / 4)
	yTop := math.Max(yStep, math.Ceil(float64(cMax)/yStep)*yStep)
	n := float64(len(buckets))
	bw := (c.X1 - c.X0) / n
	for i, b := range buckets {
		h := float64(b.Count) / yTop * (c.Y1 - c.Y0)
		pct := 0.0
		if total > 0 {
			pct = float64(b.Count) / float64(total) * 100
		}
		c.Bars = append(c.Bars, svgBar{X: c.X0 + float64(i)*bw + 0.5, Y: c.Y1 - h, W: math.Max(bw-1, 0.5), H: h,
			Title: fmt.Sprintf("%s–%s ms: %d (%s%%)", fnum(b.FromMs, 2), fnum(b.ToMs, 2), b.Count, fnum(pct, 1))})
	}
	for v := 0.0; v <= yTop+1e-9; v += yStep {
		c.YTicks = append(c.YTicks, svgTick{Pos: c.Y1 - v/yTop*(c.Y1-c.Y0), Label: fnum(v, -1)})
	}
	// X labels at bucket edges, at most ~7.
	stride := max(1, int(math.Ceil(n/6)))
	for i := 0; i <= len(buckets); i += stride {
		ms := buckets[min(i, len(buckets)-1)].FromMs
		if i == len(buckets) {
			ms = buckets[len(buckets)-1].ToMs
		}
		c.XTicks = append(c.XTicks, svgTick{Pos: c.X0 + float64(i)*bw, Label: fmtHistMs(ms)})
	}
	// Percentile markers: position by log-interpolation inside the bucket.
	pos := func(v float64) (float64, bool) {
		for i, b := range buckets {
			if v >= b.FromMs && (v < b.ToMs || i == len(buckets)-1) && b.ToMs > b.FromMs {
				f := math.Min(1, math.Log(v/b.FromMs)/math.Log(b.ToMs/b.FromMs))
				return c.X0 + (float64(i)+f)*bw, true
			}
		}
		return 0, false
	}
	for i, m := range []struct {
		name, color string
		v           float64
	}{{"p50", "#16a34a", sum.P50Ms}, {"p95", "#d97706", sum.P95Ms}, {"p99", "#dc2626", sum.P99Ms}} {
		if x, ok := pos(math.Max(m.v, buckets[0].FromMs)); ok {
			c.Marks = append(c.Marks, svgMark{Name: m.name, Color: m.color, X: x, LabelY: c.Y0 + 10 + float64(i)*12})
		}
	}
	return c
}

// fmtHistMs formats a millisecond edge compactly (1.5ms, 120ms, 2.3s).
func fmtHistMs(v float64) string {
	switch {
	case v >= 1000:
		return fnum(v/1000, 2) + "s"
	case v >= 100:
		return fnum(v, 0)
	case v >= 10:
		return fnum(v, 1)
	}
	return fnum(v, 2)
}

// niceStep rounds raw up to 1, 2 or 5 times a power of ten.
func niceStep(raw float64) float64 {
	if raw <= 0 || math.IsNaN(raw) || math.IsInf(raw, 0) {
		return 1
	}
	exp := math.Pow(10, math.Floor(math.Log10(raw)))
	switch f := raw / exp; {
	case f <= 1:
		return exp
	case f <= 2:
		return 2 * exp
	case f <= 5:
		return 5 * exp
	}
	return 10 * exp
}

// fnum formats v with prec decimals (-1 = shortest).
func fnum(v float64, prec int) string {
	if prec < 0 {
		v = math.Round(v*1e6) / 1e6
	}
	return strconv.FormatFloat(v, 'f', prec, 64)
}

var reportFuncs = template.FuncMap{
	"ms":  func(v float64) string { return fnum(v, 2) },
	"num": func(v float64) string { return fnum(v, 2) },
	"pct": func(v float64) string { return fnum(v*100, 2) + "%" },
	"delta": func(d model.MetricDelta) string {
		if d.Verdict == model.VerdictNA {
			return "–"
		}
		if d.Metric == "error_rate" {
			return fmt.Sprintf("%+.2f pp", d.Delta*100)
		}
		return fmt.Sprintf("%+.1f%%", d.Delta*100)
	},
	"val": func(metric string, v float64) string {
		if metric == "error_rate" {
			return fnum(v*100, 2) + "%"
		}
		return fnum(v, 2)
	},
	"time": func(t time.Time) string { return t.UTC().Format("15:04:05.000") },
	"pass": func(b bool) string {
		if b {
			return "pass"
		}
		return "fail"
	},
	"add": func(a, b float64) float64 { return a + b },
	"sub": func(a, b float64) float64 { return a - b },
	"f1":  func(v float64) string { return fnum(v, 1) },
	// tenth is the absolute error-rate tolerance (tolerance/10).
	"tenth": func(v float64) float64 { return v / 10 },
	"verdict": func(v model.Verdict) string {
		if v == model.VerdictNA {
			return "na"
		}
		return string(v)
	},
}

var reportTmpl = template.Must(template.New("report").Funcs(reportFuncs).Parse(reportHTML))

const reportHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GoLoad report – {{.R.Run.TestName}}{{with .R.Run.Label}} ({{.}}){{end}}</title>
<style>
:root { color-scheme: light; }
* { box-sizing: border-box; }
body { margin: 0; background: #fff; color: #111827; font: 14px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; }
main { max-width: 1000px; margin: 0 auto; padding: 32px 24px 48px; }
header { border-bottom: 2px solid #e5e7eb; padding-bottom: 16px; margin-bottom: 24px; }
h1 { font-size: 24px; margin: 0 0 4px; }
h2 { font-size: 17px; margin: 32px 0 10px; padding-bottom: 4px; border-bottom: 1px solid #e5e7eb; }
h3 { font-size: 14px; margin: 18px 0 6px; }
.sub { color: #4b5563; }
.meta { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 6px 24px; margin-top: 12px; }
.meta div span { color: #6b7280; display: block; font-size: 12px; text-transform: uppercase; letter-spacing: .04em; }
.badge { display: inline-block; padding: 1px 8px; border-radius: 10px; font-size: 12px; font-weight: 600; border: 1px solid; vertical-align: middle; }
.st-passed, .pass, .better { color: #166534; background: #dcfce7; border-color: #86efac; }
.st-failed, .fail, .worse, .st-error { color: #991b1b; background: #fee2e2; border-color: #fca5a5; }
.st-stopped, .st-running, .st-queued, .same, .na { color: #374151; background: #f3f4f6; border-color: #d1d5db; }
table { border-collapse: collapse; width: 100%; font-variant-numeric: tabular-nums; }
th, td { text-align: right; padding: 5px 8px; border-bottom: 1px solid #e5e7eb; }
th { background: #f9fafb; font-weight: 600; font-size: 12px; color: #374151; }
th:first-child, td:first-child { text-align: left; }
td.l { text-align: left; }
.kpis { display: grid; grid-template-columns: repeat(auto-fit, minmax(130px, 1fr)); gap: 10px; }
.kpi { border: 1px solid #e5e7eb; border-radius: 8px; padding: 10px 12px; }
.kpi span { display: block; font-size: 12px; color: #6b7280; }
.kpi b { font-size: 20px; }
svg { width: 100%; height: auto; display: block; }
.chart { border: 1px solid #e5e7eb; border-radius: 8px; padding: 6px; margin: 10px 0; page-break-inside: avoid; }
pre { background: #f9fafb; border: 1px solid #e5e7eb; border-radius: 6px; padding: 10px 12px; overflow-x: auto; white-space: pre-wrap; word-break: break-word; font: 12px/1.45 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
.notes { white-space: pre-wrap; }
ul.warn li { color: #92400e; }
.muted { color: #6b7280; }
.regress { font-weight: 600; }
footer { margin-top: 40px; color: #9ca3af; font-size: 12px; }
@media print {
  @page { margin: 14mm; }
  body { font-size: 11px; }
  main { max-width: none; padding: 0; }
  h2 { page-break-after: avoid; }
  table, .chart, .kpis { page-break-inside: avoid; }
  pre { white-space: pre-wrap; }
}
</style>
</head>
<body>
<main>
<header>
  <h1>{{.R.Run.TestName}} <span class="badge st-{{.R.Run.Status}}">{{.R.Run.Status}}</span></h1>
  <div class="sub">{{if .R.Run.Label}}Label: <b>{{.R.Run.Label}}</b> · {{end}}Run {{.R.Run.ID}}</div>
  <div class="meta">
    <div><span>Started</span>{{.Started}}</div>
    <div><span>Finished</span>{{if .Finished}}{{.Finished}} ({{.Duration}}){{else}}–{{end}}</div>
    <div><span>Window</span>{{.WindowText}}</div>
    <div><span>Workers</span>{{.R.Run.Workers}}</div>
    <div><span>Peak load-generator CPU</span>{{pct .R.Run.PeakCPU}}</div>
  </div>
</header>
{{with .R.Run.Error}}<p class="badge fail">Error: {{.}}</p>{{end}}

<h2>Summary</h2>
<div class="kpis">
  <div class="kpi"><span>Requests</span><b>{{.R.Summary.Requests}}</b></div>
  <div class="kpi"><span>Throughput</span><b>{{num .R.Summary.RPS}}</b> /s</div>
  <div class="kpi"><span>Error rate</span><b>{{pct .R.Summary.ErrorRate}}</b></div>
  <div class="kpi"><span>p95</span><b>{{ms .R.Summary.P95Ms}}</b> ms</div>
  <div class="kpi"><span>p99</span><b>{{ms .R.Summary.P99Ms}}</b> ms</div>
</div>
<table style="margin-top:12px">
  <tr><th>Requests</th><th>Errors</th><th>Dropped</th><th>Error rate</th><th>RPS</th><th>Mean ms</th><th>p50 ms</th><th>p90 ms</th><th>p95 ms</th><th>p99 ms</th><th>Max ms</th></tr>
  {{with .R.Summary}}<tr><td>{{.Requests}}</td><td>{{.Errors}}</td><td>{{.Dropped}}</td><td>{{pct .ErrorRate}}</td><td>{{num .RPS}}</td><td>{{ms .MeanMs}}</td><td>{{ms .P50Ms}}</td><td>{{ms .P90Ms}}</td><td>{{ms .P95Ms}}</td><td>{{ms .P99Ms}}</td><td>{{ms .MaxMs}}</td></tr>{{end}}
</table>

<h2>Thresholds</h2>
{{if .R.Thresholds}}<table>
  <tr><th>Metric</th><th>Condition</th><th>Actual</th><th>Result</th></tr>
  {{range .R.Thresholds}}<tr><td>{{.Metric}}</td><td>{{.Op}} {{num .Value}}</td><td>{{num .Actual}}</td><td><span class="badge {{pass .Passed}}">{{pass .Passed}}</span></td></tr>
  {{end}}</table>{{else}}<p class="muted">No thresholds defined.</p>{{end}}

{{if .Steps}}<h2>Per step</h2>
<table>
  <tr><th>Step</th><th>Requests</th><th>Errors</th><th>Error rate</th><th>RPS</th><th>Mean ms</th><th>p50 ms</th><th>p95 ms</th><th>p99 ms</th><th>Max ms</th></tr>
  {{range .Steps}}<tr><td>{{.Name}}</td>{{with .S}}<td>{{.Requests}}</td><td>{{.Errors}}</td><td>{{pct .ErrorRate}}</td><td>{{num .RPS}}</td><td>{{ms .MeanMs}}</td><td>{{ms .P50Ms}}</td><td>{{ms .P95Ms}}</td><td>{{ms .P99Ms}}</td><td>{{ms .MaxMs}}</td>{{end}}</tr>
  {{end}}</table>{{end}}

<h2>Over time</h2>
{{range .Charts}}<div class="chart">
<svg viewBox="0 0 {{.W}} {{.H}}" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="{{.Title}}">
  <text x="{{.X0}}" y="16" font-size="13" font-weight="600" fill="#111827">{{.Title}}</text>
  {{if .Empty}}<text x="{{.X0}}" y="110" font-size="12" fill="#6b7280">No metric points recorded.</text>{{else}}
  {{if .BandW}}<rect x="{{f1 .BandX}}" y="{{.Y0}}" width="{{f1 .BandW}}" height="{{sub .Y1 .Y0}}" fill="#eff6ff"/>{{end}}
  {{$c := .}}{{range .YTicks}}<line x1="{{$c.X0}}" x2="{{$c.X1}}" y1="{{f1 .Pos}}" y2="{{f1 .Pos}}" stroke="#e5e7eb"/><text x="{{sub $c.X0 6}}" y="{{f1 (add .Pos 4)}}" font-size="10" text-anchor="end" fill="#6b7280">{{.Label}}</text>
  {{end}}{{range .XTicks}}<line x1="{{f1 .Pos}}" x2="{{f1 .Pos}}" y1="{{$c.Y1}}" y2="{{add $c.Y1 4}}" stroke="#9ca3af"/><text x="{{f1 .Pos}}" y="{{add $c.Y1 16}}" font-size="10" text-anchor="middle" fill="#6b7280">{{.Label}}</text>
  {{end}}<line x1="{{.X0}}" x2="{{.X1}}" y1="{{.Y1}}" y2="{{.Y1}}" stroke="#9ca3af"/><line x1="{{.X0}}" x2="{{.X0}}" y1="{{.Y0}}" y2="{{.Y1}}" stroke="#9ca3af"/>
  {{range .RTicks}}<text x="{{add $c.X1 6}}" y="{{f1 (add .Pos 4)}}" font-size="10" text-anchor="start" fill="#7c3aed">{{.Label}}</text>
  {{end}}{{range .Series}}<polyline fill="none" stroke="{{.Color}}" stroke-width="1.5" stroke-linejoin="round"{{if .Dashed}} stroke-dasharray="5 3"{{end}} points="{{.Points}}"/>
  <rect x="{{f1 .LegendX}}" y="8" width="10" height="10" fill="{{.Color}}"/><text x="{{f1 (add .LegendX 14)}}" y="17" font-size="11" fill="#374151">{{.Name}}</text>
  {{end}}{{end}}
</svg></div>
{{end}}
{{with .Hist}}{{if not .Empty}}<h2>Latency distribution</h2>
<div class="chart">
<svg viewBox="0 0 {{.W}} {{.H}}" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Latency distribution">
  <text x="{{.X0}}" y="16" font-size="13" font-weight="600" fill="#111827">Requests per latency bucket (log scale, ms)</text>
  {{$c := .}}{{range .YTicks}}<line x1="{{$c.X0}}" x2="{{$c.X1}}" y1="{{f1 .Pos}}" y2="{{f1 .Pos}}" stroke="#e5e7eb"/><text x="{{sub $c.X0 6}}" y="{{f1 (add .Pos 4)}}" font-size="10" text-anchor="end" fill="#6b7280">{{.Label}}</text>
  {{end}}{{range .Bars}}<rect x="{{f1 .X}}" y="{{f1 .Y}}" width="{{f1 .W}}" height="{{f1 .H}}" fill="#2563eb"><title>{{.Title}}</title></rect>
  {{end}}{{range .XTicks}}<line x1="{{f1 .Pos}}" x2="{{f1 .Pos}}" y1="{{$c.Y1}}" y2="{{add $c.Y1 4}}" stroke="#9ca3af"/><text x="{{f1 .Pos}}" y="{{add $c.Y1 16}}" font-size="10" text-anchor="middle" fill="#6b7280">{{.Label}}</text>
  {{end}}<line x1="{{.X0}}" x2="{{.X1}}" y1="{{.Y1}}" y2="{{.Y1}}" stroke="#9ca3af"/><line x1="{{.X0}}" x2="{{.X0}}" y1="{{.Y0}}" y2="{{.Y1}}" stroke="#9ca3af"/>
  {{range .Marks}}<line x1="{{f1 .X}}" x2="{{f1 .X}}" y1="{{$c.Y0}}" y2="{{$c.Y1}}" stroke="{{.Color}}" stroke-width="1.5" stroke-dasharray="4 3"/><text x="{{f1 (add .X 4)}}" y="{{f1 .LabelY}}" font-size="11" fill="{{.Color}}">{{.Name}}</text>
  {{end}}
</svg></div>{{end}}{{end}}
{{if .R.Baseline}}{{with .R.Baseline}}<h2>Baseline comparison</h2>
<p>Compared with baseline run {{.ReferenceRunID}} (tolerance {{pct .Tolerance}}, error rate ±{{pct (tenth .Tolerance)}} absolute).
{{if .Regression}}<span class="badge worse regress">Regression</span>{{else}}<span class="badge better">No regression</span>{{end}}</p>
<table>
  <tr><th>Metric</th><th>Baseline</th><th>This run</th><th>Change</th><th>Verdict</th></tr>
  {{range .Metrics}}<tr><td>{{.Metric}}</td><td>{{val .Metric .Reference}}</td><td>{{val .Metric .Candidate}}</td><td>{{delta .}}</td><td><span class="badge {{verdict .Verdict}}">{{.Verdict}}</span></td></tr>
  {{end}}</table>{{end}}
{{range .BaseSteps}}<h3>Step {{.Name}}</h3>
<table>
  <tr><th>Metric</th><th>Baseline</th><th>This run</th><th>Change</th><th>Verdict</th></tr>
  {{range .Deltas}}<tr><td>{{.Metric}}</td><td>{{val .Metric .Reference}}</td><td>{{val .Metric .Candidate}}</td><td>{{delta .}}</td><td><span class="badge {{verdict .Verdict}}">{{.Verdict}}</span></td></tr>
  {{end}}</table>
{{end}}{{end}}
{{if .R.Run.Warnings}}<h2>Warnings</h2>
<ul class="warn">{{range .R.Run.Warnings}}<li>{{.}}</li>{{end}}</ul>{{end}}
{{if .R.Run.Notes}}<h2>Notes</h2>
<div class="notes">{{.R.Run.Notes}}</div>{{end}}
{{if .Errors}}<h2>Error samples</h2>
<p class="muted">{{len .Errors}} of {{.ErrorsTotal}} stored samples.</p>
<table>
  <tr><th>Time (UTC)</th><th>Step</th><th>Status</th><th>Message</th></tr>
  {{range .Errors}}<tr><td>{{time .T}}</td><td class="l">{{.Step}}</td><td>{{.Status}}</td><td class="l">{{.Message}}</td></tr>
  {{end}}</table>{{end}}

<h2>Scenario</h2>
<pre>{{.R.Run.YAML}}</pre>
<footer>Generated by GoLoad on {{.Generated}}. Latency percentiles are exact over the window (merged HDR histograms).</footer>
</main>
</body>
</html>
`
