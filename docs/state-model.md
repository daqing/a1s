# A1s State Model

All system state lives in PostgreSQL; control-plane processes (API,
scheduler, monitor) and worker agents hold nothing authoritative in memory.
This document defines the status enums, the legal transitions between them,
which component performs each transition, and the locking discipline that
makes concurrent processes safe.

Status values mirror the CHECK constraints in `db/migrate/` exactly. If this
document and a migration disagree, the migration wins and the document must
be fixed.

## Locking discipline

Every mutable table carries a `version BIGINT NOT NULL DEFAULT 0` column.
Every state transition — without exception — goes through
`models.UpdateWhereVersion` (`app/models/version.go`):

```sql
UPDATE <table>
SET ... , version = version + 1, updated_at = now()
WHERE id = $1 AND version = $2
```

- Rows-affected = 0 means the row is gone or its version moved on since it
  was read: the caller gets `models.ErrVersionConflict` and must re-read the
  row and re-evaluate its decision. It must never blind-retry.
- What a conflicted actor does is its own policy:
  - **scheduler**: skip — another scheduler instance won the assignment
    (Phase 4).
  - **monitor**: re-check the triggering condition (heartbeat age, status)
    against the fresh row and retry only if it still holds (Phase 5).
  - **API**: surface the conflict to the caller as an HTTP 409 (Phase 2).
- A proof that concurrent versioned updates yield exactly one winner lives
  in `app/models/version_test.go`
  (`TestUpdateWhereVersionConcurrentSingleWinner`, runs when `A1S_TEST_DSN`
  is set).

## Workers

| Status    | Meaning                                             |
| --------- | --------------------------------------------------- |
| `active`  | Registered; heartbeat is fresh.                     |
| `lost`    | Heartbeat timeout exceeded; presumed unreachable.   |

Transitions:

| From → To       | Actor    | When                                                                 |
| --------------- | -------- | -------------------------------------------------------------------- |
| — → `active`    | worker   | Registers itself on boot (heartbeat upsert, Phase 3).                 |
| `active` → `lost` | monitor | `last_heartbeat_at` older than the heartbeat timeout (Phase 5).      |
| `lost` → `active` | worker  | Only by explicit re-registration: the worker process restarting and heartbeating again flips its own row back (Phase 5 takeover semantics — a lost worker never silently rejoins). |

A `lost` worker's row is never deleted; `workers.name` is unique.

## Containers

| Status      | Meaning                                                              |
| ----------- | -------------------------------------------------------------------- |
| `pending`   | Desired state exists; not assigned to any worker (`worker_id NULL`). |
| `scheduled` | Assigned to a worker; the start command has not been confirmed yet.  |
| `running`   | The worker reports the containerd task as running.                    |
| `stopped`   | Stopped on request (or by policy); the worker confirmed the exit.     |
| `failed`    | The worker reported a start failure or an unexpected non-zero exit.   |
| `lost`      | Reserved for reconciliation (Phase 6): the runtime cannot account for the container — no worker reports it and no exit was reported. Phase 5 auto-migration does **not** route containers through `lost`. |

Transitions (actor in parentheses):

| From → To                 | Actor / Phase                  | Notes                                                                 |
| ------------------------- | ------------------------------ | --------------------------------------------------------------------- |
| — → `pending`             | API (Phase 2)                  | `run` inserts with `worker_id NULL`.                                   |
| `pending` → `scheduled`   | scheduler (Phase 4)            | Version-guarded CAS also sets `worker_id`; a conflict means another scheduler won — skip. |
| `scheduled` → `running`   | worker report (Phase 3/4)      | Worker started the container successfully.                             |
| `scheduled` → `failed`    | worker report (Phase 4)        | Start command failed.                                                  |
| `running` → `stopped`     | API desired state (Phase 2) + worker confirmation | `stop` records the desired state; the worker acts and reports. |
| `running` → `failed`      | worker report (Phase 3/6)      | Unexpected non-zero exit.                                              |
| `scheduled`/`running` → `pending` | monitor (Phase 5)      | Auto-migration: the owning worker was marked `lost`; `worker_id` resets to NULL so the scheduler reschedules. Only runtime-bearing states migrate — a `stopped` or `failed` container on a lost worker keeps its desired state and must not be restarted by the takeover. |
| `failed` → `pending`      | monitor (Phase 5)              | Auto-restart, only when the `restart_policy` calls for it.             |
| `*` → `lost`              | reconciliation (Phase 6)       | Runtime unaccountable; reconciliation decides cleanup or requeue.      |

`worker_id` references `workers.id` and is nullable; it is set only by the
scheduler's assignment transition and cleared only by the monitor's
auto-migration transition — both through `UpdateWhereVersion`.

## Restart policies

`restart_policy` values follow Docker semantics: `no` (default),
`on-failure[:N]`, `always`, `unless-stopped`. The monitor requeues `failed`
containers to `pending` only when the policy calls for it (`no` never
restarts; `on-failure:N` stops after N consecutive failures).

`restart_count` counts consecutive auto-restarts: the monitor increments it
on each requeue, and a report of `running` resets it to zero. `stopped`
(exit 0) containers are never auto-restarted — a manual `stop` and a clean
exit are indistinguishable in the current schema, so A1s errs on the side
of not resurrecting stopped containers; distinguishing them needs a
desired-state marker (Phase 6 schema work).

## containerd state mapping

The worker's `inspect` executor maps the containerd task state onto the
container statuses above (`app/worker/runtime.go`, `mapTaskStatus`):

| containerd task state | A1s status |
| --------------------- | ---------- |
| `RUNNING`             | `running`  |
| `STOPPED`, exit 0     | `stopped`  |
| `STOPPED`, exit != 0  | `failed`   |
| `PAUSED`, `CREATED`   | `stopped`  |
| no task record        | `stopped`  |
| container missing     | (reported as `exists: false`; not an A1s status) |

`stop` keeps the container object (its task is killed and reaped); `remove`
deletes both task and container.

## Conventions

- IDs are `BIGINT GENERATED ALWAYS AS IDENTITY`; future event/exit tables
  reference rows with plain BIGINT foreign keys.
- Timestamps are `TIMESTAMPTZ`, defaults provided by the database
  (`now()`); `updated_at` is refreshed by every versioned update.
- All processes connect through the same DSN (`A1S_DSN`, see
  `.env.example`); there is no per-process database.
- Multiple instances of any process are safe: every write path is a
  version-guarded transition, and conflicts are resolved by re-reading, not
  by coordination.
