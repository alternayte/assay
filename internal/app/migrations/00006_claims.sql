-- +goose Up
-- A proposal is a review card that a person or a job opens: a correction, a
-- finding, a merge, a conflict and the other card kinds. Its connections
-- are the connections of its evidence.
CREATE TABLE proposals (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('correction', 'finding', 'merge', 'mention_link', 'unnamed', 'new_term', 'term_edit',
        'conflict', 'needs_edit')),
    target text NOT NULL DEFAULT '',
    payload jsonb NOT NULL DEFAULT '{}',
    proposer_id uuid REFERENCES auth_users (id) ON DELETE SET NULL,
    via text NOT NULL DEFAULT 'system' CHECK (via IN ('ui', 'mcp', 'system')),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'accepted', 'rejected', 'needs_evidence')),
    reviewer_id uuid REFERENCES auth_users (id) ON DELETE SET NULL,
    reason text NOT NULL DEFAULT '',
    connections bigint[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz
);
CREATE INDEX proposals_status ON proposals (status, kind);

-- Evidence of an open proposal makes a claim Proposed.
ALTER TABLE claim_evidence DROP CONSTRAINT claim_evidence_kind_check;
ALTER TABLE claim_evidence ADD CONSTRAINT claim_evidence_kind_check CHECK (kind IN ('field', 'passage', 'human', 'proposal'));
ALTER TABLE claim_evidence ADD COLUMN proposal_id bigint REFERENCES proposals (id) ON DELETE CASCADE;

-- A reviewer chose one claim of a conflict set. New visible support for a
-- loser reopens the resolution.
CREATE TABLE resolutions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    subject_id bigint NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    predicate text NOT NULL,
    scope_key text NOT NULL DEFAULT '',
    chosen_claim_id bigint NOT NULL REFERENCES claims (id) ON DELETE CASCADE,
    decided_by uuid REFERENCES auth_users (id) ON DELETE SET NULL,
    decided_at timestamptz NOT NULL DEFAULT now(),
    reopened_at timestamptz
);
CREATE INDEX resolutions_subject ON resolutions (subject_id, predicate);

-- A merge points the losers to the winner. Split undoes it.
CREATE TABLE merges (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    winner_id bigint NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    loser_ids bigint[] NOT NULL,
    canonical_name text NOT NULL DEFAULT '',
    by uuid REFERENCES auth_users (id) ON DELETE SET NULL,
    auto boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    undone_at timestamptz
);
CREATE INDEX merges_winner ON merges (winner_id) WHERE undone_at IS NULL;

-- The display name of an entity for a visible set (REQ-ENT-007): the
-- registry name first, then the canonical name of an active merge when the
-- reader sees a source of every merged entity, then the name from the
-- highest-priority source. Only aliases from visible connections count, also
-- those of merged entities, so a merge never shows a hidden name (SI-16).
-- +goose StatementBegin
CREATE FUNCTION entity_display_name(entity bigint, visible bigint[]) RETURNS text
LANGUAGE sql STABLE AS $$
    WITH names AS (
        SELECT a.alias, a.alias_norm, a.is_name, k.kind, a.entity_id = entity AS own
        FROM entity_aliases a
        JOIN connections k ON k.id = a.connection_id
        WHERE (a.entity_id = entity OR a.entity_id IN (SELECT x.id FROM entities x WHERE x.merged_into = entity))
          AND a.connection_id = ANY(visible)
    )
    SELECT coalesce(
        (SELECT n.alias FROM names n WHERE n.kind = 'registry' AND n.is_name ORDER BY n.own DESC LIMIT 1),
        (SELECT m.canonical_name FROM merges m
         WHERE m.winner_id = entity AND m.undone_at IS NULL AND m.canonical_name <> ''
           AND NOT EXISTS (SELECT 1 FROM entities x
                           WHERE (x.id = entity OR x.merged_into = entity)
                             AND NOT EXISTS (SELECT 1 FROM entity_sources es
                                             WHERE es.entity_id = x.id AND es.connection_id = ANY(visible)))
         ORDER BY m.id DESC LIMIT 1),
        (SELECT n.alias FROM names n
         ORDER BY n.is_name DESC,
                  CASE n.kind WHEN 'registry' THEN 0 WHEN 'github' THEN 1 WHEN 'azdo' THEN 1 WHEN 'jira' THEN 2 ELSE 3 END,
                  n.own DESC, length(n.alias)
         LIMIT 1))
$$;
-- +goose StatementEnd

-- The extraction cache: one row per item, passage hash and extractor
-- version. output keeps the validated model output.
CREATE TABLE passage_extractions (
    item_id bigint NOT NULL REFERENCES source_items (id) ON DELETE CASCADE,
    passage_hash bytea NOT NULL,
    extractor_version text NOT NULL,
    accepted integer NOT NULL DEFAULT 0,
    rejected integer NOT NULL DEFAULT 0,
    reasons jsonb NOT NULL DEFAULT '[]',
    output jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (item_id, passage_hash, extractor_version)
);

-- +goose Down
DROP TABLE passage_extractions;
DROP FUNCTION entity_display_name(bigint, bigint[]);
DROP TABLE merges;
DROP TABLE resolutions;
ALTER TABLE claim_evidence DROP COLUMN proposal_id;
ALTER TABLE claim_evidence DROP CONSTRAINT claim_evidence_kind_check;
ALTER TABLE claim_evidence ADD CONSTRAINT claim_evidence_kind_check CHECK (kind IN ('field', 'passage', 'human'));
DROP TABLE proposals;
