-- +goose Up
-- A pause stops the sync and reconcile runs of a connection. Its content
-- stays visible, so no status hides content and the resuming status and
-- the status epoch go.
UPDATE connections SET status = 'active', status_reason = '' WHERE status = 'resuming';
ALTER TABLE connections DROP CONSTRAINT connections_status_check;
ALTER TABLE connections ADD CONSTRAINT connections_status_check CHECK (status IN ('active', 'paused'));
ALTER TABLE sync_runs DROP COLUMN status_epoch;
ALTER TABLE connections DROP COLUMN status_epoch;

-- +goose Down
ALTER TABLE connections ADD COLUMN status_epoch bigint NOT NULL DEFAULT 0;
ALTER TABLE sync_runs ADD COLUMN status_epoch bigint NOT NULL DEFAULT 0;
ALTER TABLE connections DROP CONSTRAINT connections_status_check;
ALTER TABLE connections ADD CONSTRAINT connections_status_check CHECK (status IN ('active', 'paused', 'resuming'));
