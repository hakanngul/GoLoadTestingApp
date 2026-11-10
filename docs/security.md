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

- **Scenarios and CSV files** are stored in Postgres. CSV files can contain test credentials. Use dedicated test accounts, never real users.
- **Error samples:** at most 100 per run, each message capped at 1 KB. They may contain fragments of response bodies.
- **Restarts:** runs that were active when the master restarted are marked `error`. They are not resumed.

## Reporting

Report security issues privately to the repository owner, not in public issues.
