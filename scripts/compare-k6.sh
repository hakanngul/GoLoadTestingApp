#!/usr/bin/env bash
# Phase 1 exit criterion: for the same target and the same open-model load,
# goloadtest's p95 must be within 5% of k6's.
#
# Usage: scripts/compare-k6.sh <base_url> [rps] [duration]
#   e.g. scripts/compare-k6.sh http://127.0.0.1:18080/health 200 60s
# Needs: k6 on PATH, bin/goloadtest (make build), python3.
#
# Run it against a STABLE target (not shared, not autoscaling) and run each
# tool alone, never both at the same time. Repeat 3x; single runs are noisy.
set -euo pipefail

url=${1:?usage: compare-k6.sh <url> [rps] [duration]}
rps=${2:-200}
dur=${3:-60s}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

base=$(python3 -c 'import sys,urllib.parse as u; p=u.urlsplit(sys.argv[1]); print(f"{p.scheme}://{p.netloc}")' "$url")
path=$(python3 -c 'import sys,urllib.parse as u; p=u.urlsplit(sys.argv[1]); print((p.path or "/") + ("?"+p.query if p.query else ""))' "$url")

cat > "$work/k6.js" <<JS
import http from 'k6/http';
export const options = {
  scenarios: { open: { executor: 'constant-arrival-rate', rate: ${rps}, timeUnit: '1s',
    duration: '${dur}', preAllocatedVUs: 200, maxVUs: 2000 } },
  summaryTrendStats: ['p(50)', 'p(95)', 'p(99)'],
};
export default function () { http.get('${url}'); }
JS

cat > "$work/goload.yaml" <<YAML
name: k6-compare
target:
  base_url: ${base}
load:
  model: open
  stages:
    - duration: 1ms
      target: ${rps}
    - duration: ${dur}
      target: ${rps}
  max_vus: 2000
steps:
  - name: get
    path: ${path}
YAML

echo "== k6 =="
k6 run --quiet --summary-export "$work/k6.json" "$work/k6.js" >/dev/null
echo "== goloadtest =="
./bin/goloadtest run -quiet -out "$work/goload.json" "$work/goload.yaml" >/dev/null || true

python3 - "$work/k6.json" "$work/goload.json" <<'PY'
import json, sys
k6 = json.load(open(sys.argv[1]))["metrics"]["http_req_duration"]
gl = json.load(open(sys.argv[2]))["summary"]
ns_to_ms = 1e-6  # goloadtest JSON report stores durations in nanoseconds
rows = [("p50", k6["p(50)"], gl["P50"] * ns_to_ms),
        ("p95", k6["p(95)"], gl["P95"] * ns_to_ms),
        ("p99", k6["p(99)"], gl["P99"] * ns_to_ms)]
print(f"{'':5}{'k6 ms':>10}{'goload ms':>12}{'diff':>9}")
for name, a, b in rows:
    print(f"{name:5}{a:10.2f}{b:12.2f}{(b - a) / a * 100:8.1f}%")
d = abs(rows[1][2] - rows[1][1]) / rows[1][1] * 100
print("PASS" if d <= 5 else "FAIL", f"p95 difference {d:.1f}% (criterion: <= 5%)")
PY
