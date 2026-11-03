-- +goose Up
-- A lease marks a passage that a job is extracting now, so that a second
-- job does not call the model for it at the same time.
CREATE TABLE passage_leases (
    item_id bigint NOT NULL REFERENCES source_items (id) ON DELETE CASCADE,
    passage_hash bytea NOT NULL,
    extractor_version text NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (item_id, passage_hash, extractor_version)
);
-- Extraction reuses the result of the same passage text in another item.
CREATE INDEX passage_extractions_hash ON passage_extractions (passage_hash, extractor_version);

-- +goose Down
DROP INDEX passage_extractions_hash;
DROP TABLE passage_leases;
