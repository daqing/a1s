# Deploying A1s on Tencent Cloud

This guide deploys A1s on [Tencent Cloud](https://www.tencentcloud.com/) in
two shapes:

- **Topology A — single node**: one Lighthouse or CVM instance runs
  PostgreSQL, containerd and every A1s process. Fastest way to evaluate.
- **Topology B — multi-node HA (recommended)**: one control-plane VM runs
  the api/scheduler/monitor (optionally two of each), workers run on N
  worker VMs, and the state lives in TencentDB for PostgreSQL.

The worker↔API traffic stays inside the VPC (内网): no public exposure, no
load balancer required. If you do not read Chinese, the console terms used
below are: CVM = 云服务器, Lighthouse = 轻量应用服务器, TencentDB = 云数据库,
security group = 安全组, VPC = 私有网络.

## Prerequisites

- One or more Ubuntu 22.04/24.04 instances (CVM or Lighthouse), same region
  and VPC.
  - Topology A: 1 × 2 vCPU / 4 GiB is enough for evaluation.
  - Topology B: 1 × control plane (2 vCPU / 4 GiB) + N × workers
    (2 vCPU / 4 GiB scales to a handful of light containers each).
- Go 1.27+ on your build machine (or build in CI and ship the binary).
- Outbound internet from the instances (for apt and image pulls), or a
  pre-seeded registry (see [Registry notes](#registry-notes)).

## Security groups

| Direction | Port | Source / Target | Purpose |
| --- | --- | --- | --- |
| Inbound | 22 | your IP | SSH administration |
| Inbound | 1905 | VPC subnet (e.g. 10.0.0.0/16) | A1s API — **never public** |
| Inbound | 5432 | VPC subnet | PostgreSQL (only when self-hosted; TencentDB manages its own) |

All other inbound ports stay closed. The workers only ever talk to the API
over the VPC.

## Topology A — single node

```sh
# system packages: native containerd (no Docker needed) and PostgreSQL
sudo apt-get update
sudo apt-get install -y containerd postgresql

sudo systemctl enable --now containerd postgresql
sudo -u postgres psql -c "CREATE ROLE a1s LOGIN PASSWORD 'CHANGE_ME' SUPERUSER"
sudo -u postgres psql -c "CREATE DATABASE a1s OWNER a1s"

# build locally on your machine, then copy
#   GOOS=linux GOARCH=amd64 go build -o bin/a1s-linux .
scp bin/a1s-linux ubuntu@<instance-ip>:a1s

# migrations (run on the server; the DSN is inline)
ssh ubuntu@<instance-ip> \
  "A1S_DSN='postgres://a1s:CHANGE_ME@127.0.0.1:5432/a1s?sslmode=disable' ./a1s db:migrate"
```

Run the processes under systemd (units below) and skip the worker-specific
environment — on a single node everything is local.

## Topology B — multi-node HA

### 1. Database: TencentDB for PostgreSQL (or native)

The managed option removes backup/patching work: purchase a TencentDB for
PostgreSQL instance in the same VPC, note its private address (for example
`postgres-xxxx.sql.tencentcdb.com:5432`) and the `a1s` account, and put the
address into the DSN on every node. Self-hosting PostgreSQL on the control
plane works too (same commands as Topology A); use `pg_dump` cron backups
in that case.

### 2. Control plane VM: api + scheduler + monitor

```sh
sudo apt-get update && sudo apt-get install -y curl
sudo useradd -r -s /usr/sbin/nologin a1s

scp bin/a1s-linux ubuntu@<control-plane-ip>:a1s
```

Create `/etc/a1s/a1s.env`:

```sh
A1S_DSN=postgres://a1s:CHANGE_ME@<tencentdb-host>:5432/a1s?sslmode=require
A1S_INTERNAL_TOKEN=<openssl rand -hex 32>
```

Create the systemd units — one per process, all reading the same env file.
`/etc/systemd/system/a1s-api.service`:

```ini
[Unit]
Description=A1s API
After=network-online.target
Wants=network-online.target

[Service]
User=a1s
EnvironmentFile=/etc/a1s/a1s.env
Environment=AIRWAY_ENV=production
Environment=AIRWAY_PORT=1905
ExecStart=/usr/local/bin/a1s api
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
```

`a1s-scheduler.service` and `a1s-monitor.service` are identical except for
the `ExecStart` subcommand (`scheduler` / `monitor`) and the extra
`Environment=A1S_HEARTBEAT_TIMEOUT=15s` line on the monitor. The
`Restart=always` pair matters: every instance of every process is safe to
kill and restart at any time.

```sh
sudo cp a1s /usr/local/bin/a1s && sudo chmod +x /usr/local/bin/a1s
sudo systemctl daemon-reload
sudo systemctl enable --now a1s-api a1s-scheduler a1s-monitor
curl -s http://127.0.0.1:1905/health   # → UP
```

For extra control-plane resilience, clone this VM and put both behind a
round-robin DNS record or a CLB listener — every A1s write is a
version-guarded transition, so two instances of each process never
conflict.

### 3. Worker VMs: worker + containerd

On each worker VM:

```sh
sudo apt-get update && sudo apt-get install -y containerd
sudo systemctl enable --now containerd

sudo useradd -r -s /usr/sbin/nologin a1s
scp bin/a1s-linux ubuntu@<worker-ip>:a1s
sudo cp a1s /usr/local/bin/a1s && sudo chmod +x /usr/local/bin/a1s
```

Create `/etc/a1s/a1s.env` (point `A1S_API_URL` at the control plane's
**private** address):

```sh
A1S_API_URL=http://<control-plane-private-ip>:1905
A1S_INTERNAL_TOKEN=<same token as the control plane>
A1S_CONTAINERD_ADDR=/run/containerd/containerd.sock
A1S_CONTAINERD_SNAPSHOTTER=overlayfs
```

`/etc/systemd/system/a1s-worker.service`:

```ini
[Unit]
Description=A1s Worker Agent
After=containerd.service network-online.target
Requires=containerd.service

[Service]
User=a1s
EnvironmentFile=/etc/a1s/a1s.env
ExecStart=/usr/local/bin/a1s worker --name %H
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
```

`%H` expands to the hostname, which gives every worker a unique
registration name for free.

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now a1s-worker
```

### 4. Verify

From any node (or your machine over SSH):

```sh
A1S_API_URL=http://<control-plane-private-ip>:1905 A1S_INTERNAL_TOKEN=<token> \
  a1s run nginx --name demo --restart-policy on-failure
a1s ps                     # → demo running on one of the workers
a1s stats                  # cluster summary from the database
```

Kill a worker VM in the console: within `A1S_HEARTBEAT_TIMEOUT` plus one
monitor interval its containers are running on another worker. Kill a
container with `ctr -n a1s tasks kill -s KILL demo`: the restart policy
brings it back. Both scenarios are what `scripts/e2e-chaos.sh` and
`scripts/soak.sh` assert continuously.

### 5. Scaling

- **Add a worker**: provision another VM, repeat step 3 with a fresh
  hostname. The scheduler picks it up on the next round; no control-plane
  change needed.
- **Scale the control plane**: clone the control-plane VM and let both
  instances share the same DSN and token; point half of the workers'
  `A1S_API_URL` at the second VM.

### 6. Upgrades

Replace `/usr/local/bin/a1s` with the new binary and restart the units.
Order does not matter: control-plane processes are stateless and disposable,
and a worker restart re-registers itself even after being marked lost.

## Registry notes

Workers pull images as the containerd client, so registry access is a
concern of the worker VM's network:

- From CVM/Lighthouse, `registry-1.docker.io` is often slow or unreachable.
  The in-VPC mirror `mirror.ccs.tencentyun.com` serves Docker Hub images
  without authentication. Configure containerd's declarative registry
  config on the worker:

  ```sh
  sudo mkdir -p /etc/containerd/certs.d/docker.io
  sudo tee /etc/containerd/certs.d/docker.io/hosts.toml >/dev/null <<'EOF'
  server = "https://registry-1.docker.io"

  [host."https://mirror.ccs.tencentyun.com"]
    capabilities = ["pull", "resolve"]
  EOF
  ```

  and enable the config path in `/etc/containerd/config.toml`:

  ```toml
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
  ```

  then `sudo systemctl restart containerd`.
- For private images, use Tencent Container Registry (TCR): push the A1s
  fat image (`docker build` from the repository, see the Dockerfile) to a
  TCR namespace and pull it on the workers, or simply keep shipping the
  static binary — A1s does not require containers for itself.

## Operational notes

- Every process logs JSON lines with a `role` field (`journalctl -u
  a1s-api -o cat`); multi-node runs filter by role and instance.
- `a1s stats` reads the database directly — run it from any node with the
  DSN exported.
- All state lives in the database: back up the TencentDB instance (or run
  `pg_dump` on the control plane) and the whole cluster is recoverable;
  workers and control-plane VMs are cattle.
- The internal API (worker endpoints) requires `A1S_INTERNAL_TOKEN`; the
  public API is still unauthenticated by design — keep the security group
  closed or put your own gateway in front of port 1905 before exposing it
  beyond the VPC.
