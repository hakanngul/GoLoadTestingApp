# Security

A load tester is a tool for generating a lot of traffic. Most of the controls below exist so that this traffic only ever reaches systems you are allowed to test.

## Target allowlist (required)

`serve` will not start without `-allow-targets` or `GOLOAD_ALLOWED_TARGETS`. Entries:

| Entry | Matches |
|---|---|
| `api.example.com` | that host only |
| `*.staging.example.com` | any subdomain, not the apex itself |
| `10.0.0.0/8` | targets given as IP literals in that range |
| `*` | everything (local development only; logged as a warning, rejected by the Terraform module) |

Matching ignores ports and schemes.

The master checks the scenario's `base_url` and every absolute step URL (for example `ws://` URLs) before a run starts. A run that fails the check gets HTTP 403 and no run record is created.

**Redirects.** Steps follow redirects by default, so every hop is checked as well, while the run is going:

- A hop to the **same host** as the request being redirected is always followed (scheme and port may change, e.g. `http` → `https`; the allowlist ignores both anyway). It is not a new target.
- A hop to **another host** is followed only if that host is on the allowlist. Otherwise nothing is sent to it and the step fails with `redirect to https://other.example/ not allowed (host not in allowlist)`. This applies to runs executed by the master, to Test request, and to redirect chains (each hop is checked against the host it comes from).
- **Workers** do not receive the master's allowlist. A worker therefore follows a redirect to another host only if that host is one of the scenario's own targets (`base_url` or an absolute step URL), which the master checked before the run started; anything else fails with `redirect to … not allowed (host is not a target of this scenario; …)`. To let workers follow redirects to further hosts, start them with their own list: `goloadtest worker -allow-targets '…'` or `GOLOAD_ALLOWED_TARGETS` (same syntax; hops are then checked against it).
- `goloadtest run` and `goloadtest try` have no allowlist and follow every http(s) redirect, like curl `-L`.
- Credentials stay with the host they were written for: `Authorization`, `Proxy-Authorization`, the header injected by `auth:`, an explicit `Cookie` header and a `Host` override are dropped when a redirect leaves the host of the step's first request (or downgrades it from https to http). Cookies from the jar follow the usual cookie rules (domain, path, `Secure`).

**Known limitation.** A step path made entirely of a template (`path: "{{url}}"`) is resolved per iteration from CSV data or extracted values, so the master cannot vet it up front. Plain HTTP paths must start with `/`, which keeps the request on the base host. If you allow scenario authors you do not trust, also restrict worker egress at the network level: `worker_egress_cidrs` in `deploy/aws`, or a security group or firewall.

Before any test against a shared or production system:
- Get written permission from the system owner.
- Agree on a time window.
- Share the worker egress IPs so WAF and rate-limit rules don't skew the result.
- On AWS, check the current network stress test policy: https://aws.amazon.com/ec2/testing/.

## API and UI authentication

`-token` / `GOLOAD_TOKEN` sets a bearer token. When it is set, every `/api/*` route except `/api/health` requires it. The token is compared in constant time. The live stream also accepts `?access_token=` because browsers cannot set headers on EventSource. The UI asks for the token on its sign-in screen and keeps it in localStorage.

This is single-user auth, by design: there are no roles and no multi-tenancy, which are out of scope for v1. Port 8080 is plain HTTP. Put TLS in front of it (a reverse proxy, an ALB, or SSM port forwarding as described in docs/aws.md) whenever the network between you and the master is not trusted.

## Worker authentication

Workers present `GOLOAD_WORKER_TOKEN` as gRPC metadata. `serve` refuses to open the worker port without one. Pass the token through the environment, never as a flag, because flags are visible in `ps`.

The gRPC link is plaintext by default (`-insecure=true`). It is meant for a private network or VPC: on AWS, port 9090 is reachable only from the worker security group. Workers launched by the AWS provisioner receive the token in their user data. Anyone allowed `ec2:DescribeInstanceAttribute` in the account can read it, so scope that permission accordingly.

## Data

- **Scenarios and CSV files** are stored in the master's database: by default the SQLite file `goload.db` (`/data/goload.db` in Docker), or PostgreSQL when `GOLOAD_DB` points to one. CSV files can contain test credentials, so treat the database file and its backups as secrets: keep it on an encrypted volume (the AWS master's root volume is encrypted), readable only by the service user, and out of version control (`goload.db*` is in `.gitignore`). Use dedicated test accounts, never real users.
- **Error samples:** at most 100 per run, each message capped at 1 KB. They may contain fragments of response bodies.
- **Restarts:** runs that were active when the master restarted are marked `error`. They are not resumed.

## Reporting

Report security issues privately to the repository owner, not in public issues.
