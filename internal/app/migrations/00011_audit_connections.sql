-- +goose Up
-- The connections whose content an audit entry names. NULL means that the
-- entry names no source content. An admin reads the target and the details
-- only when all of them are in the admin's visible set (SI-01).
ALTER TABLE audit_log ADD COLUMN connections bigint[];

-- +goose Down
ALTER TABLE audit_log DROP COLUMN connections;
