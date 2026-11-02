-- +goose Up
-- status_epoch counts the status changes of a connection. A run records the
-- epoch at its start. Only a reconciliation that started in the current
-- epoch ends a resume, so that a run from before a pause or a scope change
-- does not show the content again.
ALTER TABLE connections ADD COLUMN status_epoch bigint NOT NULL DEFAULT 0;
ALTER TABLE sync_runs ADD COLUMN status_epoch bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sync_runs DROP COLUMN status_epoch;
ALTER TABLE connections DROP COLUMN status_epoch;
