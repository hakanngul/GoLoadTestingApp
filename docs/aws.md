# Running goload on AWS

This page covers the Phase 4 deployment: one small always-on master and
short-lived spot workers that exist only while a run is executing.

> **Status.** The Go provisioner (`internal/provision`) is unit-tested against
> a fake EC2 API. The Terraform in `deploy/aws` and the user-data scripts have
> **not** been applied against a real AWS account yet. Treat the first
> deployment as a test: use a sandbox account and check every step.

## 1. Architecture

```
                 admin CIDR only                         allowlisted targets
   browser ───────────── :8080 ──┐                    ┌──────► https://target...
                                 ▼                    │
   ┌──────────── master EC2 (t3.small, always on) ─┐  │
   │  docker compose                               │  │  egress (IGW, public IPs)
   │   ├─ goloadtest serve  :8080 REST + UI        │  │
   │   │                    :9090 gRPC coordinator │◄─┼── workers dial in (worker SG only)
   │   └─ SQLite /data/goload.db (volume on EBS)   │  │
   │  instance role: Run/Terminate goload workers  │  │
   └──────────────┬────────────────────────────────┘  │
                  │ RunInstances (launch template, spot, user data)
                  ▼                                   │
   ┌── worker EC2 × N (c7i.large spot, per run) ──────┤
   │  kernel tuning (fd limits, port range, tw_reuse) │
   │  docker run goloadtest worker -master <ip>:9090 ─┘
   │  no inbound ports; self-terminates after max lifetime
   └───────────────────────────────────────────────────
```

Why this shape:

- **EC2 instead of Fargate.** Load generators need kernel tuning that Fargate
  does not allow: large file-descriptor limits, the full ephemeral port range
  (`net.ipv4.ip_local_port_range`), `tcp_tw_reuse`, host networking.
- **Spot workers.** A run is short and restartable, so interruptible capacity
  is fine and is usually much cheaper than on-demand. The provisioner falls back
  through several instance types and subnets, and then to on-demand unless
  `spot_only = true`.
- **One image, two roles.** The same container runs `serve` (master) and
  `worker`.
- **SQLite on the master's EBS volume.** There is exactly one master and
  workers never touch the database, so a single SQLite file
  (`/data/goload.db` in the `goload-data` Docker volume on the encrypted gp3
  root volume) is enough: no database server, no password, nothing extra to
  pay for. PostgreSQL (e.g. Amazon RDS) remains an option through
  `GOLOAD_DB=postgres://...` and `docker-compose.postgres.yml`. CSV and report
  files will move to S3 later.

### Components in this repository

| Path | What it is |
|---|---|
| `Dockerfile` | Multi-stage build: Node 22 + pnpm builds `web/`, Go 1.24 builds a static binary, runtime image is distroless `static:nonroot`. |
| `docker-compose.yml` | Master with SQLite in the `goload-data` volume (the master instance writes an equivalent file without the build step). `docker-compose.postgres.yml` is an optional override that adds `postgres:16`. |
| `deploy/env.example` | Template for the `.env` file that compose reads. |
| `deploy/aws/` | Terraform: security groups, IAM, SSM secrets, master instance, worker launch template. |
| `internal/provision/` | `Provisioner` interface, `Noop`, and the EC2 implementation `AWS`. |

### How the master uses `internal/provision`

```go
p, err := provision.NewAWS(ctx, provision.AWSConfig{
    Region:           os.Getenv("GOLOAD_AWS_REGION"),
    LaunchTemplateID: os.Getenv("GOLOAD_AWS_LAUNCH_TEMPLATE_ID"),
    SubnetIDs:        split(os.Getenv("GOLOAD_AWS_SUBNET_IDS")),
    InstanceTypes:    split(os.Getenv("GOLOAD_AWS_INSTANCE_TYPES")),
    MaxPrice:         os.Getenv("GOLOAD_AWS_MAX_PRICE"),
    SpotOnly:         os.Getenv("GOLOAD_AWS_SPOT_ONLY") == "true",
    WorkerImage:      os.Getenv("GOLOAD_WORKER_IMAGE"),
    Logger:           log,
})
insts, err := p.Launch(ctx, provision.LaunchRequest{
    RunID: run.ID, Count: n,
    MasterAddr:  os.Getenv("GOLOAD_MASTER_ADVERTISE_ADDR"), // private-ip:9090
    WorkerToken: os.Getenv("GOLOAD_WORKER_TOKEN"),
})
// insts are always the ones that started. A *provision.ShortfallError means
// fewer than n started; decide whether to run with fewer workers or abort.
defer p.Terminate(context.WithoutCancel(ctx), ids(insts))

// Safety net: a background ticker every ~10 minutes.
p.TerminateStale(ctx, longestAllowedRun+30*time.Minute)
```

Behaviour of `AWS.Launch`:

1. It walks the plan *spot × instance types × subnets* in order, then
   *on-demand × instance types × subnets*. The on-demand pass is skipped when
   `SpotOnly` is set.
2. `InsufficientInstanceCapacity`, `SpotMaxPriceTooLow`, `Unsupported` and
   `InsufficientFreeAddressesInSubnet` move it to the next type or subnet.
   `MaxSpotInstanceCountExceeded`, `VcpuLimitExceeded` and
   `InstanceLimitExceeded` skip the rest of that market, because they are
   account quotas. Any other error (permissions, for example) stops the walk.
3. Each call uses `MinCount=1, MaxCount=remaining`, so partial capacity is
   accepted. Spot requests are one-time and terminate on interruption.
4. Instances and volumes are tagged `goload:run-id=<run>`,
   `goload:role=worker`, `Name=goload-worker-<run>` and any extra `Tags`.
5. The user data is a self-contained bash script. It tunes the kernel, installs
   Docker, logs in to ECR if needed and starts the worker. It also runs
   `shutdown -h +<MaxLifetime>` (default 6 h) with
   `InstanceInitiatedShutdownBehavior=terminate`, so a worker always dies even
   if the master is gone.
6. The worker token is written only to a root-only env file
   (`/etc/goload/worker.env`) through a quoted heredoc. It is never put on a
   command line, never logged, and `LaunchRequest` redacts it in
   `fmt`/`slog` output. All values placed into the script are validated, which
   prevents shell injection.

## 2. Deploy step by step

### 2.1 Prerequisites

- An AWS account (use a separate account for load testing if you can), the
  AWS CLI v2, Terraform >= 1.6 and Docker with buildx.
- An existing VPC with **public subnets** in two or three AZs. Workers need a
  public IPv4 address (auto-assign, or an IGW route) so that load does not flow
  through a NAT gateway. NAT charges per GB processed, in both directions; see
  the cost section.
- The pre-flight checklist in section 4 is done.

### 2.2 Build and push the image

```bash
REGION=eu-central-1
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
REPO=$ACCOUNT.dkr.ecr.$REGION.amazonaws.com/goloadtest
VERSION=$(git describe --tags --always)

aws ecr create-repository --region $REGION --repository-name goloadtest \
  --image-scanning-configuration scanOnPush=true --image-tag-mutability IMMUTABLE
aws ecr get-login-password --region $REGION | docker login --username AWS --password-stdin ${REPO%%/*}

docker buildx build --platform linux/amd64 --build-arg VERSION=$VERSION \
  -t $REPO:$VERSION --push .
```

For Graviton workers (`architecture = "arm64"`, `c7g.large`, …) build
`--platform linux/amd64,linux/arm64`.

### 2.3 Apply Terraform

```bash
cd deploy/aws
cp terraform.tfvars.example terraform.tfvars   # edit: VPC, subnets, admin CIDR, image
terraform init
terraform plan -out tfplan
terraform apply tfplan
```

Variables are documented in `deploy/aws/variables.tf`. These are the
important ones:

| Variable | Default | Notes |
|---|---|---|
| `vpc_id`, `master_subnet_id`, `worker_subnet_ids` | – | Existing network. No VPC is created. |
| `admin_cidrs` | – | Who can reach `:8080`. `0.0.0.0/0` is rejected. |
| `image`, `ecr_repository_arn` | – / null | Container image; when the ARN is set, both roles get ECR pull access. |
| `master_instance_type` | `t3.small` | Master (UI, API, coordinator, SQLite). |
| `worker_instance_types` | `c7i.large, c6i.large, c5.large` | Fallback order. IAM only allows these types. |
| `spot_only`, `spot_max_price` | `false`, `""` | An empty max price means the on-demand price is the cap. |
| `worker_egress_cidrs` | `0.0.0.0/0` | Narrow this to add a network-level target allowlist. Workers still need registry, OS and SSM endpoints. |
| `master_termination_protection` | `true` | Protects the database on the master. |
| `master_associate_public_ip` | `null` | Auto-assigned public IPv4 for the master at launch; `null` follows the subnet's auto-assign setting. See below. |

**Public IPs and private subnets.** The master needs outbound internet
access (image pulls, the Docker Compose download, SSM, the EC2 API). By
default (`master_associate_public_ip = null`) it gets a public IPv4 address if
`master_subnet_id` auto-assigns one, as before; `master_public_ip` adds the
Elastic IP on top. To keep the master off the internet entirely:

1. put it in a private subnet whose route table sends `0.0.0.0/0` to a NAT
   gateway (or give it VPC interface endpoints for `ssm`, `ssmmessages`,
   `ec2messages`, `ec2`, `ecr.api`, `ecr.dkr`, plus the S3 gateway endpoint),
2. set `master_associate_public_ip = false` and `master_public_ip = false`,
3. reach the UI through SSM port forwarding (section 2.4).

Changing `master_associate_public_ip` on an existing deployment replaces the
master instance. Workers take their public IPv4 address from the worker
subnets' auto-assign setting (they accept no inbound traffic). Workers in
private subnets behind a NAT also work, but every request and response then
passes through the NAT and is billed per GB (see the cost section).

What gets created: two security groups (the master accepts `8080` from
`admin_cidrs` and `9090` from the worker SG only; workers accept **no
inbound** traffic), master and worker IAM roles and instance profiles, SSM
SecureString parameters for the API token and worker token, the
worker launch template (Amazon Linux 2023, IMDSv2, encrypted gp3), and the
master instance with an Elastic IP.

The master role can only:

- `ec2:RunInstances`, from the goload launch template, into the listed
  subnets, with the allowed instance types, and only when the request tags
  the instance/volume `goload:role=worker`
- `ec2:CreateTags`, only as part of `RunInstances`
- `ec2:TerminateInstances`, only on instances tagged `goload:role=worker`
- `ec2:Describe*` (read-only), `iam:PassRole` for the worker role only, and
  read its own three SSM parameters

### 2.4 First login

```bash
terraform output master_url
aws ssm get-parameter --with-decryption --name "$(terraform output -raw api_token_ssm_parameter)" \
  --query Parameter.Value --output text
```

Port `8080` is **plain HTTP**, so the bearer token crosses the network
unencrypted. Use one of these before relying on it:

- SSM port forwarding, with no public exposure at all (set
  `master_public_ip = false`):
  ```bash
  aws ssm start-session --target "$(terraform output -raw master_instance_id)" \
    --document-name AWS-StartPortForwardingSession \
    --parameters '{"portNumber":["8080"],"localPortNumber":["8080"]}'
  # then open http://localhost:8080
  ```
- or an ALB with an ACM certificate, or a TLS reverse proxy such as Caddy on
  the master.

Boot logs on the master are in `/var/log/cloud-init-output.log`. Open a shell
with `aws ssm start-session --target <id>`; no SSH key or port 22 is needed.

### 2.5 Smoke-test a worker by hand

The launch template's own user data reads the master address and the worker
token from SSM. You can therefore start a single worker manually and watch it
connect:

```bash
aws ec2 run-instances --launch-template LaunchTemplateId=$(terraform output -raw launch_template_id) \
  --instance-market-options 'MarketType=spot,SpotOptions={SpotInstanceType=one-time,InstanceInterruptionBehavior=terminate}'
```

Workers launched by the master get the user data generated in
`internal/provision/userdata.go` instead (with `goload:run-id=<run>`). The two
scripts must stay in sync.

### 2.6 Database and backups

The master keeps everything (tests, CSV files, runs, metrics, error groups) in
`/data/goload.db` inside the `goload-data` Docker volume, i.e. under
`/var/lib/docker/volumes/goload_goload-data/_data/` on the root EBS volume.
Options, from simplest:

- **EBS snapshots** of the master's root volume (e.g. a daily Amazon Data
  Lifecycle Manager policy). SQLite in WAL mode recovers cleanly from a
  crash-consistent snapshot.
- **File copy while stopped:** `docker compose stop master`, copy `goload.db`
  out of the volume, `docker compose start master`.
- **Online copy** with the `sqlite3` CLI on the host
  (`sudo dnf install -y sqlite`):
  `sudo sqlite3 /var/lib/docker/volumes/goload_goload-data/_data/goload.db ".backup /root/goload-backup.db"`.

## 3. Cost model

> All prices are **approximate list prices (us-east-1 / eu-central-1, Linux,
> late 2025)** for orientation only. Check them against the current
> [EC2 pricing](https://aws.amazon.com/ec2/pricing/) and the
> [AWS Pricing Calculator](https://calculator.aws/) for your region before
> budgeting.

### Fixed cost (always on): roughly 30-60 USD/month

| Item | Approx. monthly |
|---|---|
| Master `t3.small` on-demand (≈ 0.021-0.024 USD/h) | ≈ 15-18 USD |
| 30 GiB gp3 root volume (≈ 0.08-0.10 USD/GB-month) | ≈ 2.5-3 USD |
| Elastic IP / public IPv4 (≈ 0.005 USD/h) | ≈ 3.6 USD |
| EBS snapshots, SSM, CloudWatch basics, ECR storage | ≈ 2-5 USD |
| **Total, t3.small** | **≈ 25-30 USD** |
| `t3.medium` instead (bigger UI load / many concurrent runs) | ≈ 45-60 USD |

The database is a SQLite file on the root volume, so it costs nothing extra.
Moving to RDS instead adds roughly 15-30 USD/month for the smallest instance
classes.

### Per test

Workers are billed per second (60 s minimum) only while they exist:

```
compute ≈ N_workers × (T_run + T_boot) / 3600 × (P_spot + P_ipv4)
egress  ≈ RPS × T_run × S_request / 1e9 × P_egress
nat     ≈ RPS × T_run × (S_request + S_response) / 1e9 × P_nat     # only if workers sit behind NAT
total   ≈ compute + egress (+ nat) (+ cross-AZ/region transfer if the target is in AWS)
```

| Symbol | Meaning | Approximate value (verify) |
|---|---|---|
| `T_boot` | Boot, Docker install, image pull | ≈ 2-5 min per worker |
| `P_spot` | `c7i.large` spot | often ≈ 0.03-0.05 USD/h (on-demand ≈ 0.09 USD/h); varies by AZ and time |
| `P_ipv4` | public IPv4 per worker | ≈ 0.005 USD/h |
| `P_egress` | internet egress, first tier | ≈ 0.09 USD/GB, with a small monthly free allowance |
| `P_nat` | NAT gateway data processing | ≈ 0.045 USD/GB, plus ≈ 0.045 USD/h per NAT |

**Egress traffic is the hidden cost.** Compute for a test is usually cents to
a few dollars. Bytes sent to the internet are billed per GB, and if workers
are behind a NAT gateway, **every byte in both directions** (including the
responses) is billed again as NAT processing.

#### Worked example (approximate)

10 × `c7i.large` spot at 0.04 USD/h, a 30-minute run plus 5 minutes of boot,
20 000 RPS in total, 1 KB requests and 20 KB responses, with the target on the
internet:

```
compute = 10 × (35/60) h × (0.04 + 0.005) USD/h          ≈ 0.26 USD
requests = 20 000 × 1 800 s                               = 36 M
egress  = 36 M × 1 KB  = 36 GB  × 0.09 USD/GB             ≈ 3.24 USD
inbound = 36 M × 20 KB = 720 GB (inbound over IGW: free)
--------------------------------------------------------------------------
total (public subnets)                                    ≈ 3.5 USD
if behind NAT: (36 + 720) GB × 0.045 USD/GB               ≈ +34 USD
```

Upload-heavy scenarios (large POST bodies) move the main cost into the egress
term. Always estimate `RPS × S_request` before a big run.

## 4. Pre-flight checklist

Go through this before **every** new target or significantly larger test.

- [ ] **Written permission from the target owner.** Only send load to systems
      you own or are explicitly authorised to test, and agree on a time window
      and an emergency contact on both sides.
- [ ] **AWS network stress test policy.** Read the current policy at
      <https://aws.amazon.com/ec2/testing/>. Large tests that exceed AWS's
      sustained bandwidth or packet-rate threshold need advance notification
      or approval through the process described there. **Check the current
      threshold and process in that document; do not rely on a number copied
      from elsewhere.** A load test is not a DDoS simulation: DDoS tests have
      their own separate AWS policy and are not supported by this tool.
- [ ] **Target allowlist.** The target hosts are on goload's application
      allowlist. Optionally also narrow `worker_egress_cidrs` to the target
      ranges (keep registry, OS repository and AWS endpoints reachable, or use
      VPC endpoints).
- [ ] **WAF, rate limits and IP allowlisting with the target team.** Workers
      get changing public IPs. Either agree that the target's WAF, CDN or rate
      limiter will allow the test traffic (for example by a header the scenario
      sends, or by temporarily allowing the AWS region's ranges), or assign
      Elastic IPs. Otherwise you measure the WAF instead of the system.
- [ ] **Spot vCPU quota.** Check *All Standard (A, C, D, H, I, M, R, T, Z) Spot
      Instance Requests* (in vCPUs) in Service Quotas for the region:
      `aws service-quotas list-service-quotas --service-code ec2 --query "Quotas[?contains(QuotaName,'Spot')]"`.
      N × `c7i.large` needs N × 2 vCPUs. New accounts often have low limits,
      and increases can take days.
- [ ] **On-demand vCPU quota** (*Running On-Demand Standard … instances*), if
      on-demand fallback is enabled.
- [ ] **Cost estimate** from section 3. Set an AWS Budget alert on the account.
- [ ] **Stop button.** Know how to stop the run in the UI and how to terminate
      every worker by tag (see section 5) if the master is unreachable.
- [ ] **Monitoring on the target side** is ready (dashboards, on-call aware).
- [ ] **Load generators are not the bottleneck.** Watch the worker CPU warning
      in the run report and add workers rather than overloading them.

## 5. Teardown

Terminate workers first. They normally die at the end of the run, after
`TerminateStale`, or after `MaxLifetime`, but check anyway:

```bash
IDS=$(aws ec2 describe-instances \
  --filters Name=tag:goload:role,Values=worker Name=instance-state-name,Values=pending,running,stopping,stopped \
  --query 'Reservations[].Instances[].InstanceId' --output text)
[ -n "$IDS" ] && aws ec2 terminate-instances --instance-ids $IDS
```

Then remove the stack:

```bash
cd deploy/aws
# 1. Back up the database if you need it (on the master; the runtime image
#    has no shell, so copy the file from the volume while the master is stopped):
#    cd /opt/goload && sudo docker compose stop master
#    sudo cp /var/lib/docker/volumes/goload_goload-data/_data/goload.db ~/goload-backup.db
#    sudo docker compose start master
# 2. Allow termination of the master:
terraform apply -var master_termination_protection=false
# 3. Destroy:
terraform destroy
```

The master's root volume uses `delete_on_termination = false` so that the
SQLite database survives an accidental replacement. Delete it by hand after
`destroy`:

```bash
aws ec2 describe-volumes --filters Name=tag:Name,Values=goload-master-root \
  --query 'Volumes[].VolumeId' --output text   # then: aws ec2 delete-volume --volume-id ...
```

Finally, delete the ECR repository (`aws ecr delete-repository --force
--repository-name goloadtest`) and any Terraform state bucket you no longer
need.

## 6. Security notes and known limitations

- The worker token is part of the user data of workers that the master
  launches. Anyone with `ec2:DescribeInstanceAttribute` in the account can read
  it, so rotate it (update the SSM parameter and restart the master) after
  sharing access. Manual launches from the template fetch it from SSM instead.
  A future hardening step is to have every worker fetch it from SSM.
- Generated secrets also live in Terraform state, so keep state remote,
  encrypted and access-controlled.
- Changes to the master's user data do **not** re-run on an existing instance
  (`ignore_changes`). Edit `/opt/goload/.env` on the box, or replace the
  instance deliberately.
- The Terraform has not been validated with `terraform validate` or applied
  yet, and IAM condition keys (`ec2:LaunchTemplate`, `aws:RequestTag/...`)
  should be checked with a dry run (`aws ec2 run-instances --dry-run` under the
  master role) during the first deployment.
