-- name: VisibleClaims :many
-- Claims with visible support whose subject is in @subjects, whose object
-- is in @objects, or whose ID is in @ids. Names are the best visible
-- aliases. Subject and object need a visible source.
SELECT c.id, c.predicate, c.subject_id, s.key AS subject_key, s.type AS subject_type,
       coalesce(entity_display_name(s.id, @visible::bigint[]), s.key)::text AS subject_name,
       c.object_entity_id, coalesce(o.key, '')::text AS object_key, coalesce(o.type, '')::text AS object_type,
       coalesce(entity_display_name(o.id, @visible::bigint[]), o.key, '')::text AS object_name,
       coalesce(c.object_text, '')::text AS object_text, c.object_norm, c.scope_env, c.valid_from, c.origin
FROM claims c
JOIN entities s ON s.id = c.subject_id
LEFT JOIN entities o ON o.id = c.object_entity_id
WHERE c.retracted_at IS NULL
  AND EXISTS (SELECT 1 FROM entity_sources ss WHERE ss.entity_id = s.id AND ss.connection_id = ANY(@visible::bigint[]))
  AND (o.id IS NULL OR EXISTS (SELECT 1 FROM entity_sources os WHERE os.entity_id = o.id AND os.connection_id = ANY(@visible::bigint[])))
  AND (c.subject_id = ANY(@subjects::bigint[]) OR c.object_entity_id = ANY(@objects::bigint[]) OR c.id = ANY(@ids::bigint[]))
  AND EXISTS (SELECT 1 FROM claim_evidence e
              WHERE e.claim_id = c.id AND e.connection_id = ANY(@visible::bigint[]) AND e.stance = 'supports')
ORDER BY c.id;

-- name: VisibleEvidence :many
-- The visible evidence of claims, with its item. Evidence of a proposal
-- carries the proposal status.
SELECT e.claim_id, e.item_id, e.passage_hash, e.kind, e.stance, e.quote, e.state, e.source_type, e.connection_id, e.created_at,
       i.title, i.url, i.source_updated_at, k.name AS connection_name, coalesce(p.status, '')::text AS proposal_status
FROM claim_evidence e
JOIN source_items i ON i.id = e.item_id
JOIN connections k ON k.id = e.connection_id
LEFT JOIN proposals p ON p.id = e.proposal_id
WHERE e.claim_id = ANY(@claims::bigint[]) AND e.connection_id = ANY(@visible::bigint[])
  AND i.connection_id = ANY(@visible::bigint[])
ORDER BY e.claim_id, e.state, e.id;

-- name: Resolutions :many
SELECT id, subject_id, predicate, scope_key, chosen_claim_id, decided_at, reopened_at
FROM resolutions WHERE subject_id = ANY(@subjects::bigint[]);

-- name: SupersededDecisions :many
-- Decisions that a claim with visible support supersedes.
SELECT DISTINCT c.object_entity_id::bigint AS id
FROM claims c
WHERE c.predicate = 'supersedes' AND c.retracted_at IS NULL AND c.object_entity_id = ANY(@subjects::bigint[])
  AND EXISTS (SELECT 1 FROM claim_evidence e
              WHERE e.claim_id = c.id AND e.connection_id = ANY(@visible::bigint[]) AND e.stance = 'supports');

-- name: ClaimScope :one
-- A claim with visible support, for a resolution.
SELECT c.id, c.subject_id, c.predicate, c.scope_env FROM claims c
WHERE c.id = @id AND c.retracted_at IS NULL
  AND EXISTS (SELECT 1 FROM claim_evidence e
              WHERE e.claim_id = c.id AND e.connection_id = ANY(@visible::bigint[]) AND e.stance = 'supports');

-- name: CloseResolutions :exec
-- A new resolution replaces the open one of the same subject, predicate and
-- scope.
UPDATE resolutions SET reopened_at = @now
WHERE subject_id = @subject_id AND predicate = @predicate AND scope_key = @scope_key AND reopened_at IS NULL;

-- name: InsertResolution :one
INSERT INTO resolutions (subject_id, predicate, scope_key, chosen_claim_id, decided_by, decided_at)
VALUES (@subject_id, @predicate, @scope_key, @chosen_claim_id, @decided_by, @decided_at)
RETURNING id;

-- name: ReopenResolutions :many
-- Resolutions that chose another claim than the one with new support.
UPDATE resolutions r SET reopened_at = @now
FROM claims c
WHERE c.id = @claim_id AND r.subject_id = c.subject_id AND r.predicate = c.predicate AND r.scope_key = c.scope_env
  AND r.chosen_claim_id <> c.id AND r.reopened_at IS NULL
RETURNING r.id, r.chosen_claim_id;

-- name: InsertCard :one
INSERT INTO proposals (kind, target, payload, via, connections) VALUES (@kind, @target, @payload, 'system', @connections)
RETURNING id;

-- name: EquivalentTexts :many
-- Cached equivalent pairs among the text values.
SELECT a_norm, b_norm FROM text_equivalence
WHERE equivalent AND a_norm = ANY(@norms::text[]) AND b_norm = ANY(@norms::text[]);
