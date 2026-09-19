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

## User Interface Philosophy

Operations are **imperative** (command-style), not declarative (YAML).
Users issue direct commands, and A1s maintains the state needed to keep the
system consistent. The CLI is itself stateless: it talks to the API process
over HTTP.

## Architecture

- **Implementation language**: Go.
- **Control plane**: not a single process, but several stateless,
  role-separated processes that cooperate through a shared database:
  - **API process** — accepts user commands (run, stop, scale, ...), writes
    state to the database.
  - **Scheduler process** — watches for unscheduled containers, decides
    which worker each lands on, and writes the decision back.
  - **Health monitor process** — tracks worker heartbeats; on timeout,
    marks the failed worker's containers for rescheduling.
  - Optionally a **state reconciliation process** that compares desired
    state with actual container state reported by workers.
- **State storage**: all system state lives in **PostgreSQL**. Control
  plane processes persist nothing locally; multiple instances of any
  process can sit behind a load balancer and read the system's state from
  the database. Any process can be killed and restarted at will, and the
  failure of a control plane process never affects containers already
  running on workers.
- **Concurrency control**: **optimistic locking** (version fields), so
  multiple stateless instances can read and write concurrently without
  conflicting decisions (e.g., two schedulers scheduling the same
  container).
- **Failure detection**: workers send **heartbeats** to the control plane
  at regular intervals; a worker whose heartbeat times out is declared
  lost, and its containers are rescheduled onto other workers.
- **Container runtime**: the same approach as Kubernetes — **containerd**
  via CRI, rather than a direct Docker API.
- **Worker agent**: a process on each worker node that manages the local
  container lifecycle (create, start, stop, inspect) through the containerd
  client, and reports status and heartbeats to the control plane.

## Implementation Foundation: Airway

A1s is built entirely on top of [Airway](https://github.com/daqing/airway),
a full-stack Go framework inspired by Ruby on Rails, instead of assembling
everything from the Go standard library:

- **`lib/repo`** (generics-based repository) for all database access.
- **SQL Builder** (`lib/sql` with the `pg` dialect) for hand-written
  queries such as scheduler worker selection.
- **Schema-driven migrations** and the scaffolding CLI for schema
  management.
- **Gin-based HTTP server and CLI command system** — the control plane's
  processes (API, scheduler, health monitor) run as subcommands of one
  binary, sharing the same code and model definitions; the worker agent
  likewise runs as a subcommand of the same binary.

Components outside Airway's coverage, notably the containerd client used by
the worker agent, use the official upstream Go libraries.

## License

[MIT](LICENSE)
