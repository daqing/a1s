-- commands: the control plane's queue of work items addressed to one worker.
--
-- Lifecycle: queued (waiting for the worker to poll) -> delivered (handed
-- out by GET .../commands) -> done (result reported). A command stuck in
-- delivered (worker died mid-flight) is reconciliation work for Phase 6.
--
-- version is the optimistic-lock column; container_id stays nullable so the
-- channel can carry non-container probes later.

CREATE TABLE commands (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    worker_id    BIGINT NOT NULL REFERENCES workers (id),
    container_id BIGINT REFERENCES containers (id),
    action       TEXT NOT NULL CHECK (action IN ('start', 'stop', 'remove', 'inspect')),
    payload      JSONB NOT NULL DEFAULT '{}'::jsonb,
    status       TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'delivered', 'done')),
    result       JSONB NOT NULL DEFAULT '{}'::jsonb,
    version      BIGINT NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX commands_worker_status_idx ON commands (worker_id, status);
