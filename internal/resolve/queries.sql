-- name: ProfileInputs :many
-- The profile input of each active entity, read only from its own
-- sources (REQ-RES-002).
-- vis:system
SELECT e.id, e.type, e.key, own.ids::bigint[] AS connections,
       coalesce(entity_display_name(e.id, own.ids), e.key)::text AS name,
       ARRAY(SELECT DISTINCT a.alias FROM entity_aliases a
             WHERE a.entity_id = e.id AND a.connection_id = ANY(own.ids) ORDER BY a.alias)::text[] AS aliases,
       coalesce(d.name, '')::text AS domain,
       ARRAY(SELECT c.predicate || ': ' || coalesce(entity_display_name(c.object_entity_id, own.ids), o.key, c.object_text, '')
             FROM claims c LEFT JOIN entities o ON o.id = c.object_entity_id
             WHERE c.subject_id = e.id AND c.retracted_at IS NULL
               AND EXISTS (SELECT 1 FROM claim_evidence ev
                           WHERE ev.claim_id = c.id AND ev.connection_id = ANY(own.ids)
                             AND ev.state = 'current' AND ev.kind <> 'proposal')
             ORDER BY c.predicate, c.id LIMIT 10)::text[] AS claims,
       ARRAY(SELECT i.title FROM source_items i
             WHERE i.connection_id = ANY(own.ids)
               AND (i.entity_id = e.id OR EXISTS (SELECT 1 FROM claim_evidence ev JOIN claims c ON c.id = ev.claim_id
                                                  WHERE ev.item_id = i.id AND c.subject_id = e.id AND ev.connection_id = ANY(own.ids)))
             ORDER BY i.source_updated_at DESC NULLS LAST, i.id LIMIT 5)::text[] AS titles
FROM entities e
CROSS JOIN LATERAL (SELECT array_agg(s.connection_id ORDER BY s.connection_id) AS ids
                    FROM entity_sources s WHERE s.entity_id = e.id) own
LEFT JOIN domains d ON d.id = e.domain_id
WHERE e.status <> 'merged' AND own.ids IS NOT NULL
ORDER BY e.id;

-- name: ProfileHashes :many
SELECT entity_id, hash FROM entity_profiles;

-- name: UpsertProfile :exec
INSERT INTO entity_profiles (entity_id, text, hash, connections, updated_at)
VALUES (@entity_id, @text, @hash, @connections, now())
ON CONFLICT (entity_id) DO UPDATE SET text = EXCLUDED.text, hash = EXCLUDED.hash,
    connections = EXCLUDED.connections, updated_at = now();

-- name: DeleteStaleProfiles :exec
DELETE FROM entity_profiles p
WHERE NOT EXISTS (SELECT 1 FROM entities e WHERE e.id = p.entity_id AND e.status <> 'merged');

-- name: TrigramPairs :many
-- Pairs of active entities of one type with similar aliases.
-- vis:system
SELECT a.entity_id AS a_id, b.entity_id AS b_id, max(similarity(a.alias_norm, b.alias_norm))::float8 AS score
FROM entity_aliases a
JOIN entity_aliases b ON b.alias_norm % a.alias_norm AND a.entity_id < b.entity_id
JOIN entities ea ON ea.id = a.entity_id
JOIN entities eb ON eb.id = b.entity_id
WHERE ea.status <> 'merged' AND eb.status <> 'merged' AND ea.type = eb.type
  AND similarity(a.alias_norm, b.alias_norm) >= @min_score::float8
GROUP BY a.entity_id, b.entity_id;

-- name: AcronymPairs :many
-- Pairs where a one-word alias of 2 to 6 letters is the initials of an
-- alias of 2 to 6 words of the other entity.
-- vis:system
WITH words AS (
    SELECT a.entity_id, e.type, a.alias_norm, w.n,
           (SELECT string_agg(left(x.word, 1), '' ORDER BY x.ord)
            FROM unnest(string_to_array(a.alias_norm, ' ')) WITH ORDINALITY AS x (word, ord)) AS initials
    FROM entity_aliases a
    JOIN entities e ON e.id = a.entity_id
    CROSS JOIN LATERAL (SELECT cardinality(string_to_array(a.alias_norm, ' ')) AS n) w
    WHERE e.status <> 'merged'
)
SELECT DISTINCT least(s.entity_id, l.entity_id)::bigint AS a_id, greatest(s.entity_id, l.entity_id)::bigint AS b_id
FROM words s
JOIN words l ON l.initials = s.alias_norm AND l.type = s.type AND l.entity_id <> s.entity_id
WHERE s.n = 1 AND s.alias_norm ~ '^[a-z]{2,6}$' AND l.n BETWEEN 2 AND 6;

-- name: NeighbourPairs :many
-- Pairs of one type that share an owner or a parent.
SELECT least(a.subject_id, b.subject_id)::bigint AS a_id, greatest(a.subject_id, b.subject_id)::bigint AS b_id,
       count(DISTINCT a.object_entity_id)::int AS shared
FROM claims a
JOIN claims b ON b.predicate = a.predicate AND b.object_entity_id = a.object_entity_id AND b.subject_id <> a.subject_id
JOIN entities ea ON ea.id = a.subject_id
JOIN entities eb ON eb.id = b.subject_id
WHERE a.predicate IN ('owned_by', 'part_of') AND a.retracted_at IS NULL AND b.retracted_at IS NULL
  AND ea.status <> 'merged' AND eb.status <> 'merged' AND ea.type = eb.type
GROUP BY 1, 2;

-- name: Verdicts :many
SELECT a_id, b_id FROM pair_verdicts WHERE kind = @kind;

-- name: GetCalibration :one
SELECT kind, bins, reviewed, auto_apply FROM calibration WHERE kind = @kind;

-- name: ListCalibration :many
SELECT kind, bins, reviewed, auto_apply, updated_at FROM calibration ORDER BY kind;

-- name: ReviewedOutcomes :many
-- Reviewed outcomes of a kind, newest first. An undone auto-apply counts
-- as a rejection.
SELECT raw_p::float8 AS raw_p, band::text AS band, outcome = 'accepted' AS accepted
FROM decisions
WHERE kind = @kind AND outcome IN ('accepted', 'rejected', 'undone') AND raw_p IS NOT NULL AND band IS NOT NULL
ORDER BY reviewed_at DESC NULLS LAST, id DESC;

-- name: UpsertCalibration :one
INSERT INTO calibration (kind, bins, reviewed, high_precision, updated_at)
VALUES (@kind, @bins, @reviewed, @high_precision, now())
ON CONFLICT (kind) DO UPDATE SET bins = EXCLUDED.bins, reviewed = EXCLUDED.reviewed,
    high_precision = EXCLUDED.high_precision, updated_at = now()
RETURNING auto_apply;

-- name: SetAutoApply :exec
INSERT INTO calibration (kind, auto_apply) VALUES (@kind, @auto_apply)
ON CONFLICT (kind) DO UPDATE SET auto_apply = EXCLUDED.auto_apply, updated_at = now();

-- name: InsertDecision :one
INSERT INTO decisions (kind, subject_refs, input_hash, model, probs, raw_p, band, connections, outcome, reasons, at)
VALUES (@kind, @subject_refs, @input_hash, @model, @probs, @raw_p, @band, @connections, @outcome, @reasons, @at)
RETURNING id;

-- name: DecidedInput :one
-- Whether a decision of the same kind and input exists.
SELECT EXISTS (SELECT 1 FROM decisions WHERE kind = @kind AND input_hash = @input_hash)::boolean;

-- name: OpenPairCard :one
SELECT EXISTS (SELECT 1 FROM proposals p JOIN decisions d ON d.id = p.decision_id
               WHERE p.status = 'open' AND p.kind = 'merge' AND d.subject_refs @> @refs::text[])::boolean;

-- name: InsertDecisionCard :one
INSERT INTO proposals (kind, target, payload, via, connections, decision_id)
VALUES (@kind, @target, @payload, 'system', @connections, @decision_id)
RETURNING id;

-- name: VisibleCards :many
-- Open cards whose inputs are all visible (SI-18, REQ-REV-002).
SELECT p.id, p.kind, p.target, p.payload, p.created_at, p.decision_id, d.raw_p, d.band, d.reasons, d.subject_refs
FROM proposals p
LEFT JOIN decisions d ON d.id = p.decision_id
WHERE p.status = 'open' AND p.connections <@ @visible::bigint[]
  AND (sqlc.narg(kind)::text IS NULL OR p.kind = sqlc.narg(kind))
ORDER BY p.id
LIMIT @lim;

-- name: CardForUpdate :one
SELECT p.id, p.kind, p.status, p.connections, p.payload, p.decision_id, d.subject_refs, d.kind AS decision_kind
FROM proposals p
LEFT JOIN decisions d ON d.id = p.decision_id
WHERE p.id = @id
FOR UPDATE OF p;

-- name: DecideCard :exec
UPDATE proposals SET status = @status, reviewer_id = @reviewer_id, reason = @reason, decided_at = @decided_at WHERE id = @id;

-- name: SetOutcome :exec
UPDATE decisions SET outcome = @outcome, reviewed_at = @reviewed_at WHERE id = @id;

-- name: AddVerdict :exec
INSERT INTO pair_verdicts (a_id, b_id, kind, verdict, by)
VALUES (least(@x::bigint, @y::bigint), greatest(@x::bigint, @y::bigint), @kind, @verdict, @by)
ON CONFLICT (a_id, b_id, kind) DO UPDATE SET verdict = EXCLUDED.verdict, by = EXCLUDED.by, at = now();

-- name: VisibleDecisions :many
-- Decisions with an outcome whose inputs are all visible, newest first.
SELECT d.id, d.kind, d.subject_refs, d.raw_p, d.band, d.outcome, d.reasons, d.at, d.reviewed_at
FROM decisions d
WHERE d.outcome = @outcome AND d.connections <@ @visible::bigint[]
ORDER BY d.at DESC, d.id DESC
LIMIT @lim;

-- name: DecisionForUpdate :one
SELECT id, kind, outcome, connections, subject_refs, merge_id FROM decisions WHERE id = @id FOR UPDATE;

-- name: SetDecisionMerge :exec
UPDATE decisions SET merge_id = @merge_id WHERE id = @id;

-- name: EntityRefs :many
-- Keys, types and display names of entities that are visible.
SELECT e.id, e.key, e.type, coalesce(entity_display_name(e.id, @visible::bigint[]), e.key)::text AS name
FROM entities e
WHERE e.key = ANY(@keys::text[])
  AND EXISTS (SELECT 1 FROM entity_sources s WHERE s.entity_id = e.id AND s.connection_id = ANY(@visible::bigint[]));

-- name: SourcesOfKeys :many
SELECT id FROM entities WHERE key = ANY(@keys::text[]) ORDER BY id;

-- name: ProfilesOf :many
SELECT p.entity_id, e.key, p.text, p.connections FROM entity_profiles p JOIN entities e ON e.id = p.entity_id
WHERE p.entity_id = ANY(@ids::bigint[]);
