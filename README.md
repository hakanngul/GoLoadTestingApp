# GoLoad

GoLoad sends HTTP and WebSocket load from one or more workers and shows the results live in a web UI. One Go binary runs as the master (API, UI, worker coordinator), as a worker, or as a standalone CLI.

```
goloadtest run scenario.yaml          # CLI: single node, exit code for CI
goloadtest serve                      # master: UI + REST API + gRPC for workers
goloadtest worker -master host:9090   # worker: generates load for the master
```

## Quick start

### macOS (UI in 3 commands)

```bash
brew install go node pnpm        # Go 1.24+, Node 22+, pnpm 10
make serve                       # builds the UI + binary, starts on :8080
open http://localhost:8080
```

`make serve` keeps tests, runs, metrics and error groups in `./goload.db`
(SQLite, created on first start; survives restarts) and allows any target. No
database server is needed. `GOLOAD_DB=memory make serve` gives a throwaway
store instead.

Requirements: Go 1.24+, Node 22 + pnpm (for the UI build). PostgreSQL 16 is optional (see [Data and backups](#data-and-backups)).

```bash
make build                                   # builds web/ then bin/goloadtest
./bin/goloadtest run examples/smoke.yaml     # CLI only, no server
```

Master with UI, single node, data in `./goload.db`:

```bash
GOLOAD_ALLOWED_TARGETS='*' ./bin/goloadtest serve -grpc-addr ""
# open http://localhost:8080
```

Master with Postgres and two workers on the same machine:

```bash
export GOLOAD_WORKER_TOKEN=$(openssl rand -hex 32)
./bin/goloadtest serve -db 'postgres://user:pass@localhost:5432/goload?sslmode=disable' \
  -allow-targets 'api.staging.example.com' -token "$(openssl rand -hex 32)"
./bin/goloadtest worker -master 127.0.0.1:9090 &
./bin/goloadtest worker -master 127.0.0.1:9090 &
```

Docker Compose (master, SQLite in the `goload-data` volume at `/data/goload.db`): copy `deploy/env.example` to `.env`, fill it in, then run `docker compose up -d`. For PostgreSQL add the override: `docker compose -f docker-compose.yml -f docker-compose.postgres.yml up -d`. For AWS (spot workers, Terraform) see [docs/aws.md](docs/aws.md).

### Data and backups

`serve -db` (or `GOLOAD_DB`) picks the store:

| Value | Store |
|---|---|
| `sqlite:./goload.db` (default), `sqlite:/data/goload.db`, or a path ending in `.db` | SQLite file (pure Go, no CGO); the directory is created if needed |
| `postgres://user:pass@host:5432/goload?sslmode=disable` (or `postgresql://`) | PostgreSQL 16 |
| `memory` | In-memory, lost on exit |

The startup log names the store in use. Schema migrations run automatically on
start for both SQLite and PostgreSQL. SQLite runs in WAL mode, so next to
`goload.db` you will see `goload.db-wal` and `goload.db-shm` while the master
runs; treat the three as one. One master per database file: SQLite fits the
single-master design (workers never touch the database). Use PostgreSQL if you
already run one or want managed backups (e.g. RDS).

Back up the SQLite store with either:

```bash
sqlite3 goload.db ".backup backup.db"   # safe while the master is running
cp goload.db backup.db                  # only while the master is stopped
```

Restore by stopping the master and putting the backup in place as `goload.db`
(delete any stale `goload.db-wal` / `goload.db-shm`). Switching between SQLite
and PostgreSQL does not migrate existing data.

## Scenario format

One YAML file per test. `internal/scenario/types.go` is the single source of truth. Examples live in `examples/`:

| File | Shows |
|---|---|
| `smoke.yaml` | Open model, simple GETs, thresholds |
| `login_flow.yaml` | Closed model, CSV users, token extract, think time |
| `spike.yaml` | Ramp and a sudden spike |
| `websocket_chat.yaml` | WebSocket: connect, join, hold, ping |
| `auth_login.yaml` | Scenario-level auth: cached per-user login, injected header |

The UI editor produces the same YAML (Form and YAML tabs stay in sync).

### Authentication

An `auth:` block authenticates iterations without making login a step of every iteration:

```yaml
auth:
  type: login              # login | bearer | basic
  scope: per_user          # default; or shared (one login per load generator)
  refresh: 15m             # re-login after this age (0 = never)
  login:                   # an HTTP step; method defaults to POST here
    path: /login
    body: '{"username":"{{username}}","password":"{{password}}"}'
    extract: [{var: token, from: json, expr: data.access_token}]
# header: Authorization    value: Bearer {{token}}   (defaults)
```

- `per_user` caches one login per distinct rendered login request (usually one per CSV row); concurrent iterations for the same user wait for a single login. Logins are reported as step `auth:login`; cache hits report nothing.
- The header is added to every HTTP step and WebSocket handshake unless the step sets it itself. `inject: false` only exposes the extracted variables.
- A failed login fails the iteration and is retried next time; a `401` from any step drops the cached login.
- `bearer` (`token: "{{api_key}}"`) and `basic` (`username`, `password`) need no login; `{{var}}`s come from CSV data.

## How it measures

- **Open model (default).** The target is iterations per second. Arrival times come from the integral of the ramp, so there is no drift. When `max_vus` iterations are already in flight, a new arrival is dropped and counted, never queued.
- **Closed model.** The target is the number of virtual users looping the steps.
- **Coordinated omission.** Latency of an iteration's first step is measured from its *scheduled* start, not from when it was sent.
- **Percentiles.** Each worker sends HDR histograms every second. The master merges them; percentiles are never averaged.
- **Errors.** HTTP 400 and above counts as an error unless the step asserts on status explicitly.
- **Self-check.** Generator CPU is reported. A run gets a warning if it reaches 80%, because latency may then come from the generator instead of the target.

Measured in this repo's environment (local target):
- Single node held a 2000 rps target at 1998–2003 rps.
- Three workers held 600 it/s × 2 steps at 1189–1211 req/s, for 36,000/36,000 requests.

## Distributed mode

Workers dial the master over gRPC (`proto/cluster.proto`) and keep one stream open, so workers need no inbound ports. For each run the master:
1. splits the load exactly across workers (including ramps);
2. partitions `unique` CSV rows so no row is used twice;
3. starts all workers at the same instant;
4. merges each second once all workers have reported it (stragglers get 2 s).

A lost worker produces a warning, and the run continues with the rest. Workers reconnect automatically after a master restart. Runs that were active during a restart are marked `error`.

How the number of workers is chosen:
- `workers <= 1` with no remote workers connected: the run executes inside the master (single-node mode).
- More workers than are idle: the master launches them through the AWS provisioner when one is configured, and terminates them afterwards.

## Security

See [docs/security.md](docs/security.md). In short:
- `serve` refuses to start without a target allowlist.
- `GOLOAD_TOKEN` protects the API and UI.
- `GOLOAD_WORKER_TOKEN` protects the worker port.

## Development

```bash
make test                                    # go test -race ./...
GOLOAD_TEST_DSN='postgres://...' make test   # also runs the store suite on Postgres (SQLite always runs)
cd web && pnpm mock & pnpm dev               # UI against a mock API on :8080
make proto                                   # regenerate gRPC code (needs protoc)
scripts/compare-k6.sh http://target/health 200 60s   # accuracy check vs k6
```

| Package | Responsibility |
|---|---|
| `internal/scenario` | YAML parse, defaults, validation, `{{var}}` rendering |
| `internal/data` | CSV feeder (sequential / random / unique) |
| `internal/engine` | Open and closed load models |
| `internal/protocol/httpx`, `wsx`, `executor` | HTTP and WebSocket steps, extract and assert |
| `internal/metrics` | Sharded HDR collector, merge, thresholds |
| `internal/runner` | Run lifecycle: local or distributed, persistence, live fan-out |
| `internal/cluster` | gRPC master and worker, load split, per-second merge |
| `internal/provision` | AWS spot worker launch and terminate |
| `internal/store` | SQLite (default), Postgres and in-memory stores, migrations |
| `internal/api` | REST API, SSE live stream, embedded UI |
| `internal/allowlist` | Target allowlist |
| `web/` | React + TypeScript UI |

Exit codes of `run`: `0` passed, `1` threshold failed, `2` invalid scenario or usage, `3` run error.
