-- name: RegistryID :one
SELECT id FROM connections WHERE kind = 'registry';

-- name: EveryoneConnections :many
-- The registry and the connections shared with Everyone.
SELECT c.id FROM connections c
WHERE c.kind = 'registry'
   OR (c.status = 'active' AND EXISTS (SELECT 1 FROM connection_visibility v JOIN groups g ON g.id = v.group_id
                                       WHERE v.connection_id = c.id AND g.builtin))
ORDER BY c.id;

-- name: VisibleRefs :many
-- Entities by key that have a visible source, with a name for the
-- visible set of Everyone.
SELECT e.id, e.key, e.type, coalesce(entity_display_name(e.id, @everyone::bigint[]), e.key)::text AS name
FROM entities e
WHERE e.key = ANY(@keys::text[]) AND e.status <> 'merged'
  AND EXISTS (SELECT 1 FROM entity_sources s WHERE s.entity_id = e.id AND s.connection_id = ANY(@visible::bigint[]));

-- name: RecordItem :one
SELECT i.id, i.kind, i.entity_id, i.version_id FROM source_items i
WHERE i.connection_id = ANY(@visible::bigint[]) AND i.external_id = @key;

-- name: ClearRecordAliases :exec
DELETE FROM entity_aliases WHERE entity_id = @entity_id AND connection_id = ANY(@visible::bigint[]);

-- name: RecordVersions :many
SELECT id, edited_by, created_at FROM source_versions WHERE item_id = @item_id ORDER BY id DESC;

-- name: RecordVersion :one
SELECT id, body, metadata, edited_by, created_at FROM source_versions WHERE item_id = @item_id AND id = @id;

-- name: PreviousVersion :one
SELECT id, body FROM source_versions WHERE item_id = @item_id AND id < @id ORDER BY id DESC LIMIT 1;

-- name: ListRecords :many
SELECT i.external_id AS key, i.kind, i.title, i.source_updated_at, v.metadata
FROM source_items i
JOIN source_versions v ON v.id = i.version_id
WHERE i.connection_id = ANY(@visible::bigint[]) AND (sqlc.narg(kind)::text IS NULL OR i.kind = sqlc.narg(kind))
ORDER BY lower(i.title)
LIMIT 1000;

-- name: SetEntityDomain :exec
UPDATE entities SET domain_id = @domain_id WHERE id = @id;

-- name: EntityID :one
SELECT id FROM entities WHERE key = @key;

-- name: InsertTermEdit :one
INSERT INTO proposals (kind, target, payload, proposer_id, via, connections)
VALUES ('term_edit', @target, @payload, @proposer_id, 'ui', @connections)
RETURNING id;

-- name: UserName :one
SELECT coalesce(nullif(display_name, ''), email)::text FROM auth_users WHERE id = @id;
