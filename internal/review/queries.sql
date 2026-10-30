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
