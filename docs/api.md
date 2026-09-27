# A1s REST API (v1)

The public API is the only write path for user commands. Handlers live in
`app/api`, persist desired state to PostgreSQL, and never talk to workers
directly. JSON only; timestamps are RFC 3339 UTC.

## Conventions

- **Error envelope** — every non-2xx response:

  ```json
  { "error": { "code": "not_found", "message": "container 42 not found" } }
  ```

  Codes map to HTTP status: `validation_error` → 400, `not_found` → 404,
  `conflict` → 409, `internal_error` → 500.

- **Container object** — mirrors `containers` (`docs/state-model.md`):

  ```json
  {
    "id": 1,
    "name": "web-1",
    "image": "nginx",
    "command": "",
    "args": ["-g", "daemon off;"],
    "env": { "KEY": "value" },
    "status": "pending",
    "worker_id": null,
    "restart_policy": "no",
    "version": 0,
    "scheduled_at": null,
    "created_at": "2026-09-26T12:00:00Z",
    "updated_at": "2026-09-26T12:00:00Z"
  }
  ```

- **Worker object** — mirrors `workers`:

  ```json
  {
    "id": 1,
    "name": "w1",
    "address": "10.0.0.2:9001",
    "status": "active",
    "last_heartbeat_at": "2026-09-26T12:00:05Z",
    "version": 3,
    "created_at": "2026-09-26T11:00:00Z",
    "updated_at": "2026-09-26T12:00:05Z"
  }
  ```

## Containers

### `POST /api/v1/containers`

Creates a container in `pending` status with `worker_id NULL`; the scheduler
picks it up (Phase 4). The API never talks to workers.

Request (only `image` is required):

```json
{
  "name": "web-1",
  "image": "nginx",
  "command": "",
  "args": ["-g", "daemon off;"],
  "env": { "KEY": "value" },
  "restart_policy": "no"
}
```

- `name`: when omitted, the API generates a unique name
  (`<image-slug>-<random>`).
- `restart_policy`: `no` | `on-failure` | `always` | `unless-stopped`
  (suffixes like `on-failure:3` allowed); defaults to `no`.

Responses: `201` with the container object; `400` `validation_error`
(missing/blank image, invalid restart policy, non-string env values, bad
args); `409` `conflict` (name already exists).

### `GET /api/v1/containers`

Lists containers, newest first. Optional query parameter:
`status=<pending|scheduled|running|stopped|failed|lost>`.

Responses: `200` with `{ "containers": [ container, ... ] }`; `400`
`validation_error` for an unknown status value. (Pagination arrives with the
dashboard work; the wrapper object leaves room for it.)

### `GET /api/v1/containers/:id`

Responses: `200` with the container object; `404` `not_found`.

### `POST /api/v1/containers/:id/stop`

Desired-state transition `scheduled|running → stopped` via the optimistic
lock. While no worker owns the container the row simply becomes `stopped`;
once workers exist (Phase 3+) a stop command is queued for the owning worker
and its report remains authoritative — the end state is the same.

Responses: `200` with the container object after the transition; `404`
`not_found`; `409` `conflict` (status not stoppable — e.g. `pending` — or
lost a version race; re-read and retry).

### `DELETE /api/v1/containers/:id`

Removes the row outright (no tombstone). Repeating a DELETE on the same id
returns `404` — strict idempotency is revisited in Phase 6.

Responses: `204` empty body; `404` `not_found`.

## Workers

### `GET /api/v1/workers`

Lists workers, most recently heartbeating first.

Responses: `200` with `{ "workers": [ worker, ... ] }`.

## Not covered here

Internal endpoints (`/api/v1/internal/...`: heartbeat, command channel,
status reports) are defined in Phase 3 and protected by
`A1S_INTERNAL_TOKEN`; they never appear in this public contract.
