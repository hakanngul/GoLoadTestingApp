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
store instead. Runs are kept forever by default; `GOLOAD_RETENTION=90d make serve`
deletes finished runs older than 90 days (see [Retention](#retention)).

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

#### Retention

Every run stores one metric point (with its HDR histograms) per second, so the
database grows with every test you run: roughly 4 KiB per run-second on SQLite,
about 2.4 MiB for a 10-minute run. Nothing is deleted unless you opt in:

```bash
goloadtest serve -retention 90d ...        # or GOLOAD_RETENTION=90d; also 720h, 1d12h
goloadtest prune -older-than 30d -dry-run  # one-shot: list what would be deleted
goloadtest prune -older-than 30d           # ... and delete it (-db as for serve)
```

With `-retention` the master prunes at start and then every hour, and logs how
many runs it deleted; `0` (the default) keeps everything, and anything below
`1h` is rejected. A run is deleted, with its metrics, error samples and error
groups, once it has finished (any status but queued/running) and its finish
time (start time if it never recorded one) is older than the retention. Two
runs are always kept, whatever their age: **a test's baseline run**, and **the
latest finished run of each test**, so the Tests page, trends and comparisons
always have something to show. Deleting a test still deletes all of its runs.
`GET /api/health` reports the active setting as `retention_seconds`, and the
Runs page shows it.

SQLite gives the space back to the file system after each prune that deleted
something (incremental auto-vacuum, run in 16 MiB steps). A database file
created before this feature has to be rewritten once with a full `VACUUM` to
enable that; it happens on its first prune (after the delete, so on the
smaller file), blocks the master's writes while it runs and needs free disk
space for a copy of the remaining data. Measured: 50 runs x 600 s (119 MiB),
45 runs pruned to 12 MiB in 0.4 s with the one-time `VACUUM`, 1.3 s with the
incremental path. PostgreSQL reuses the freed space through autovacuum; the
files only shrink with a manual `VACUUM FULL`.

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

### Cookies and redirects

GoLoad behaves like Locust (`requests.Session`), JMeter (Cookie Manager + Follow Redirects) and a browser: every virtual user keeps its cookies, and redirects are followed. Both are on by default and need no YAML.

```yaml
target:
  base_url: https://www.example.com
  cookies: false            # default true: keep cookies per virtual user
  follow_redirects: false   # default true: follow 301/302/303/307/308
  max_redirects: 5          # default 10
steps:
  - name: old link
    path: /old
    follow_redirects: false # per step; overrides the target setting
    assert: [{type: status, value: "301"}]
```

- **Cookies.** `Set-Cookie` of every response (each redirect hop, the `auth.login` request and WebSocket handshakes included) is stored in the virtual user's cookie jar and sent back on later requests to that site, HTTP and WebSocket alike. A `Cookie` header you set yourself is merged with the jar; your cookie wins on a name clash.
  - *Closed model:* one jar per virtual user for its whole life, across iterations, like a Locust user or a JMeter thread. A site that first answers `302` + `Set-Cookie` is redirected once per VU.
  - *Open model:* every iteration is a new visitor and starts with an empty jar (it is redirected once per iteration). Cookies are still shared between the steps of that iteration.
  - *`auth: {type: login}`:* the cookies the login sets are cached with the token and given to every virtual user that reuses the cached login (see Authentication).
- **Redirects.** A step reports the **final** response: status, assertions, `extract` (including `from: header`) and bytes are those of the last response, and its latency (and `target.timeout`) covers the whole chain. `303`, and `301`/`302` after anything but GET/HEAD, continue as a GET without body; `307`/`308` repeat method and body. A chain that would repeat a request it already made fails at once with `redirect loop: /a → /b → /a`; longer chains stop after `max_redirects` hops.
- **Where a redirect may lead.** A hop that stays on the same host is always followed. A hop to another host is followed only if the server's target allowlist has that host (`redirect to https://… not allowed (host not in allowlist)` otherwise); see [docs/security.md](docs/security.md) for distributed workers. `Authorization`, the header injected by `auth:`, your own `Cookie` header and a `Host` override are never sent to another host.
- **User-Agent.** Requests are sent as `GoLoad/<version>` unless a target or step header sets `User-Agent`.

> **Behaviour change.** Earlier versions never followed redirects and kept no cookies. A step that expects a redirect status (`assert: status 301/302`, or `extract` of the `Location` header) now sees the final response instead: add `follow_redirects: false` to that step. Scenarios that sent a `Cookie` header by hand keep working. To get the old behaviour everywhere, set `target.cookies: false` and `target.follow_redirects: false`.

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
- Session cookies work like the token: the login runs as a new visitor, the cookies it sets are cached with the extracted variables, and every virtual user that uses that login gets them in its cookie jar before its first step. With `scope: shared` all virtual users share one server-side session; with `per_user` there is one per distinct login. A re-login (`refresh`, or after a `401`) replaces them.
- `bearer` (`token: "{{api_key}}"`) and `basic` (`username`, `password`) need no login; `{{var}}`s come from CSV data.

### Test request (`goloadtest try`)

Before starting a run, send ONE iteration and see exactly what the target answers:

- **UI:** the **Test request** button next to Run / Save in both editors sends the current, possibly unsaved scenario. A drawer shows each step (status, time, rendered URL, every redirect that was followed, the names of the cookies sent and stored, headers with secrets masked, a body snippet, extracted variables, every assertion) and hints at the top. Hints that carry a fix (e.g. a new `base_url`) have an **Apply** button that edits the form.
- **CLI:** `goloadtest try [-v] [-json] scenario.yaml` prints the same report; exit 0 when every step passed, 1 otherwise.
- **API:** `POST /api/tests/try` `{yaml, test_id?}` (see `internal/api/doc.go`).

It runs the login (if any) and each step once, in order, through the same executors as a run, and stops at the first failing step like a run does. `{{vars}}` come from the first CSV row (saved tests with an uploaded file; the CLI reads the file next to the scenario). The server's target allowlist applies. Limits: 30 s in total, the scenario timeout per step, the first 20 steps, think times and WebSocket hold skipped, body snippets up to 8 KiB of text, at most 4 test requests at once per server.

Hints explain the usual reasons a run fails with 100% errors, and what is worth changing: a **redirect to another host or scheme** that was followed (e.g. `https://example.com/` → `https://www.example.com:443/`: every request would make that extra hop, so the hint suggests `base_url: https://www.example.com`), a redirect that was **not allowed** or **loops** (with cookies disabled: enable cookies), a redirect to the same URL that resolved once the cookie came back (information only), a **WAF / bot protection** block (Cloudflare, Akamai, …), **rate limiting** (429), `{{var}}`s that were **sent literally** because nothing defines them, extracts that did not match, failed assertions, and **TLS / DNS / connection** errors in plain words. `goloadtest validate` and the editor's Validation panel also warn about `{{var}}` references that no CSV column, earlier extract or auth variable defines.

In runs, a failing `3xx` of a step with `follow_redirects: false` names its target, e.g. `assert status: got 301, want eq 200 (redirects to https://www.example.com:443/)`.

## Scheduled tests

Plan runs of a saved test: **once** at a date and time, or **recurring**
(daily, weekdays, chosen weekdays at HH:MM, or any 5-field cron expression
such as `*/30 8-18 * * 1-5`). Use the **Schedule** button on a test (list or
editor) or the **Schedules** page; the API is `/api/schedules` (see
`internal/api/doc.go`).

Each schedule can override what a run does, without changing the saved test:

- **Load** — *Scenario load* (as saved), *Scale peak* (keep the stage shape
  and model, scale every stage so the peak becomes N VUs or N it/s; the unit
  must match the scenario's model), or *Ramp & hold* (replace the stages with
  ramp-up → hold → optional ramp-down; VUs selects the closed model, it/s the
  open model; ramp-up 0 reaches full load within 1 s). The override is applied
  to a copy of the scenario when the schedule fires and validated like any run;
  the run's frozen YAML shows exactly what ran.
- **Workers**, **run label** (default `scheduled: <name>`) and **notes**.
- **Overlap** — when another run is active on the server: *skip* this
  occurrence (default) or *wait* up to N minutes (default 30) and then run it
  (or record it as skipped).

Runs started by a schedule carry `schedule_id` and show a **scheduled** badge
linking back to it. *Run now* starts a run with the schedule's settings
immediately (it ignores timing and the overlap policy).

Semantics worth knowing:

- **The server must be running at fire time.** Schedules run inside
  `goloadtest serve`; there is no external cron.
- **Missed runs are not run late.** An occurrence found more than 1 minute
  after it was due (e.g. the server was down) is recorded as `missed` and the
  schedule moves on to its next time. An occurrence that was waiting (queue)
  when the server stopped is also recorded as `missed`.
- **Exactly once.** The next fire time is advanced in the database with a
  compare-and-set before a run starts, so restarts, concurrent edits or two
  masters on the same Postgres never start an occurrence twice.
- **One-time schedules** disable themselves after their occurrence (started,
  skipped, missed or error). Each schedule shows its last result
  (`started`/`queued`/`skipped`/`missed`/`error`, with a message and a link to
  the run).
- **Time zones.** Times are wall-clock times in the schedule's IANA zone
  (default: your browser's), evaluated with daylight saving time: a time that
  does not exist on a spring-forward day (e.g. 02:30) fires when the clock
  jumps (03:00); a time that occurs twice on a fall-back day fires once, at
  its first occurrence. The dialog previews the next 5 fire times.
- Deleting a test deletes its schedules; deleting a schedule keeps its runs.

## How it measures

- **Open model (default).** The target is iterations per second. Arrival times come from the integral of the ramp, so there is no drift. When `max_vus` iterations are already in flight, a new arrival is dropped and counted, never queued.
- **Closed model.** The target is the number of virtual users looping the steps.
- **Coordinated omission.** Latency of an iteration's first step is measured from its *scheduled* start, not from when it was sent.
- **Virtual users keep cookies.** Closed model: one cookie jar per virtual user for the whole run. Open model: each iteration is a new visitor with an empty jar. Turn off with `target.cookies: false`. See [Cookies and redirects](#cookies-and-redirects).
- **Redirects are followed** (up to 10 hops). A step counts as one request: its status is the final one and its latency spans the whole chain, so a step that is redirected on every request measures two round trips. Use `follow_redirects: false` on a step (or the target) to measure the 3xx itself.
- **Percentiles.** Each worker sends HDR histograms every second. The master merges them; percentiles are never averaged.
- **Errors.** HTTP 400 and above (of the final response) counts as an error unless the step asserts on status explicitly.
- **Error groups.** Every failure is counted in a group keyed by step, status and a *normalized* message (at most 100 groups per run, then one "other errors" group). Request-specific parts are replaced so that one cause stays one group: UUIDs become `{uuid}`, long hex/base64 tokens `{id}`, numbers of 4+ digits `{n}` (`status 503` is kept), long durations `{dur}`, timestamps `{ts}`, URL path numbers `{n}`, query values `{v}` (keys kept), and the ephemeral local port in `127.0.0.1:54321->…` `{port}`. Each group keeps its first raw message as an example: the UI shows it under "Show example" and copies it.
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
- `serve` refuses to start without a target allowlist; redirects to hosts that are not on it are not followed.
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
