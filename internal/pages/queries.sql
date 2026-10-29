-- name: EvidenceConnections :many
-- The visible connections with evidence for an entity: claim evidence of its
-- claims as subject or object, and its sources.
SELECT DISTINCT x.connection_id::bigint AS connection_id FROM (
    SELECT ev.connection_id FROM claim_evidence ev
    JOIN claims c ON c.id = ev.claim_id
    WHERE (c.subject_id = ANY(@members::bigint[]) OR c.object_entity_id = ANY(@members::bigint[]))
      AND ev.connection_id = ANY(@visible::bigint[])
    UNION
    SELECT s.connection_id FROM entity_sources s
    WHERE s.entity_id = ANY(@members::bigint[]) AND s.connection_id = ANY(@visible::bigint[])
) x
ORDER BY 1;

-- name: PermissionsChecked :one
-- The oldest permissions check of the connections. unchecked is set when one
-- has none.
SELECT coalesce(bool_or(permissions_checked_at IS NULL), true)::boolean AS unchecked,
       coalesce(min(permissions_checked_at), now())::timestamptz AS checked_at
FROM connections WHERE id = ANY(@ids::bigint[]);

-- name: GetPage :one
SELECT id, claim_set_hash, claim_ids, summary_md, citations, lint, state, model, compiled_at
FROM pages WHERE entity_id = @entity_id AND visibility_key = @visibility_key;

-- name: UpsertPage :exec
INSERT INTO pages (entity_id, visibility_key, connections, claim_set_hash, claim_ids, summary_md, citations, lint, state, model, compiled_at)
VALUES (@entity_id, @visibility_key, @connections, @claim_set_hash, @claim_ids, @summary_md, @citations, @lint, @state, @model, @compiled_at)
ON CONFLICT (entity_id, visibility_key) DO UPDATE
SET connections = EXCLUDED.connections, claim_set_hash = EXCLUDED.claim_set_hash, claim_ids = EXCLUDED.claim_ids,
    summary_md = EXCLUDED.summary_md, citations = EXCLUDED.citations, lint = EXCLUDED.lint, state = EXCLUDED.state,
    model = EXCLUDED.model, compiled_at = EXCLUDED.compiled_at;

-- name: AliasNorms :many
-- The visible normalized aliases of an entity and its merged entities.
SELECT DISTINCT a.alias_norm FROM entity_aliases a
WHERE a.entity_id = ANY(@members::bigint[]) AND a.connection_id = ANY(@visible::bigint[]) AND length(a.alias_norm) >= 3;

-- name: MentioningPassages :many
-- Visible passages that match the pattern of the aliases, newest item first.
SELECT p.item_id, p.hash
FROM passages p
JOIN source_items i ON i.id = p.item_id
WHERE p.connection_id = ANY(@visible::bigint[]) AND i.connection_id = ANY(@visible::bigint[])
  AND p.norm ~* @pattern::text
ORDER BY i.source_updated_at DESC NULLS LAST, p.item_id DESC, p.ordinal
LIMIT @lim;

-- name: OpenPageCard :one
SELECT EXISTS (SELECT 1 FROM proposals WHERE kind = 'needs_edit' AND target = @target AND status = 'open')::boolean;

-- name: InsertPageCard :exec
INSERT INTO proposals (kind, target, payload, via, connections)
VALUES ('needs_edit', @target, @payload, 'system', @connections);

-- name: EntityKey :one
SELECT key FROM entities WHERE id = @id;
