# A1s Task Breakdown

This file turns `docs/ROADMAP.md` into a flat checklist of small, independently
finishable tasks. Each task is sized for one spare-time sitting (roughly
30–120 minutes) and ends with a concrete "Done when" line — do not tick the
box until that line is true.

## How to use this list

- Work strictly in ID order within a phase; do not start a phase before the
  previous phase's acceptance task is ticked (the tree must stay working at
  every phase boundary).
- Before ticking any box: `go build ./...` and `go test ./...` must pass.
- Each phase ends with a `Phase N check` task that replays the roadmap's
  acceptance scenario. That task is the real gate, not the code writing.
- Task IDs are stable. Referencing one in a commit subject (e.g.
  `T2.3: add container inspect endpoint`) is encouraged but optional.
- If a task turns out to be too big for one sitting, stop at a green build,
  and split the remainder rather than leaving it half-done overnight.
- Where a live PostgreSQL is needed (integration tests, demos) and none is
  running, start one via Docker, e.g.
  `docker run --name a1s-pg -e POSTGRES_PASSWORD=a1s -p 5432:5432 -d postgres:16`.

## Progress overview

| Phase | Theme                              | Tasks    | Status |
| ----- | --------------------------------- | -------- | ------ |
| 0     | Skeleton cleanup and commands     | T0.1–T0.5 | 5/5 ✓  |
| 1     | Data model and optimistic locking | T1.1–T1.7 | 1/7    |
| 2     | API process + CLI client          | T2.1–T2.9 | open   |
| 3     | Worker agent + heartbeats         | T3.1–T3.8 | open   |
| 4     | Scheduler                         | T4.1–T4.5 | open   |
| 5     | Health monitor                    | T5.1–T5.6 | open   |
| 6     | Hardening and polish              | T6.1–T6.7 | open   |

---

## Phase 0 — Skeleton cleanup and command layout

- [x] **T0.1 — Subcommand dispatch in `main.go`**
  Replace the "server by default" logic with explicit dispatch: `api` (with
  `server` kept as an alias) boots today's HTTP server; `scheduler`,
  `monitor`, and `worker` exit with a clear "not implemented yet" message and
  a distinct exit code; every other argument keeps going to the Airway CLI as
  today. `--version`/`-v` behavior stays.
  *Done when:* `go build ./...` passes; `go run . api` boots the current
  server; `go run . scheduler` prints the placeholder; `go run . repl` still
  works.

- [x] **T0.2 — Trim route registration in `config/routes.go`**
  Decide and apply: stop registering `home_api`, `storage_api`, the websocket
  routes, and the OpenAPI route (`openapi_api`). Keep `health_api` (`/health`)
  for LB probes. Default per roadmap: drop OpenAPI now, revisit in Phase 6.
  Record the decision in a short comment.
  *Done when:* `go run . api` serves `/health` only; no other routes answer;
  build and tests pass.

- [x] **T0.3 — Remove dead scaffold code and boot hooks**
  Now that nothing references them, remove the unused scaffold pieces:
  `app/views`, `app/assets`, `app/websocket`, `app/api/home_api`,
  `app/api/storage_api`, `app/api/openapi_api`, the `jsbuild` dev-server boot
  in `main.go` (local mode), the templ directives in `generate.go`, and any
  Procfile/justfile entries that exist only for the frontend. Git history
  keeps everything recoverable. The admin dashboard is a separate project
  consuming the public API (see `docs/ROADMAP.md`), so the web scaffold has
  no future in this repo.
  *Done when:* `go build ./...`, `go vet ./...`, and `go test ./...` are all
  clean with no unused imports or dead packages left.

- [x] **T0.4 — Env-var namespacing**
  Fix the env contract: process-specific vars use the `A1S_` prefix
  (`A1S_DSN`, `A1S_API_URL`, later `A1S_HEARTBEAT_INTERVAL`, ...), while
  Airway-internal vars (`AIRWAY_ENV`, `AIRWAY_PORT`) keep their names. Update
  `.env.example` (create it if missing) with one commented block per process.
  *Done when:* `.env.example` documents every var the binary reads today, and
  `a1s api` boots from a `.env` containing only those vars.

- [x] **T0.5 — Phase 0 check**
  Replay the roadmap acceptance end to end on a clean checkout.
  *Done when:* `go build ./...` passes; the binary's help/usage lists
  `api`, `scheduler`, `monitor`, `worker` plus the Airway CLI commands; and
  `a1s api` boots an HTTP server answering only `/health`.

---

## Phase 1 — Data model and optimistic locking

- [x] **T1.1 — `workers` table migration**
  Write `db/migrate/<name>.up.sql` / `.down.sql` (Airway CLI applies them via
  `go run . db:migrate`): `workers` with `id`, `name` (unique), `address`,
  `status` (`active`/`lost`), `last_heartbeat_at`, `created_at`, `updated_at`,
  and a `version BIGINT NOT NULL DEFAULT 0` column for optimistic locking.
  *Done when:* `go run . db:migrate` applies cleanly against a fresh
  PostgreSQL and the down migration reverts it.

- [ ] **T1.2 — `containers` table migration**
  `containers` with `id` (choose an id style that a later `exits`/events
  table can reference; record the choice as a SQL comment), `name`, `image`,
  `command`, `args`, `env` (JSONB), `status`
  (`pending`/`running`/`stopped`/`failed`/`lost` — consider a CHECK
  constraint), `worker_id` (nullable FK to `workers`), `restart_policy`,
  `version`, `scheduled_at`, timestamps.
  *Done when:* migration up/down runs cleanly on top of T1.1.

- [ ] **T1.3 — `Worker` repo model**
  Add `app/models/worker.go` using the Airway `lib/repo` generics, with the
  `Version` field mapped, and register it for the REPL namespace (follow the
  existing `app/models/registry.go` pattern).
  *Done when:* `go run . repl` can insert, find, and list `Worker` rows
  against the migrated database.

- [ ] **T1.4 — `Container` repo model**
  Same as T1.3 for `containers`, including `Version`, the status constants as
  named Go values, and a typed `Env` map that serializes to the JSONB column.
  *Done when:* REPL round-trips a `Container` row including env and status.

- [ ] **T1.5 — Optimistic-lock update helper**
  Implement `UpdateWhereVersion` (e.g. in `app/models` or a small
  `app/repo` helper) on top of Airway's `lib/sql` pg dialect: updates
  `... WHERE id = ? AND version = ?`, increments the version, and returns a
  typed conflict error when rows-affected is 0. Reuse it for every future
  state transition.
  *Done when:* unit test shows a matching version updates the row and
  increments version, while a stale version returns the conflict error
  without touching the row.

- [ ] **T1.6 — Concurrency proof test**
  An integration test (skipped unless a test DSN env var is set) that runs
  two concurrent `UpdateWhereVersion` calls on the same row and asserts
  exactly one succeeds.
  *Done when:* the test passes against PostgreSQL (use the Docker command
  from the preamble if needed) and demonstrates exactly one winner.

- [ ] **T1.7 — `docs/state-model.md` + Phase 1 check**
  Write the state model doc: every `workers`/`containers` status, the legal
  transitions (who performs them: API, scheduler, worker, monitor), and the
  version-conflict rule. Then replay the phase acceptance.
  *Done when:* doc exists and is consistent with the migrations; migrations
  run via the Airway CLI; the T1.6 test proves single-winner updates.

---

## Phase 2 — API process + CLI client

- [ ] **T2.1 — API contract doc**
  Write `docs/api.md` defining request/response JSON for
  `POST /api/v1/containers`, `GET /api/v1/containers`,
  `GET /api/v1/containers/:id`, `POST /api/v1/containers/:id/stop`,
  `DELETE /api/v1/containers/:id`, `GET /api/v1/workers`, plus one shared
  error envelope (`{"error": {"code": ..., "message": ...}}` or similar).
  *Done when:* the doc fits on one screen per endpoint group and every later
  handler task can be written mechanically from it.

- [ ] **T2.2 — `POST /api/v1/containers` (run)**
  Handler in `app/api` (new `containers_api` package) that validates name and
  image (Airway `lib/validation`), defaults the restart policy, and inserts a
  row with status `pending` and `worker_id NULL`. The API never talks to
  workers.
  *Done when:* `curl` create returns 201 with the row JSON; invalid input
  returns the error envelope with 4xx; the row is visible in PostgreSQL.

- [ ] **T2.3 — List and inspect endpoints**
  `GET /api/v1/containers` (list with status, newest first) and
  `GET /api/v1/containers/:id` (single, 404 via the error envelope).
  *Done when:* rows created in T2.2 round-trip through both endpoints.

- [ ] **T2.4 — Stop and remove endpoints**
  `POST /api/v1/containers/:id/stop` and `DELETE /api/v1/containers/:id` as
  desired-state transitions: stop moves `running`→`stopped` (recorded, the
  worker acts later), remove deletes the row (or tombstones it — follow the
  state-model doc). Both go through `UpdateWhereVersion`.
  *Done when:* transitions are persisted with version increments; illegal
  transitions (e.g. stopping a `pending` container) return 409.

- [ ] **T2.5 — `GET /api/v1/workers`**
  List workers with status and `last_heartbeat_at`.
  *Done when:* endpoint returns the (still empty or hand-seeded) workers
  table in the documented shape.

- [ ] **T2.6 — Handler tests**
  `httptest`-based tests for every endpoint above: happy path, validation
  errors, 404s, and conflict (409) cases, backed by the same test-DSN setup
  as T1.6.
  *Done when:* `go test ./...` covers all Phase 2 handlers and passes.

- [ ] **T2.7 — CLI client core: `run` and `ps`**
  Implement `a1s run <image> [--name] [--cmd] [--env]` and `a1s ps` as thin
  HTTP clients against `A1S_API_URL` (small shared client helper in a new
  `app/cli` package), printing plain readable tables. Map connection failures
  to a clear message and non-zero exit.
  *Done when:* against a running `a1s api`, `a1s run nginx` then `a1s ps`
  shows the pending row; no API running → friendly error, exit code ≠ 0.

- [ ] **T2.8 — CLI: `stop`, `rm`, `workers` + exit codes**
  Same client helper; exit code 0 on success, 1 on API error, 2 on usage.
  *Done when:* full CRUD cycle works from the CLI alone.

- [ ] **T2.9 — Phase 2 check**
  *Done when:* with only `a1s api` running: `a1s run` creates a `pending`
  row in PostgreSQL, `a1s ps` shows it, stop/remove transitions persist, and
  `a1s workers` answers. No worker exists yet — that is expected here.

---

## Phase 3 — Worker agent + heartbeats

- [ ] **T3.1 — containerd client spike and decision**
  Decide between the high-level containerd Go client and raw CRI (roadmap
  leans CRI-via-containerd; pick one), add the dependency, and write a
  throwaway connection check (temporary subcommand or test) that lists
  namespaces from a locally running containerd.
  *Done when:* the spike connects to a real containerd (e.g. started via
  Docker or brew), prints namespaces, and the decision is recorded in
  `docs/state-model.md` or a new short `docs/architecture.md`.

- [ ] **T3.2 — Internal API surface + shared token**
  Create `/api/v1/internal/...` group protected by a bearer token
  (`A1S_INTERNAL_TOKEN`, reject with 401 when unset on either side), and add
  `POST /api/v1/internal/heartbeat` which upserts the worker row by name and
  refreshes `last_heartbeat_at` (active on first registration).
  *Done when:* curl with the token upserts and refreshes a worker row;
  without the token the endpoint 401s.

- [ ] **T3.3 — Worker boot + heartbeat loop**
  `a1s worker --name w1` (plus `A1S_API_URL`, `A1S_INTERNAL_TOKEN`): on boot
  register via heartbeat, then loop every `A1S_HEARTBEAT_INTERVAL` (default
  5 s). Log locally; keep the loop resilient to transient API errors.
  *Done when:* with `a1s api` + `a1s worker` running, `last_heartbeat_at`
  advances in the DB and `a1s workers` shows the worker as `active`.

- [ ] **T3.4 — Command channel**
  `GET /api/v1/internal/workers/:id/commands` (simple polling every few
  seconds for now) plus a result-reporting endpoint. Define the command
  vocabulary (`start`, `stop`, `remove`, `inspect`) in `docs/api.md`. The API
  side just reads queued rows; nothing generates commands until Phase 4.
  *Done when:* a hand-inserted command row is fetched by the worker and its
  reported result lands in the DB.

- [ ] **T3.5 — containerd lifecycle: pull, create, start**
  Worker-side executor for `start`: pull image (if missing), create the
  container, start it, using the client style chosen in T3.1.
  *Done when:* a hand-queued `start` command results in a running container
  verifiable with `ctr`/`crictl` on the machine.

- [ ] **T3.6 — containerd lifecycle: stop, remove, status mapping**
  Executor for `stop`, `remove`, `inspect`; map containerd task/container
  states onto the `containers` statuses from `docs/state-model.md`.
  *Done when:* stop→running exits cleanly; remove cleans up both container
  and task; inspect reports a correct mapped status for a running and a
  stopped container.

- [ ] **T3.7 — Status reporting loop**
  Worker periodically reconciles actual containerd state into the API via
  `PUT /api/v1/internal/containers/:id/status` (optimistic-locked), so the DB
  reflects reality even without commands.
  *Done when:* externally killing a container (via `ctr`) is reflected in
  the DB within one reporting interval.

- [ ] **T3.8 — Phase 3 check**
  *Done when:* on a single machine, `a1s api` + `a1s worker` run; heartbeats
  appear in the DB; pulling and starting `nginx` through containerd works
  end to end via a manually queued command or temporary debug endpoint.

---

## Phase 4 — Scheduler

- [ ] **T4.1 — Scheduler loop skeleton**
  `a1s scheduler`: poll every `A1S_SCHEDULER_INTERVAL` (default 2–5 s) for
  containers with `status='pending' AND worker_id IS NULL`; log candidates,
  assign nothing yet. Graceful behavior on zero active workers.
  *Done when:* scheduler logs pending containers it observes; `a1s run`
  followed by scheduler startup shows the row being noticed.

- [ ] **T4.2 — Worker selection query**
  Pick the `active` worker with the fewest running containers, via
  `lib/sql`. Leave the structure open for later policies (resources, labels,
  spread/packing) but implement only least-loaded now.
  *Done when:* with hand-seeded worker/container rows the query returns the
  expected worker, covered by a test.

- [ ] **T4.3 — Assignment via optimistic locking**
  `UPDATE containers SET worker_id=?, status='scheduled', version=version+1
  WHERE id=? AND version=? AND worker_id IS NULL`; a conflict means another
  scheduler instance won — skip silently. Then queue the `start` command for
  the winning worker.
  *Done when:* running two scheduler instances against the same pending row
  results in exactly one assignment and one queued command.

- [ ] **T4.4 — End-to-end start flow**
  The worker picks up the `start` command (channel from T3.4), runs it
  (T3.5), reports `running`; on start failure reports `failed`.
  *Done when:* `a1s run nginx` with api + scheduler + worker all running
  ends with a container visible as `running` in both `a1s ps` and
  `ctr`/`crictl`.

- [ ] **T4.5 — Phase 4 check (scheduler chaos)**
  *Done when:* the full flow above works, and killing + restarting the
  scheduler mid-test produces no duplicate assignments and no stuck
  containers.

---

## Phase 5 — Health monitor: auto-restart and auto-migration

- [ ] **T5.1 — Monitor loop skeleton + timing config**
  `a1s monitor` with env-configurable heartbeat timeout and poll interval;
  loop structure in place, marking nothing yet.
  *Done when:* the process runs, logs its configured timings, and is
  documented in `.env.example`.

- [ ] **T5.2 — Mark lost workers**
  Workers whose `last_heartbeat_at` is older than the timeout transition
  `active`→`lost` through `UpdateWhereVersion` so two monitor instances never
  double-mark.
  *Done when:* stopping a worker's heartbeat (kill the process) flips its row
  to `lost` within the detection window, verified in the DB.

- [ ] **T5.3 — Auto-migration**
  For each container on a lost worker, reset to `pending` with
  `worker_id NULL` (version-guarded) so the scheduler reschedules onto
  survivors. Define and record takeover semantics in `docs/state-model.md`:
  a `lost` worker may rejoin only via explicit re-registration (`a1s worker`
  restart re-registers and flips itself back to `active`).
  *Done when:* containers on a lost worker return to `pending` and get
  rescheduled by Phase 4 machinery without any manual DB edits.

- [ ] **T5.4 — Auto-restart**
  Watch for `failed` containers (and exited ones whose `restart_policy` says
  so) and re-queue them as `pending`, version-guarded.
  *Done when:* `kill`-ing a container process externally results in an
  automatic restart within the detection window, per policy.

- [ ] **T5.5 — Cross-instance dedup review**
  Audit every monitor transition for the optimistic-lock pattern; add a
  regression test that runs the recovery logic twice concurrently and
  asserts single effects.
  *Done when:* two monitor instances running simultaneously through a
  worker-loss event produce no double-migrations or double-restarts.

- [ ] **T5.6 — Phase 5 check (the core demo)**
  Document the failure-detection timeline (heartbeat interval × timeout ×
  poll interval) in `docs/state-model.md`, then run the demo.
  *Done when:* two workers; several containers running; `kill -9` one worker
  → its containers are rescheduled and running on the survivor within the
  window; externally killing a container triggers auto-restart.

---

## Phase 6 — Reconciliation, hardening, and polish

- [ ] **T6.1 — Reconciliation loop**
  Inside the monitor (or standalone): periodically compare desired state (DB)
  with actual state (worker reports / runtime inspection) and repair drift —
  unexpected exits → restart policy, missing containers → re-queue, ghost
  entries → cleanup.
  *Done when:* manually corrupting state (drop a container behind the
  system's back) is detected and repaired within one reconcile cycle.

- [ ] **T6.2 — Command idempotency**
  Repeated `stop`/`run` with identical parameters must be safe (document
  what `run` with an existing name does: reject or adopt). Cover with tests.
  *Done when:* idempotency tests pass and the semantics are documented in
  `docs/api.md`.

- [ ] **T6.3 — Observability**
  Structured (JSON) logs per process with a process-role field; plus either
  a `/metrics` endpoint or an `a1s stats` CLI reading from the DB (pick one,
  record why).
  *Done when:* a chaos run can be followed from logs/stats alone without
  touching the database.

- [ ] **T6.4 — E2E chaos script + CI**
  A shell or Go script automating the Phase 5 demo (start cluster → run
  containers → kill worker → assert recovery), wired into CI.
  *Done when:* the script runs green unattended from a clean checkout with
  Docker available.

- [ ] **T6.5 — Docs pass**
  Rewrite `README.md` operational sections: how to run each process, a
  configuration reference (every `A1S_*` var), failure semantics; refresh
  `docs/state-model.md` and `docs/api.md` to match reality.
  *Done when:* a new contributor can go from clean checkout to the Phase 5
  demo using only the README.

- [ ] **T6.6 — Packaging and one-command cluster**
  Dockerfile per process (or one fat image with command override — decide and
  record); extend the justfile with `just cluster` launching api + scheduler
  + monitor + 2 workers locally.
  *Done when:* `just cluster` brings up a working local cluster from one
  command, and the container image(s) build.

- [ ] **T6.7 — Phase 6 check: one-hour soak**
  *Done when:* the multi-instance setup (2× api, 2× scheduler, 2× monitor,
  2+ workers) survives an hour of randomized kills with zero inconsistent
  state — no duplicate scheduling, no lost updates — and the README
  instructions work from a clean checkout.
