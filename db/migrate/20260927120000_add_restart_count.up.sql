-- restart_count tracks consecutive auto-restarts of a container, so
-- on-failure:N policies can stop after N attempts. It resets when the
-- container reports running again.

ALTER TABLE containers ADD COLUMN restart_count BIGINT NOT NULL DEFAULT 0;
