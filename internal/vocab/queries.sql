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
-- or review evidence: the claim that an open resolution chose, else the
-- newest value. Status, definition, preferred term and owner use only
-- evidence with no proposal or an accepted proposal. The preferred term
-- needs a visible source. Names and aliases come from
-- visible connections only (SI-16). Connections are the visible connections
-- that give the name, the aliases, the status, the definition, the
-- preferred term and the owner: a text that shows the term depends on them.
SELECT e.id, e.key, coalesce(e.domain_id, 0)::bigint AS domain_id, coalesce(d.name, '')::text AS domain,
       coalesce(entity_display_name(e.id, @visible::bigint[]), e.key)::text AS name,
       ARRAY(SELECT DISTINCT a.alias FROM entity_aliases a
             WHERE a.entity_id = ANY(entity_members(e.id))
               AND a.connection_id = ANY(@visible::bigint[]) ORDER BY a.alias)::text[] AS aliases,
       coalesce(st.value, '')::text AS status,
       coalesce(df.value, '')::text AS definition,
       coalesce(pf.value, '')::text AS preferred,
       coalesce(ow.value, '')::text AS owner,
       ARRAY(SELECT DISTINCT x.connection_id FROM (
               SELECT s.connection_id FROM entity_sources s
               WHERE s.entity_id = ANY(entity_members(e.id))
                 AND s.connection_id = ANY(@visible::bigint[])
               UNION
               SELECT a.connection_id FROM entity_aliases a
               WHERE a.entity_id = ANY(entity_members(e.id))
                 AND a.connection_id = ANY(@visible::bigint[])
               UNION
               SELECT u.conn FROM (VALUES (st.connection_id), (df.connection_id), (pf.connection_id), (ow.connection_id)) u (conn)
               WHERE u.conn IS NOT NULL) x
             ORDER BY x.connection_id)::bigint[] AS connections
FROM entities e
LEFT JOIN domains d ON d.id = e.domain_id
LEFT JOIN LATERAL (
    SELECT c.object_text AS value, ev.connection_id FROM claims c JOIN claim_evidence ev ON ev.claim_id = c.id
    WHERE c.subject_id = e.id AND c.predicate = 'status' AND c.retracted_at IS NULL AND ev.state = 'current'
      AND ev.connection_id = ANY(@visible::bigint[]) AND ev.source_type IN ('registry', 'review')
      AND (ev.proposal_id IS NULL OR EXISTS (SELECT 1 FROM proposals pr WHERE pr.id = ev.proposal_id AND pr.status = 'accepted'))
    ORDER BY EXISTS (SELECT 1 FROM resolutions r WHERE r.chosen_claim_id = c.id AND r.reopened_at IS NULL) DESC,
             c.valid_from DESC NULLS LAST, c.id DESC LIMIT 1) st ON true
LEFT JOIN LATERAL (
    SELECT c.object_text AS value, ev.connection_id FROM claims c JOIN claim_evidence ev ON ev.claim_id = c.id
    WHERE c.subject_id = e.id AND c.predicate = 'defined_as' AND c.retracted_at IS NULL AND ev.state = 'current'
      AND ev.connection_id = ANY(@visible::bigint[])
      AND (ev.proposal_id IS NULL OR EXISTS (SELECT 1 FROM proposals pr WHERE pr.id = ev.proposal_id AND pr.status = 'accepted'))
    ORDER BY (ev.source_type IN ('registry', 'review')) DESC, c.id LIMIT 1) df ON true
LEFT JOIN LATERAL (
    SELECT o.key AS value, ev.connection_id FROM claims c JOIN entities o ON o.id = c.object_entity_id JOIN claim_evidence ev ON ev.claim_id = c.id
    WHERE c.subject_id = e.id AND c.predicate = 'preferred_term' AND c.retracted_at IS NULL AND ev.state = 'current'
      AND ev.connection_id = ANY(@visible::bigint[])
      AND (ev.proposal_id IS NULL OR EXISTS (SELECT 1 FROM proposals pr WHERE pr.id = ev.proposal_id AND pr.status = 'accepted'))
      AND EXISTS (SELECT 1 FROM entity_sources os WHERE os.entity_id = o.id AND os.connection_id = ANY(@visible::bigint[]))
    ORDER BY c.id LIMIT 1) pf ON true
LEFT JOIN LATERAL (
    SELECT entity_display_name(c.object_entity_id, @visible::bigint[]) AS value, ev.connection_id
    FROM claims c JOIN claim_evidence ev ON ev.claim_id = c.id
    WHERE c.subject_id = e.id AND c.predicate = 'owned_by' AND c.retracted_at IS NULL AND ev.state = 'current'
      AND ev.connection_id = ANY(@visible::bigint[])
      AND (ev.proposal_id IS NULL OR EXISTS (SELECT 1 FROM proposals pr WHERE pr.id = ev.proposal_id AND pr.status = 'accepted'))
    ORDER BY c.id LIMIT 1) ow ON true
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
