-- +goose Up
-- The hash of the title, the body and the given passages of an item. Store
-- skips the passages and chunks when it is unchanged.
ALTER TABLE source_items ADD COLUMN index_hash bytea;
-- The sha256 of the chunk text. The embed job copies the vector of a chunk
-- with the same hash. The chunk_hash job fills the hash of older chunks;
-- a chunk without one gets no copy.
ALTER TABLE chunks ADD COLUMN text_hash bytea;
CREATE INDEX chunks_text_hash ON chunks (text_hash) WHERE text_hash IS NOT NULL;
CREATE INDEX chunks_no_text_hash ON chunks (id) WHERE text_hash IS NULL;
-- The time of the last exact count of done and total. Between counts the
-- embed shards add to done.
ALTER TABLE embedding_spaces ADD COLUMN counted_at timestamptz;

-- +goose Down
ALTER TABLE embedding_spaces DROP COLUMN counted_at;
DROP INDEX chunks_no_text_hash;
DROP INDEX chunks_text_hash;
ALTER TABLE chunks DROP COLUMN text_hash;
ALTER TABLE source_items DROP COLUMN index_hash;
