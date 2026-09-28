# A1s

A1s is a simplified container orchestration system that provides the core
high-availability features of Kubernetes — container monitoring, automatic
restart, and automatic migration of containers across workers — while being
far easier to understand and operate. Instead of Kubernetes' declarative,
YAML-based model and its steep learning curve, A1s offers a small set of
imperative, command-style operations.

## Motivation

Kubernetes is a powerful automated operations platform, but its design is
overly complex. Developers and operators must master a large and diverse set
of concepts and terminology (Pods, Deployments, Services, ReplicaSets,
Ingress, and so on) before they can be productive. The learning curve is
steep and the barrier to entry is high.

Yet the underlying need remains: backends need a system that guarantees
high availability. A1s aims to keep that essential capability — and only
that — while removing everything that makes Kubernetes hard to approach.

## Scope

A1s implements three core capabilities:

1. **Container monitoring** — track the health and status of running
   containers.
2. **Automatic restart** — restart containers that fail.
3. **Automatic migration** — when a worker node goes down, reschedule its
   containers onto surviving workers.

Out of scope: service discovery, networking/ingress, configuration
management, rolling upgrades, and the rest of the Kubernetes feature set.

## Architecture

One Go binary (`a1s`) with five subcommands, cooperating through PostgreSQL:

- **`a1s api`** — the HTTP control plane. The only write path for user
  commands; never talks to workers directly.
- **`a1s scheduler`** — assigns pending containers to workers (least-loaded
  first) and queues the start commands.
- **`a1s monitor`** — marks workers whose heartbeat timed out as lost,
  requeues their runtime containers onto survivors, and requeues failed
  containers whose restart policy calls for it.
- **`a1s worker`** — the agent on each worker node: pulls and runs
  containers through **containerd**, heartbeats, executes queued commands,
  and reports observed status back.
- **`a1s run|ps|stop|rm|workers|stats`** — the stateless CLI client, talking
  to the API over HTTP.

Design invariants:

- **All state lives in PostgreSQL.** Control-plane processes persist
  nothing locally; run any number of them behind plain round-robin.
- **Optimistic locking everywhere.** Every state transition is a
  version-guarded update, so competing processes can never double-apply
  (see `docs/state-model.md`).
- **Reconciliation** (`docs/state-model.md`): workers report their real
  container set every poll round and the API repairs drift — missing
  runtimes are re-queued, lost stops are re-issued, ghost runtimes are
  cleaned up.
- **Failure detection**: worker heartbeats; a worker whose heartbeat times
  out is declared lost and its containers migrate. The timing math lives in
  `docs/state-model.md`.
- **Runtime**: containerd through its official Go client (why, in
  `docs/architecture.md`).

## Quick start

Requirements: Go 1.27+, Docker (for PostgreSQL and containerd), curl.

The one-command way — `just cluster` boots postgres + a containerd node,
migrates, and starts api, scheduler, monitor and two workers (logs and pids
in `.cluster/`; `just cluster-down` stops them):

```sh
just cluster
```

The explicit steps, for understanding what `just cluster` does:

```sh
# 1. PostgreSQL
docker run -d --name a1s-pg -e POSTGRES_USER=a1s -e POSTGRES_PASSWORD=a1s \
  -e POSTGRES_DB=a1s -p 127.0.0.1:5432:5432 postgres:16

# 2. A containerd node (see docs/architecture.md for the details; the
#    proxy vars are only needed where registry access requires one)
docker run -d --name a1s-containerd --privileged -p 127.0.0.1:60001:60001 \
  -e HTTPS_PROXY=http://http.docker.internal:3128 \
  -e HTTP_PROXY=http://http.docker.internal:3128 \
  -e NO_PROXY=localhost,127.0.0.1 \
  alpine:latest \
  sh -c 'apk add --no-cache containerd socat runc >/dev/null && \
    (containerd >/var/log/containerd.log 2>&1 &) && sleep 2 && \
    socat TCP-LISTEN:60001,fork,reuseaddr UNIX-CONNECT:/run/containerd/containerd.sock'

# 3. Migrations
go build -o bin/a1s .
A1S_DSN='postgres://a1s:a1s@127.0.0.1:5432/a1s?sslmode=disable' go run . db:migrate

# 4. The cluster
A1S_DSN='postgres://a1s:a1s@127.0.0.1:5432/a1s?sslmode=disable' ./bin/a1s api &
A1S_DSN='postgres://a1s:a1s@127.0.0.1:5432/a1s?sslmode=disable' ./bin/a1s scheduler &
A1S_DSN='postgres://a1s:a1s@127.0.0.1:5432/a1s?sslmode=disable' ./bin/a1s monitor &
A1S_INTERNAL_TOKEN=dev-token A1S_API_URL=http://127.0.0.1:1905 \
A1S_CONTAINERD_ADDR=tcp://127.0.0.1:60001 A1S_CONTAINERD_SNAPSHOTTER=native \
  ./bin/a1s worker --name w1 &
```

> On macOS the worker must run on a Linux host sharing filesystems with its
> containerd — the two-container layout above is the simplest local shape.
> `docs/architecture.md` explains why.

Then:

```sh
export A1S_API_URL=http://127.0.0.1:1905
./bin/a1s run nginx --name web --restart-policy on-failure
./bin/a1s ps
./bin/a1s stats
./bin/a1s stop web   # by id; see ./bin/a1s ps
```

The automated end-to-end chaos acceptance — two workers, `kill -9`, policy
restarts — is one command (used by CI as well):

```sh
scripts/e2e-chaos.sh          # macOS: A1S_E2E_CONTAINER_PROXY=http://http.docker.internal:3128
```

### Without Docker at all: Lima

`scripts/vm.sh` runs the same cluster on a plain Lima VM — no Docker, no
Colima. The VM hosts containerd, PostgreSQL and three workers natively
(overlayfs works, no proxy juggling); only the control plane stays on the
host:

```sh
just vm          # provision (first start downloads the image) and start
just vm-down     # stop the processes; the VM keeps running
```

`limactl delete a1s` resets the VM entirely. See `docs/architecture.md`.

## Configuration

Every variable is documented with defaults in `.env.example`. The short
version:

| Variable | Default | Used by | Purpose |
| --- | --- | --- | --- |
| `A1S_DSN` | — | api, scheduler, monitor, stats | PostgreSQL DSN holding all state |
| `A1S_API_URL` | `http://127.0.0.1:1905` | worker, CLI | control plane endpoint |
| `A1S_INTERNAL_TOKEN` | — | api, worker | shared bearer token for the internal API; **required** |
| `AIRWAY_PORT` | `1905` | api | listen port |
| `A1S_HEARTBEAT_INTERVAL` | `5s` | worker | heartbeat cadence |
| `A1S_COMMAND_INTERVAL` | `2s` | worker | command poll / status report cadence |
| `A1S_SCHEDULER_INTERVAL` | `3s` | scheduler | assignment loop cadence |
| `A1S_HEARTBEAT_TIMEOUT` | `15s` | monitor | heartbeat age before a worker is lost |
| `A1S_MONITOR_INTERVAL` | `5s` | monitor | recovery loop cadence |
| `A1S_CONTAINERD_ADDR` | `/run/containerd/containerd.sock` | worker | containerd socket or `tcp://host:port` |
| `A1S_CONTAINERD_NAMESPACE` | `a1s` | worker | containerd namespace |
| `A1S_CONTAINERD_SNAPSHOTTER` | `overlayfs` | worker | snapshotter for pulls and containers |
| `A1S_CONTAINERD_PLATFORM` | local | worker | pull/spec platform override for cross-platform clients |

Airway framework variables (`AIRWAY_ENV`, `AIRWAY_PORT`, `URL_PREFIX`,
`STORAGE_*`) keep their framework names; see `.env.example`.

## Failure semantics

- **Control-plane processes are disposable.** Killing api, scheduler or
  monitor never touches running containers. Multiple instances of each are
  safe: every write is a version-guarded transition, conflicts resolve by
  re-reading.
- **A worker that dies** (no heartbeat for `A1S_HEARTBEAT_TIMEOUT`) is
  marked `lost`; its `scheduled`/`running` containers return to `pending`
  and the scheduler places them on survivors. Desired states (`stopped`,
  `failed`) are not restarted by the takeover. A `lost` worker rejoins only
  by restarting its agent, which re-registers it.
- **A container that dies unexpectedly** is reported `failed` by its worker
  within one status-report round; the monitor requeues it per
  `restart_policy` (`no` | `on-failure[:N]` | `always` | `unless-stopped`),
  counting consecutive failures; a `running` report resets the count.
- **Drift** (a runtime deleted behind the system's back, a lost stop
  command, a ghost container after a row deletion) is detected by the
  worker manifest reconciliation within one poll round and repaired
  automatically.

Full status machines and transition actors: `docs/state-model.md`. Wire
protocol: `docs/api.md`. Cloud deployment: `deploy/tencent-cloud.md`.

## Implementation Foundation: Airway

A1s is built entirely on top of [Airway](https://github.com/daqing/airway),
a full-stack Go framework inspired by Ruby on Rails, instead of assembling
everything from the Go standard library:

- **`lib/repo`** (generics-based repository) for all database access.
- **SQL Builder** (`lib/sql` with the `pg` dialect) for hand-written
  queries such as scheduler worker selection.
- **Schema-driven migrations** and the scaffolding CLI for schema
  management.
- **Gin-based HTTP server and CLI command system** — all processes run as
  subcommands of one binary, sharing the same code and model definitions.

Components outside Airway's coverage, notably the containerd client used by
the worker agent, use the official upstream Go libraries.

## Development

```sh
go build ./...            # build
go test ./...             # unit tests (skips DB-backed ones without A1S_TEST_DSN)
scripts/e2e-chaos.sh      # full end-to-end chaos acceptance
```

Database-backed tests use their own per-package databases derived from
`A1S_TEST_DSN` (see `internal/testdb`). Logs from every process are JSON
lines with a `role` field, so multi-process runs filter cleanly with
standard tools.

## License

[MIT](LICENSE)

---

[English](README.md) | [简体中文](README.zh-CN.md)
