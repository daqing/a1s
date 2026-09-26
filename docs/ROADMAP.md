# A1s Development Roadmap

This roadmap breaks the A1s project (see `README.md`) into incremental phases.
Each phase ends with a working, verifiable system — never a broken tree — so
development can pause and resume at any phase boundary. Durations are rough
guidelines for a part-time effort and should be adjusted to your pace.

For a task-by-task checklist sized to spare-time sittings, see
`docs/TASKS.md`.

Legend: `[API]` control plane API process · `[SCHED]` scheduler process ·
`[MON]` health monitor process · `[W]` worker agent · `[CLI]` stateless CLI
client commands · `[DB]` database.

---

## Phase 0 — Skeleton cleanup and command layout (1–2 weeks)

The repo is currently a stock Airway scaffold (home page, websocket, storage,
OpenAPI). Turn it into the A1s process layout before any feature work.

Steps:

1. Decide the command surface of the single binary:
   - `a1s api` — HTTP control plane API.
   - `a1s scheduler` — scheduling loop process.
   - `a1s monitor` — heartbeat/health monitor process.
   - `a1s worker` — worker agent (runs on each worker node).
   - `a1s run/stop/ps/scale/...` — stateless CLI client that talks to the API
     over HTTP.
   - Keep Airway's built-in CLI (`repl`, migrations, scaffolding) reachable.
2. Restructure `main.go` dispatch: replace the current "server by default"
   logic with explicit subcommand dispatch (keep `server`/`api` as an alias for
   the API process if convenient).
3. Trim the scaffold: remove or quarantine parts A1s will not use
   (`app/api/home_api`, `app/api/storage_api`, websocket, views/assets) — or
   leave them but stop registering them in `config/routes.go`. Keep the
   health-check route for LB probes.
4. Keep the OpenAPI route only if you plan to document the API with it from
   day one; otherwise drop it and revisit in Phase 6.
5. Decide env-var namespacing (e.g. `A1S_DSN`, `A1S_API_URL`) and update
   `.env.example`.

Acceptance: `go build ./...` passes; `a1s --help` (or equivalent) lists all
subcommands; `a1s api` boots an HTTP server with only the health route.

---

## Phase 1 — Data model and optimistic locking (2–3 weeks)

All system state lives in PostgreSQL; nothing is persisted in control plane
processes. Get the schema and the locking discipline right first — every later
phase depends on it.

Steps:

1. Write migrations (under `db/migrate`) for the core tables:
   - `workers`: id, name, address, status (`active`/`lost`), last_heartbeat_at,
     created_at/updated_at.
   - `containers`: id, name, image, command/args, env, status
     (`pending`/`running`/`stopped`/`failed`/`lost`), worker_id (nullable),
     restart_policy, version (optimistic lock), scheduled_at, timestamps.
   - Later phases may add `exits`/events table; design IDs so it is easy.
2. Define repo models in `app/models` using Airway `lib/repo` generics, with a
   `Version` field on every mutable table.
3. Implement an optimistic-locking update helper (e.g. `UpdateWhereVersion`)
   using Airway's SQL builder (`lib/sql`, `pg` dialect): every state transition
   updates `... WHERE id = ? AND version = ?` and reports rows-affected = 0 as
   a conflict, so competing scheduler instances can never double-assign a
   container.
4. Seed conventions: all processes connect via the same DSN; write a short
   `docs/state-model.md` describing each status and the legal transitions.

Acceptance: migrations run via the Airway CLI; a unit test proves that two
concurrent versioned updates on the same row yield exactly one winner.

---

## Phase 2 — API process + CLI client (3–4 weeks)

The API is the only write path for user commands; the CLI is stateless HTTP.

Steps:

1. Define the REST API (start minimal, expand later):
   - `POST /api/v1/containers` — run a container (name, image, cmd, env).
   - `GET /api/v1/containers` — list with status.
   - `GET /api/v1/containers/:id` — inspect one.
   - `POST /api/v1/containers/:id/stop` — stop.
   - `DELETE /api/v1/containers/:id` — remove.
   - `GET /api/v1/workers` — list workers (useful from day one).
2. Implement handlers in `app/api` writing desired state to PostgreSQL. A
   `run` command creates a container in `pending` status with `worker_id NULL`;
   the scheduler (Phase 4) picks it up. The API never talks to workers
   directly.
3. Implement CLI subcommands (`run`, `ps`, `stop`, `rm`, `workers`) as thin
   HTTP clients against `A1S_API_URL`, printing readable tables.
4. Validation and errors: validate image/name at the API, return consistent
   JSON errors, map them to CLI exit codes.

Acceptance: `a1s run` → row appears in PostgreSQL as `pending`; `a1s ps`
shows it; stop/remove transitions are persisted. No worker exists yet —
that is fine at this phase boundary.

---

## Phase 3 — Worker agent + heartbeats (4–6 weeks)

First contact with containerd. This is the riskiest integration, so give it
its own phase.

Steps:

1. Add the containerd client dependency (official upstream Go library; decide
   early between the high-level containerd client and raw CRI — README says
   CRI via containerd, pick one and record it in the docs).
2. Worker boot: `a1s worker --name w1` registers/creates its `workers` row,
   then starts a heartbeat loop (`POST /api/v1/internal/heartbeat` or a
   lightweight RPC) every N seconds; the monitor will later use timeouts.
3. Command channel, simplest viable design: worker long-polls (or polls every
   few seconds) the API for commands addressed to it
   (`GET /api/v1/internal/workers/:id/commands`), then reports results. This
   avoids extra infra; upgrade to a streaming protocol only if polling hurts.
4. Implement container lifecycle against containerd: create/pull image, create,
   start, stop, remove, inspect/status. Map runtime states onto the `containers`
   statuses.
5. Status reporting: worker periodically reports actual state of its
   containers to the API (`PUT /api/v1/internal/containers/:id/status`), so the
   DB reflects reality.

Acceptance (single machine is fine): start `a1s api` and `a1s worker`; the
worker heartbeats appear in the DB; pulling and starting a plain image (e.g.
`nginx`) through containerd works end to end via a manual test command or a
temporary API endpoint.

---

## Phase 4 — Scheduler (2–3 weeks)

The scheduler watches `pending` containers and assigns workers.

Steps:

1. Scheduling loop: `a1s scheduler` polls (e.g. every 2–5 s, or LISTEN/NOTIFY
   if you want instant reaction) for containers with status `pending` and
   `worker_id IS NULL`.
2. Worker selection with the SQL builder: simplest first — pick an `active`
   worker with the fewest running containers; leave hooks (resources, labels,
   spread/packing policies) for later.
3. Assignment via optimistic locking: `UPDATE containers SET worker_id=?, status='scheduled', version=version+1 WHERE id=? AND version=? AND worker_id IS NULL`.
   A conflict means another scheduler won — skip. This is the mechanism that
   makes multiple scheduler instances safe.
4. The worker picks up the assignment through its command channel and starts
   the container; on start it reports `running`; if the start fails, report
   `failed` and let the scheduler/monitor retry policy decide.

Acceptance: `a1s run nginx` with one API + one scheduler + one worker running
ends with a running container visible via `a1s ps` and `ctr`/`crictl`. Kill
and restart the scheduler mid-test: no duplicate assignments, no stuck
containers.

---

## Phase 5 — Health monitor: auto-restart and auto-migration (3–4 weeks)

The two headline features of the README.

Steps:

1. `a1s monitor` loop: mark workers whose `last_heartbeat_at` is older than
   the timeout as `lost` (optimistic-lock the transition so two monitors do
   not double-mark).
2. Auto-migration: for each container on a lost worker, reset it to `pending`
   with `worker_id NULL`; the scheduler reschedules it onto surviving workers.
   Containers on the lost node may still be running — define and implement the
   takeover semantics (the lost worker is unreachable, so the primary risk is
   split-brain if it comes back; record the decision, e.g. workers re-join only
   when explicitly re-registered).
3. Auto-restart: watch for containers in `failed` (or exited when a
   restart_policy says so) and re-queue them as `pending`.
4. Deduplicate recovery work across monitor instances with the same
   optimistic-lock pattern.
5. Configure intervals/timeouts via env vars; document the failure-detection
   timeline (heartbeat interval × timeout × poll interval).

Acceptance demo (the core scenario): two workers; run several containers;
`kill -9` the worker process (simulate node loss); within the detection window
the containers are rescheduled and running on the surviving worker; killing a
container process causes an automatic restart.

---

## Phase 6 — Reconciliation, hardening, and polish (4+ weeks)

The README lists state reconciliation as optional — treat it as the
stabilization phase.

Steps:

1. Reconciliation loop (can live inside the monitor process or standalone):
   periodically compare desired state (DB) with actual state (worker reports /
   runtime inspection) and repair drift: unexpected exits → restart policy;
   missing containers → re-queue; ghost entries → clean up.
2. Multi-instance soak: run 2× API, 2× scheduler, 2× monitor behind nothing
   more than round-robin; kill processes at random; assert no duplicate
   scheduling, no lost updates.
3. Idempotency of user commands: repeated `stop`/`run` with the same
   parameters must be safe.
4. Observability: structured logs per process, a `/metrics`-style endpoint or
   at least a `a1s stats` CLI reading from the DB, OpenAPI docs if kept.
5. E2E test script(s) (shell or Go) automating the Phase 5 demo; wire into CI.
6. Docs: rewrite `README.md` operational sections (how to run each process,
   configuration reference, failure semantics), plus the `docs/state-model.md`
   from Phase 1.
7. Packaging: Dockerfile per process or one fat image with command override;
   revisit `Procfile.dev`/justfile for a one-command local cluster
   (`just cluster` launching API + scheduler + monitor + 2 workers).

Acceptance: the full demo in step 2 runs unattended for an hour of chaos with
zero inconsistent state; README instructions work from a clean checkout.

---

## Dependency summary

```
Phase 0 (skeleton)
  └─ Phase 1 (DB schema + locking)
       └─ Phase 2 (API + CLI)
            └─ Phase 3 (worker agent) ─┐
                 └─ Phase 4 (scheduler)┘  (3 and 4 can overlap at the API contract)
                      └─ Phase 5 (monitor: restart + migration)
                           └─ Phase 6 (reconciliation, hardening, polish)
```

Estimated total: roughly 5–7 months part-time; compressible if worked on
full-time. Phases 2–4 deliver the first end-to-end "run a container" moment
early enough to keep feedback loops short.
