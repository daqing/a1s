-- containers: desired + observed state of one user container.
--
-- ID style: BIGINT GENERATED ALWAYS AS IDENTITY (same decision as the
-- workers migration), so the future exits/events table references rows with
-- a plain BIGINT foreign key.
--
-- The status CHECK includes 'scheduled' (assigned to a worker, not yet
-- reported running) — the Phase 4 scheduler assignment is a
-- pending -> scheduled transition; 'lost' covers containers whose worker
-- vanished without a final report.
--
-- version is the optimistic-lock column shared by every mutable table.

CREATE TABLE containers (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    image          TEXT NOT NULL,
    command        TEXT NOT NULL DEFAULT '',
    args           JSONB NOT NULL DEFAULT '[]'::jsonb,
    env            JSONB NOT NULL DEFAULT '{}'::jsonb,
    status         TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'scheduled', 'running', 'stopped', 'failed', 'lost')),
    worker_id      BIGINT REFERENCES workers (id),
    restart_policy TEXT NOT NULL DEFAULT 'no',
    version        BIGINT NOT NULL DEFAULT 0,
    scheduled_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
