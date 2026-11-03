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
-- Passages with no cached result that no job is extracting now.
SELECT p.id, p.item_id, p.hash
FROM passages p
WHERE p.connection_id = ANY(@visible::bigint[]) AND p.id > @after
  AND NOT EXISTS (SELECT 1 FROM passage_extractions x
                  WHERE x.item_id = p.item_id AND x.passage_hash = p.hash AND x.extractor_version = @version)
  AND NOT EXISTS (SELECT 1 FROM passage_leases l
                  WHERE l.item_id = p.item_id AND l.passage_hash = p.hash AND l.extractor_version = @version AND l.expires_at > now())
ORDER BY p.id
LIMIT @lim;

-- name: IsCached :one
SELECT EXISTS (SELECT 1 FROM passage_extractions
               WHERE item_id = @item_id AND passage_hash = @passage_hash AND extractor_version = @version)::boolean;

-- name: AcquireLease :execrows
-- A lease that has expired goes to the new job.
INSERT INTO passage_leases (item_id, passage_hash, extractor_version, expires_at)
VALUES (@item_id, @passage_hash, @version, @expires_at)
ON CONFLICT (item_id, passage_hash, extractor_version) DO UPDATE SET expires_at = EXCLUDED.expires_at
WHERE passage_leases.expires_at <= now();

-- name: ReleaseLease :exec
DELETE FROM passage_leases WHERE item_id = @item_id AND passage_hash = @passage_hash AND extractor_version = @version;

-- name: ReusableOutput :one
-- The model output for the same passage text in another item of the
-- connection, in the visible set of the job.
SELECT x.output FROM passage_extractions x
JOIN source_items i ON i.id = x.item_id
WHERE x.passage_hash = @passage_hash AND x.extractor_version = @version AND i.connection_id = @connection_id AND x.item_id <> @item_id
  AND i.connection_id = ANY(@visible::bigint[])
LIMIT 1;
