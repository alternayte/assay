-- name: ListDomains :many
SELECT id, name, description FROM domains ORDER BY lower(name);

-- name: InsertDomain :one
INSERT INTO domains (name, description) VALUES (@name, @description) RETURNING id;

-- name: UpdateDomain :execrows
UPDATE domains SET name = @name, description = @description WHERE id = @id;

-- name: DeleteDomain :execrows
DELETE FROM domains WHERE id = @id;

-- name: VisibleTerms :many
-- Active terms with a visible source. Status comes from visible registry
-- or review evidence, the newest value first. Names and aliases come from
-- visible connections only (SI-16).
SELECT e.id, e.key, coalesce(e.domain_id, 0)::bigint AS domain_id, coalesce(d.name, '')::text AS domain,
       coalesce(entity_display_name(e.id, @visible::bigint[]), e.key)::text AS name,
       ARRAY(SELECT DISTINCT a.alias FROM entity_aliases a
             WHERE (a.entity_id = e.id OR a.entity_id IN (SELECT x.id FROM entities x WHERE x.merged_into = e.id))
               AND a.connection_id = ANY(@visible::bigint[]) ORDER BY a.alias)::text[] AS aliases,
       coalesce((SELECT c.object_text FROM claims c JOIN claim_evidence ev ON ev.claim_id = c.id
                 WHERE c.subject_id = e.id AND c.predicate = 'status' AND c.retracted_at IS NULL AND ev.state = 'current'
                   AND ev.connection_id = ANY(@visible::bigint[]) AND ev.source_type IN ('registry', 'review')
                 ORDER BY c.valid_from DESC NULLS LAST, c.id DESC LIMIT 1), '')::text AS status,
       coalesce((SELECT c.object_text FROM claims c JOIN claim_evidence ev ON ev.claim_id = c.id
                 WHERE c.subject_id = e.id AND c.predicate = 'defined_as' AND c.retracted_at IS NULL AND ev.state = 'current'
                   AND ev.connection_id = ANY(@visible::bigint[])
                 ORDER BY (ev.source_type IN ('registry', 'review')) DESC, c.id LIMIT 1), '')::text AS definition,
       coalesce((SELECT o.key FROM claims c JOIN entities o ON o.id = c.object_entity_id JOIN claim_evidence ev ON ev.claim_id = c.id
                 WHERE c.subject_id = e.id AND c.predicate = 'preferred_term' AND c.retracted_at IS NULL AND ev.state = 'current'
                   AND ev.connection_id = ANY(@visible::bigint[]) ORDER BY c.id LIMIT 1), '')::text AS preferred,
       coalesce((SELECT entity_display_name(c.object_entity_id, @visible::bigint[]) FROM claims c JOIN claim_evidence ev ON ev.claim_id = c.id
                 WHERE c.subject_id = e.id AND c.predicate = 'owned_by' AND c.retracted_at IS NULL AND ev.state = 'current'
                   AND ev.connection_id = ANY(@visible::bigint[]) ORDER BY c.id LIMIT 1), '')::text AS owner
FROM entities e
LEFT JOIN domains d ON d.id = e.domain_id
WHERE e.type = 'term' AND e.status = 'active'
  AND EXISTS (SELECT 1 FROM entity_sources s WHERE s.entity_id = e.id AND s.connection_id = ANY(@visible::bigint[]))
ORDER BY lower(coalesce(entity_display_name(e.id, @visible::bigint[]), e.key)), e.id;

-- name: TermUsage :many
-- Visible items per connection whose text matches the pattern.
SELECT p.connection_id, k.name, count(DISTINCT p.item_id)::int AS items
FROM passages p
JOIN connections k ON k.id = p.connection_id
WHERE p.connection_id = ANY(@visible::bigint[]) AND p.norm ~* @pattern::text
GROUP BY p.connection_id, k.name
ORDER BY items DESC, k.name;
