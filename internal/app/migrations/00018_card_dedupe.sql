-- +goose Up
-- A card stands for one question. dedupe_key names the question, and only
-- one open card holds each key. Resolve fills the keys of the open cards
-- that exist. closed is the status of a card whose question no longer
-- applies; it has no reviewer and no decision outcome.
ALTER TABLE proposals ADD COLUMN dedupe_key text;
ALTER TABLE proposals DROP CONSTRAINT proposals_status_check;
ALTER TABLE proposals ADD CONSTRAINT proposals_status_check
    CHECK (status IN ('open', 'accepted', 'rejected', 'needs_evidence', 'closed'));
CREATE UNIQUE INDEX proposals_open_key ON proposals (kind, dedupe_key) WHERE status = 'open' AND dedupe_key IS NOT NULL;
CREATE INDEX proposals_key ON proposals (kind, dedupe_key text_pattern_ops) WHERE dedupe_key IS NOT NULL;

-- Resolution moves to its own queue with one worker.
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('river_job') IS NULL THEN
        RETURN;
    END IF;
    UPDATE river_job SET queue = 'resolve'
    WHERE kind = 'resolve' AND queue = 'default' AND state IN ('available', 'scheduled', 'retryable', 'pending');
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('river_job') IS NULL THEN
        RETURN;
    END IF;
    UPDATE river_job SET queue = 'default'
    WHERE kind = 'resolve' AND queue = 'resolve' AND state IN ('available', 'scheduled', 'retryable', 'pending');
END
$$;
-- +goose StatementEnd
UPDATE proposals SET status = 'rejected' WHERE status = 'closed';
DROP INDEX proposals_key;
DROP INDEX proposals_open_key;
ALTER TABLE proposals DROP CONSTRAINT proposals_status_check;
ALTER TABLE proposals ADD CONSTRAINT proposals_status_check CHECK (status IN ('open', 'accepted', 'rejected', 'needs_evidence'));
ALTER TABLE proposals DROP COLUMN dedupe_key;
