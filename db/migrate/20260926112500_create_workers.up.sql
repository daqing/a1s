-- workers: one row per worker node registered with the control plane.
--
-- ID style (applies to later tables too): BIGINT GENERATED ALWAYS AS IDENTITY
-- primary keys, so future event/exit tables can reference rows with a plain
-- BIGINT foreign key.
--
-- version is the optimistic-lock column: every state transition updates the
-- row with WHERE id = $1 AND version = $2 and bumps version, so competing
-- control-plane processes can never double-update a worker.

CREATE TABLE workers (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    address           TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'lost')),
    last_heartbeat_at TIMESTAMPTZ,
    version           BIGINT NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
