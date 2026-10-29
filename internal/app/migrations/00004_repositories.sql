-- +goose Up
-- The revision of an item in its source, such as the blob ID of a
-- repository file. Sync fetches a file only when its revision changes.
ALTER TABLE source_items ADD COLUMN revision text NOT NULL DEFAULT '';

-- The source type of evidence. Source priority per predicate orders
-- evidence by it. One repository connection gives catalog, codeowners and
-- repository evidence.
ALTER TABLE claim_evidence ADD COLUMN source_type text NOT NULL DEFAULT 'repository'
    CHECK (source_type IN ('registry', 'catalog', 'codeowners', 'jira', 'confluence', 'repository', 'review'));
UPDATE claim_evidence e SET source_type = c.kind
FROM connections c
WHERE c.id = e.connection_id AND c.kind IN ('registry', 'jira', 'confluence');
ALTER TABLE claim_evidence ALTER COLUMN source_type DROP DEFAULT;

-- +goose Down
ALTER TABLE claim_evidence DROP COLUMN source_type;
ALTER TABLE source_items DROP COLUMN revision;
