-- name: Explore :many
-- Visible entities with filters, newest change first. An entity is visible
-- when it has a visible source. Merged entities show as their winner.
SELECT e.id, coalesce(entity_visible_key(e.id, @visible::bigint[]), e.key)::text AS key, e.type, e.status, e.domain_id,
       coalesce(d.name, '')::text AS domain_name, coalesce(entity_display_name(e.id, @visible::bigint[]), e.key)::text AS name,
       greatest(e.updated_at,
                (SELECT max(ev.updated_at) FROM claim_evidence ev JOIN claims c ON c.id = ev.claim_id
                 WHERE (c.subject_id = e.id OR c.subject_id IN (SELECT x.id FROM entities x WHERE x.merged_into = e.id))
                   AND ev.connection_id = ANY(@visible::bigint[])))::timestamptz AS changed_at
FROM entities e
LEFT JOIN domains d ON d.id = e.domain_id
WHERE e.status <> 'merged'
  AND EXISTS (SELECT 1 FROM entity_sources s
              WHERE (s.entity_id = e.id OR s.entity_id IN (SELECT x.id FROM entities x WHERE x.merged_into = e.id))
                AND s.connection_id = ANY(@visible::bigint[]))
  AND (@type::text = '' OR e.type = @type)
  AND (@domain::bigint = 0 OR e.domain_id = @domain)
  AND (@status::text = '' OR e.status = @status)
  AND (@query::text = '' OR EXISTS (SELECT 1 FROM entity_aliases a
                                    WHERE (a.entity_id = e.id OR a.entity_id IN (SELECT x.id FROM entities x WHERE x.merged_into = e.id))
                                      AND a.connection_id = ANY(@visible::bigint[])
                                      AND (a.alias_norm LIKE '%' || @query || '%' OR a.alias_norm % @query)))
ORDER BY changed_at DESC NULLS LAST, e.id DESC
LIMIT @lim;

-- name: VisibleEntityByKey :one
-- An entity by key, for a reader. A merged entity resolves to its winner.
-- A hidden entity and a missing key give no row. The key is the visible key
-- of the winner.
SELECT w.id, coalesce(entity_visible_key(w.id, @visible::bigint[]), w.key)::text AS key, w.type, w.status, w.domain_id, coalesce(d.name, '')::text AS domain_name,
       coalesce(entity_display_name(w.id, @visible::bigint[]), w.key)::text AS name, w.created_at
FROM entities e
JOIN entities w ON w.id = coalesce(e.merged_into, e.id)
LEFT JOIN domains d ON d.id = w.domain_id
WHERE e.key = @key
  AND EXISTS (SELECT 1 FROM entity_sources es WHERE es.entity_id = e.id AND es.connection_id = ANY(@visible::bigint[]))
  AND EXISTS (SELECT 1 FROM entity_sources s
              WHERE (s.entity_id = w.id OR s.entity_id IN (SELECT x.id FROM entities x WHERE x.merged_into = w.id))
                AND s.connection_id = ANY(@visible::bigint[]));

-- name: Members :many
-- The winner and the entities merged into it.
SELECT id FROM entities WHERE id = @id OR merged_into = @id ORDER BY id;

-- name: VisibleAliases :many
SELECT DISTINCT ON (a.alias_norm) a.alias, a.alias_norm, a.is_name
FROM entity_aliases a
WHERE a.entity_id = ANY(@members::bigint[]) AND a.connection_id = ANY(@visible::bigint[])
ORDER BY a.alias_norm, a.is_name DESC, a.id;

-- name: VisibleSources :many
-- The visible items that give evidence about the entities, newest first.
SELECT DISTINCT ON (i.id) i.id, i.title, i.url, i.kind, i.source_updated_at, k.name AS connection_name, k.kind AS source_type
FROM claim_evidence ev
JOIN claims c ON c.id = ev.claim_id
JOIN source_items i ON i.id = ev.item_id
JOIN connections k ON k.id = i.connection_id
WHERE (c.subject_id = ANY(@members::bigint[]) OR c.object_entity_id = ANY(@members::bigint[]))
  AND ev.connection_id = ANY(@visible::bigint[]) AND i.connection_id = ANY(@visible::bigint[])
ORDER BY i.id;

-- name: GetEntitiesByID :many
SELECT id, key, type, status, merged_into, created_at FROM entities WHERE id = ANY(@ids::bigint[]);

-- name: HasRegistrySource :many
SELECT DISTINCT s.entity_id FROM entity_sources s JOIN connections k ON k.id = s.connection_id
WHERE s.entity_id = ANY(@ids::bigint[]) AND k.kind = 'registry' AND s.connection_id = ANY(@visible::bigint[]);

-- name: EveryoneConnections :many
-- The active connections that are shared with Everyone: the visible set that
-- every user has.
SELECT c.id FROM connections c
WHERE c.status = 'active'
  AND EXISTS (SELECT 1 FROM connection_visibility v JOIN groups g ON g.id = v.group_id AND g.builtin WHERE v.connection_id = c.id)
ORDER BY c.id;

-- name: AllVisible :one
-- Whether each entity has a source in the visible set.
SELECT (NOT EXISTS (SELECT 1 FROM unnest(@ids::bigint[]) x(id)
                    WHERE NOT EXISTS (SELECT 1 FROM entity_sources es
                                      WHERE es.entity_id = x.id AND es.connection_id = ANY(@visible::bigint[]))))::bool AS visible;

-- name: InsertMerge :one
INSERT INTO merges (winner_id, loser_ids, canonical_name, by, auto)
VALUES (@winner_id, @loser_ids, @canonical_name, @by, @auto)
RETURNING id;

-- name: MarkMerged :exec
UPDATE entities SET status = 'merged', merged_into = @winner, updated_at = now() WHERE id = ANY(@ids::bigint[]);

-- name: RepointMerged :exec
-- Entities merged into a loser follow it to the new winner.
UPDATE entities SET merged_into = @winner, updated_at = now() WHERE merged_into = ANY(@ids::bigint[]);

-- name: GetMerge :one
SELECT * FROM merges WHERE id = @id;

-- name: UndoMerge :execrows
UPDATE merges SET undone_at = @now WHERE id = @id AND undone_at IS NULL;

-- name: VisibleKey :one
SELECT coalesce(entity_visible_key(@id, @visible::bigint[]), '')::text AS key;

-- name: MergedOwners :many
-- The entities merged into the winner, each with the winner of its newest
-- active merge, or 0.
SELECT e.id, coalesce((SELECT m.winner_id FROM merges m WHERE m.undone_at IS NULL AND e.id = ANY(m.loser_ids)
                       ORDER BY m.id DESC LIMIT 1), 0)::bigint AS owner
FROM entities e WHERE e.merged_into = @winner ORDER BY e.id;

-- name: TopWinner :one
-- The entity that id is merged into at the top, or id itself.
WITH RECURSIVE up AS (SELECT e.id, e.merged_into FROM entities e WHERE e.id = @id
                      UNION ALL
                      SELECT e.id, e.merged_into FROM entities e JOIN up ON e.id = up.merged_into)
SELECT up.id FROM up WHERE up.merged_into IS NULL LIMIT 1;

-- name: SetMergedInto :exec
UPDATE entities SET merged_into = @winner, updated_at = now() WHERE id = @id;

-- name: MergeVisible :one
-- Whether the merge exists and the reader sees a source of each entity.
SELECT EXISTS (SELECT 1 FROM merges m WHERE m.id = @id
  AND NOT EXISTS (SELECT 1 FROM unnest(m.winner_id || m.loser_ids) x(id)
                  WHERE NOT EXISTS (SELECT 1 FROM entity_sources es WHERE es.entity_id = x.id AND es.connection_id = ANY(@visible::bigint[]))))::bool;

-- name: Unmerge :execrows
UPDATE entities SET status = 'active', merged_into = NULL, updated_at = now()
WHERE id = ANY(@ids::bigint[]) AND merged_into = @winner;

-- name: Edges :many
-- Entity-valued claims of the entities, in both directions, with visible
-- support. Endpoints need a visible source.
SELECT c.id, c.predicate, coalesce(cs.merged_into, cs.id)::bigint AS subject_id, coalesce(co.merged_into, co.id)::bigint AS object_id
FROM claims c
JOIN entities cs ON cs.id = c.subject_id
JOIN entities co ON co.id = c.object_entity_id
WHERE c.retracted_at IS NULL
  AND (c.subject_id = ANY(@ids::bigint[]) OR c.object_entity_id = ANY(@ids::bigint[]))
  AND (cardinality(@predicates::text[]) = 0 OR c.predicate = ANY(@predicates::text[]))
  AND EXISTS (SELECT 1 FROM claim_evidence e
              WHERE e.claim_id = c.id AND e.connection_id = ANY(@visible::bigint[]) AND e.stance = 'supports')
  AND EXISTS (SELECT 1 FROM entity_sources s WHERE s.entity_id = c.subject_id AND s.connection_id = ANY(@visible::bigint[]))
  AND EXISTS (SELECT 1 FROM entity_sources s WHERE s.entity_id = c.object_entity_id AND s.connection_id = ANY(@visible::bigint[]))
ORDER BY c.id;

-- name: Nodes :many
SELECT e.id, coalesce(entity_visible_key(e.id, @visible::bigint[]), e.key)::text AS key, e.type,
       coalesce(entity_display_name(e.id, @visible::bigint[]), e.key)::text AS name
FROM entities e WHERE e.id = ANY(@ids::bigint[]);

-- name: ActiveMerges :many
-- The active merges into an entity when the reader sees every entity merged
-- into it: a merge with a hidden entity would show its canonical name
-- (SI-16), as in entity_display_name.
SELECT m.id, m.canonical_name, m.auto, m.created_at,
       array(SELECT x.key FROM entities x
             WHERE x.id = ANY(m.loser_ids)
               AND EXISTS (SELECT 1 FROM entity_sources es WHERE es.entity_id = x.id AND es.connection_id = ANY(@visible::bigint[]))
             ORDER BY x.id)::text[] AS keys,
       array(SELECT coalesce(entity_display_name(x.id, @visible::bigint[]), x.key) FROM entities x
             WHERE x.id = ANY(m.loser_ids)
               AND EXISTS (SELECT 1 FROM entity_sources es WHERE es.entity_id = x.id AND es.connection_id = ANY(@visible::bigint[]))
             ORDER BY x.id)::text[] AS names
FROM merges m
WHERE m.winner_id = @winner_id AND m.undone_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM entities x
                  WHERE (x.id = m.winner_id OR x.merged_into = m.winner_id)
                    AND NOT EXISTS (SELECT 1 FROM entity_sources es WHERE es.entity_id = x.id AND es.connection_id = ANY(@visible::bigint[])))
  AND NOT EXISTS (SELECT 1 FROM entities x
                  WHERE x.id = ANY(m.loser_ids)
                    AND NOT EXISTS (SELECT 1 FROM entity_sources es WHERE es.entity_id = x.id AND es.connection_id = ANY(@visible::bigint[])))
ORDER BY m.id DESC;

-- name: SourceConnections :many
-- The visible connections with a source of the entities of keys.
SELECT DISTINCT es.connection_id FROM entity_sources es JOIN entities e ON e.id = es.entity_id
WHERE e.key = ANY(@keys::text[]) AND es.connection_id = ANY(@visible::bigint[])
ORDER BY es.connection_id;

-- name: InsertMergeProposal :one
INSERT INTO proposals (kind, target, payload, proposer_id, via, connections)
VALUES ('merge', @target, @payload, @proposer_id, 'ui', @connections)
RETURNING id;
