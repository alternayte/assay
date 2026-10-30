-- name: VisibleClaim :one
-- A claim with visible support, with the connections and items of its
-- visible evidence.
SELECT c.id, c.subject_id, c.predicate, c.scope_env, s.key AS subject_key, s.type AS subject_type,
       array_agg(DISTINCT e.connection_id ORDER BY e.connection_id)::bigint[] AS connections,
       array_agg(DISTINCT e.item_id ORDER BY e.item_id)::bigint[] AS items
FROM claims c
JOIN entities s ON s.id = c.subject_id
JOIN claim_evidence e ON e.claim_id = c.id AND e.connection_id = ANY(@visible::bigint[]) AND e.stance = 'supports'
WHERE c.id = @id AND c.retracted_at IS NULL
GROUP BY c.id, s.key, s.type;

-- name: VisibleEntity :one
-- An entity with a visible source.
SELECT e.id, e.type, e.key FROM entities e
WHERE e.key = @key AND e.merged_into IS NULL
  AND EXISTS (SELECT 1 FROM entity_sources es WHERE es.entity_id = e.id AND es.connection_id = ANY(@visible::bigint[]));

-- name: VisibleItem :one
SELECT i.id, i.connection_id, k.kind FROM source_items i
JOIN connections k ON k.id = i.connection_id
WHERE i.id = @id AND i.connection_id = ANY(@visible::bigint[]);

-- name: QuotedPassage :one
-- The first passage of the current version of an item that holds the exact
-- quote.
SELECT p.hash FROM passages p
WHERE p.item_id = @item_id AND p.connection_id = ANY(@visible::bigint[]) AND strpos(p.text, @quote::text) > 0
ORDER BY p.ordinal
LIMIT 1;

-- name: UpsertClaim :one
INSERT INTO claims (subject_id, predicate, object_entity_id, object_text, object_norm, scope_env, origin)
VALUES (@subject_id, @predicate, @object_entity_id, @object_text, @object_norm, @scope_env, 'human')
ON CONFLICT (subject_id, predicate, (coalesce(object_entity_id, 0)), object_norm, scope_env,
             (coalesce(valid_from, '-infinity'::timestamptz)))
DO UPDATE SET retracted_at = NULL
RETURNING id;

-- name: InsertProposal :one
INSERT INTO proposals (kind, target, payload, proposer_id, via, connections)
VALUES (@kind, @target, @payload, @proposer_id, @via, @connections)
RETURNING id;

-- name: Proposals :many
-- The proposals of one person, newest first.
SELECT p.id, p.kind, p.target, p.status, p.reason, p.via, p.created_at, p.decided_at
FROM proposals p
WHERE p.proposer_id = @proposer_id
ORDER BY p.id DESC
LIMIT @lim;

-- name: ChangedItems :many
-- Visible items with a version since @since, newest first, with the
-- visible entities that they are about: the entity of the item and the
-- subjects of claims with current evidence on it.
WITH ch AS (
    SELECT v.item_id, max(v.created_at)::timestamptz AS at, count(*) > 1 AS changed
    FROM source_versions v
    WHERE v.connection_id = ANY(@visible::bigint[])
    GROUP BY v.item_id
    HAVING max(v.created_at) >= @since
    ORDER BY 2 DESC
    LIMIT 500
)
SELECT i.id, i.title, i.url, k.name AS connection, ch.at, ch.changed, e.id AS entity_id, e.key, e.type,
       coalesce(entity_display_name(e.id, @visible::bigint[]), e.key)::text AS name
FROM ch
JOIN source_items i ON i.id = ch.item_id AND i.connection_id = ANY(@visible::bigint[])
JOIN connections k ON k.id = i.connection_id
JOIN LATERAL (
    SELECT c.subject_id AS entity_id
    FROM claim_evidence ev JOIN claims c ON c.id = ev.claim_id
    WHERE ev.item_id = i.id AND ev.connection_id = ANY(@visible::bigint[]) AND ev.state = 'current' AND c.retracted_at IS NULL
    UNION
    SELECT i.entity_id WHERE i.entity_id IS NOT NULL
) x ON true
JOIN entities e ON e.id = x.entity_id AND e.merged_into IS NULL
WHERE EXISTS (SELECT 1 FROM entity_sources es WHERE es.entity_id = e.id AND es.connection_id = ANY(@visible::bigint[]))
ORDER BY ch.at DESC, i.id, e.id;

-- name: NewTerms :many
-- Visible terms created since @since.
SELECT e.id, e.key, e.type, coalesce(entity_display_name(e.id, @visible::bigint[]), e.key)::text AS name, e.created_at
FROM entities e
WHERE e.type = 'term' AND e.created_at >= @since AND e.merged_into IS NULL
  AND EXISTS (SELECT 1 FROM entity_sources es WHERE es.entity_id = e.id AND es.connection_id = ANY(@visible::bigint[]))
ORDER BY e.created_at DESC
LIMIT 200;

-- name: EntitiesOfKeys :many
-- Visible entities of keys.
SELECT e.id, e.key, e.type, coalesce(entity_display_name(e.id, @visible::bigint[]), e.key)::text AS name
FROM entities e
WHERE e.key = ANY(@keys::text[]) AND e.merged_into IS NULL
  AND EXISTS (SELECT 1 FROM entity_sources es WHERE es.entity_id = e.id AND es.connection_id = ANY(@visible::bigint[]));

-- name: EntitySourceConnections :many
-- The visible connections with a source of the entities.
SELECT DISTINCT es.connection_id FROM entity_sources es
WHERE es.entity_id = ANY(@ids::bigint[]) AND es.connection_id = ANY(@visible::bigint[])
ORDER BY es.connection_id;
