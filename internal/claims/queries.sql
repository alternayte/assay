-- name: AboutEntities :many
-- Claims about the entities, as subject or object, with visible support.
-- Names are the best visible aliases.
SELECT c.id, c.predicate, c.subject_id, s.key AS subject_key, s.type AS subject_type,
       coalesce((SELECT n.alias FROM entity_aliases n JOIN connections nk ON nk.id = n.connection_id
                 WHERE n.entity_id = s.id AND n.connection_id = ANY(@visible::bigint[])
                 ORDER BY nk.kind = 'registry' DESC, n.is_name DESC, n.id LIMIT 1), s.key)::text AS subject_name,
       c.object_entity_id, coalesce(o.key, '')::text AS object_key, coalesce(o.type, '')::text AS object_type,
       coalesce((SELECT n.alias FROM entity_aliases n JOIN connections nk ON nk.id = n.connection_id
                 WHERE n.entity_id = o.id AND n.connection_id = ANY(@visible::bigint[])
                 ORDER BY nk.kind = 'registry' DESC, n.is_name DESC, n.id LIMIT 1), o.key, '')::text AS object_name,
       coalesce(c.object_text, '')::text AS object_text, c.object_norm, c.scope_env, c.valid_from
FROM claims c
JOIN entities s ON s.id = c.subject_id
LEFT JOIN entities o ON o.id = c.object_entity_id
WHERE c.retracted_at IS NULL
  AND EXISTS (SELECT 1 FROM entity_sources ss WHERE ss.entity_id = s.id AND ss.connection_id = ANY(@visible::bigint[]))
  AND (o.id IS NULL OR EXISTS (SELECT 1 FROM entity_sources os WHERE os.entity_id = o.id AND os.connection_id = ANY(@visible::bigint[])))
  AND (c.subject_id = ANY(@entities::bigint[]) OR c.object_entity_id = ANY(@entities::bigint[]))
  AND EXISTS (SELECT 1 FROM claim_evidence e
              WHERE e.claim_id = c.id AND e.connection_id = ANY(@visible::bigint[]) AND e.stance = 'supports')
ORDER BY c.subject_id = ANY(@entities::bigint[]) DESC, c.valid_from DESC NULLS LAST, c.id
LIMIT @lim;

-- name: VisibleEvidence :many
-- The visible evidence of claims, with its item.
SELECT e.claim_id, e.item_id, e.passage_hash, e.kind, e.stance, e.quote, e.state, e.source_type, e.connection_id,
       i.title, i.url, i.source_updated_at, k.name AS connection_name
FROM claim_evidence e
JOIN source_items i ON i.id = e.item_id
JOIN connections k ON k.id = e.connection_id
WHERE e.claim_id = ANY(@claims::bigint[]) AND e.connection_id = ANY(@visible::bigint[])
  AND i.connection_id = ANY(@visible::bigint[])
ORDER BY e.claim_id, e.state, e.id;
