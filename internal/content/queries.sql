-- name: GetItemByExternalID :one
-- vis:system
SELECT id, content_hash, revision FROM source_items
WHERE connection_id = @connection_id AND external_id = @external_id;

-- name: InsertItem :one
-- vis:system
INSERT INTO source_items (connection_id, external_id, kind, title, url, author, source_created_at, source_updated_at, content_hash,
                          revision)
VALUES (@connection_id, @external_id, @kind, @title, @url, @author, @source_created_at, @source_updated_at, @content_hash,
        @revision)
RETURNING id;

-- name: UpdateItem :exec
-- vis:system
UPDATE source_items
SET kind = @kind, title = @title, url = @url, author = @author, source_created_at = @source_created_at,
    source_updated_at = @source_updated_at, content_hash = @content_hash, version_id = @version_id, revision = @revision,
    updated_at = now()
WHERE id = @id;

-- name: SetItemEntity :exec
-- vis:system
UPDATE source_items SET entity_id = @entity_id WHERE id = @id;

-- name: SetRevision :exec
-- vis:system
UPDATE source_items SET revision = @revision WHERE id = @id;

-- name: InsertVersion :one
INSERT INTO source_versions (item_id, connection_id, content_hash, title, body, metadata, edited_by, source_updated_at)
VALUES (@item_id, @connection_id, @content_hash, @title, @body, @metadata, @edited_by, @source_updated_at)
RETURNING id;

-- name: DeleteItemPassages :exec
-- vis:system
DELETE FROM passages WHERE item_id = @item_id;

-- name: DeleteItemChunks :exec
-- vis:system
DELETE FROM chunks WHERE item_id = @item_id;

-- name: InsertPassages :copyfrom
-- vis:system
INSERT INTO passages (item_id, version_id, connection_id, hash, ordinal, kind, heading_path, text, norm)
VALUES (@item_id, @version_id, @connection_id, @hash, @ordinal, @kind, @heading_path, @text, @norm);

-- name: InsertChunk :exec
-- vis:system
INSERT INTO chunks (item_id, version_id, connection_id, ordinal, heading_path, text, passage_hashes, tokens, tsv)
VALUES (@item_id, @version_id, @connection_id, @ordinal, @heading_path, @text, @passage_hashes::bytea[], @tokens,
        setweight(to_tsvector('simple', unaccent(@title::text)), 'A') ||
        setweight(to_tsvector('simple', unaccent(@heading::text)), 'B') ||
        setweight(to_tsvector('simple', unaccent(@body::text)), 'C'));

-- name: RefreshEvidence :exec
-- Evidence whose passage hash survives in the new version stays current.
-- Other evidence of the item becomes stale.
-- vis:system
UPDATE claim_evidence e
SET state = CASE WHEN EXISTS (SELECT 1 FROM passages p WHERE p.item_id = e.item_id AND p.hash = e.passage_hash)
                 THEN 'current' ELSE 'stale' END,
    updated_at = now()
WHERE e.item_id = @item_id AND e.kind <> 'human';

-- name: UpsertEntity :one
INSERT INTO entities (type, key, domain_id) VALUES (@type, @key, @domain_id)
ON CONFLICT (key) DO UPDATE SET type = EXCLUDED.type, domain_id = coalesce(entities.domain_id, EXCLUDED.domain_id)
RETURNING id;

-- name: AddEntitySource :exec
-- vis:system
INSERT INTO entity_sources (entity_id, connection_id) VALUES (@entity_id, @connection_id)
ON CONFLICT DO NOTHING;

-- name: AddAlias :exec
-- vis:system
INSERT INTO entity_aliases (entity_id, connection_id, alias, alias_norm, is_name)
VALUES (@entity_id, @connection_id, @alias, @alias_norm, @is_name)
ON CONFLICT (entity_id, connection_id, alias_norm)
DO UPDATE SET is_name = entity_aliases.is_name OR EXCLUDED.is_name,
              alias = CASE WHEN EXCLUDED.is_name THEN EXCLUDED.alias ELSE entity_aliases.alias END;

-- name: ClearOtherNames :exec
-- vis:system
UPDATE entity_aliases SET is_name = false
WHERE entity_id = @entity_id AND connection_id = @connection_id AND alias_norm <> @alias_norm AND is_name;

-- name: LatestClaim :one
SELECT id, coalesce(object_entity_id, 0)::bigint AS object_entity_id, object_norm, retracted_at
FROM claims
WHERE subject_id = @subject_id AND predicate = @predicate AND scope_env = '' AND origin = 'source'
ORDER BY valid_from DESC NULLS LAST, id DESC
LIMIT 1;

-- name: UpsertClaim :one
INSERT INTO claims (subject_id, predicate, object_entity_id, object_text, object_norm, valid_from, origin)
VALUES (@subject_id, @predicate, @object_entity_id, @object_text, @object_norm, @valid_from, 'source')
ON CONFLICT (subject_id, predicate, (coalesce(object_entity_id, 0)), object_norm, scope_env,
             (coalesce(valid_from, '-infinity'::timestamptz)))
DO UPDATE SET retracted_at = NULL
RETURNING id;

-- name: Unretract :exec
UPDATE claims SET retracted_at = NULL WHERE id = @id;

-- name: AddEvidence :one
-- Inserted is false when the row existed.
-- vis:system
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, state, source_type)
VALUES (@claim_id, @connection_id, @item_id, @passage_hash, @kind, @stance, @quote, 'current', @source_type)
ON CONFLICT (claim_id, item_id, passage_hash, stance)
DO UPDATE SET state = 'current', quote = EXCLUDED.quote, source_type = EXCLUDED.source_type, updated_at = now()
RETURNING (xmax = 0) AS inserted;

-- name: ClaimGroupConnections :many
-- The connections of the evidence of a claim and of the other claims of
-- its subject and predicate.
-- vis:system
SELECT DISTINCT e.connection_id FROM claim_evidence e
JOIN claims c ON c.id = e.claim_id
JOIN claims t ON t.subject_id = c.subject_id AND t.predicate = c.predicate
WHERE t.id = @claim_id
ORDER BY e.connection_id;

-- name: ConnectionKind :one
SELECT kind FROM connections WHERE id = @id;

-- name: ItemClaims :many
-- vis:system
SELECT DISTINCT claim_id FROM claim_evidence WHERE item_id = @item_id;

-- name: DeleteItem :execrows
-- The cascade deletes the versions, passages, chunks and evidence of the item.
-- vis:system
DELETE FROM source_items WHERE id = @id;

-- name: RetractUnsupported :execrows
-- vis:system
UPDATE claims c SET retracted_at = @now
WHERE c.id = ANY(@ids::bigint[]) AND c.retracted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM claim_evidence e WHERE e.claim_id = c.id);

-- name: GetVisibleItem :one
SELECT i.id, i.connection_id, c.name AS connection_name, c.kind AS connection_kind, i.kind, i.title, i.url, i.author,
       i.source_updated_at
FROM source_items i
JOIN connections c ON c.id = i.connection_id
WHERE i.id = @id AND i.connection_id = ANY(@visible::bigint[]);

-- name: VisiblePassages :many
SELECT hash, ordinal, kind, heading_path, text
FROM passages
WHERE item_id = @item_id AND connection_id = ANY(@visible::bigint[])
ORDER BY ordinal;
