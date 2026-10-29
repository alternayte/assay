-- name: Passages :many
-- Passages of items by hash, in the visible set of the trigger.
SELECT p.item_id, p.hash, p.text, p.norm, p.heading_path, i.title, i.connection_id, i.source_updated_at
FROM passages p
JOIN source_items i ON i.id = p.item_id
WHERE p.item_id = ANY(@items::bigint[]) AND p.hash = ANY(@hashes::bytea[])
  AND p.connection_id = ANY(@visible::bigint[]) AND i.connection_id = ANY(@visible::bigint[])
ORDER BY p.item_id, p.ordinal;

-- name: Cached :many
SELECT item_id, passage_hash FROM passage_extractions
WHERE item_id = ANY(@items::bigint[]) AND passage_hash = ANY(@hashes::bytea[]) AND extractor_version = @version;

-- name: InsertExtraction :exec
INSERT INTO passage_extractions (item_id, passage_hash, extractor_version, accepted, rejected, reasons, output)
VALUES (@item_id, @passage_hash, @extractor_version, @accepted, @rejected, @reasons, @output)
ON CONFLICT DO NOTHING;

-- name: KnownEntities :many
-- Visible entities with an alias that the passage contains as whole words.
SELECT DISTINCT ON (e.id) e.id, e.key, e.type, coalesce(entity_display_name(e.id, @visible::bigint[]), e.key)::text AS name
FROM entity_aliases a
JOIN entities e ON e.id = coalesce((SELECT m.merged_into FROM entities m WHERE m.id = a.entity_id), a.entity_id)
WHERE a.connection_id = ANY(@visible::bigint[])
  AND length(a.alias_norm) >= 3
  AND strpos(' ' || @norm::text || ' ', ' ' || a.alias_norm || ' ') > 0
LIMIT 20;

-- name: EntityByKey :one
SELECT e.id, e.type FROM entities e
WHERE e.key = @key
  AND EXISTS (SELECT 1 FROM entity_sources s WHERE s.entity_id = e.id AND s.connection_id = ANY(@visible::bigint[]));

-- name: EntitiesByAlias :many
SELECT DISTINCT e.id, e.type FROM entity_aliases a
JOIN entities e ON e.id = coalesce((SELECT m.merged_into FROM entities m WHERE m.id = a.entity_id), a.entity_id)
WHERE a.alias_norm = @norm AND a.connection_id = ANY(@visible::bigint[]);

-- name: Pending :one
-- Passages of a connection with no cached result, and their size.
SELECT count(*)::bigint AS passages, coalesce(sum(length(p.text)), 0)::bigint AS chars
FROM passages p
WHERE p.connection_id = ANY(@visible::bigint[])
  AND NOT EXISTS (SELECT 1 FROM passage_extractions x
                  WHERE x.item_id = p.item_id AND x.passage_hash = p.hash AND x.extractor_version = @version);

-- name: PendingBatch :many
SELECT p.id, p.item_id, p.hash
FROM passages p
WHERE p.connection_id = ANY(@visible::bigint[]) AND p.id > @after
  AND NOT EXISTS (SELECT 1 FROM passage_extractions x
                  WHERE x.item_id = p.item_id AND x.passage_hash = p.hash AND x.extractor_version = @version)
ORDER BY p.id
LIMIT @lim;
