-- +goose Up
-- A person edits an entity from its page. An edited alias is written in
-- each source connection of the entity, so it shows only to the users who
-- see the entity. An edited name comes first in the display name.
ALTER TABLE entity_aliases ADD COLUMN edited boolean NOT NULL DEFAULT false;
-- A type that a person set stays when a connector writes the entity again.
ALTER TABLE entities ADD COLUMN type_edited_at timestamptz;
-- A removed alias. The connector and mention writers skip it, so the next
-- sync does not add it again.
CREATE TABLE alias_suppressions (
    entity_id bigint NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    alias_norm text NOT NULL,
    by uuid REFERENCES auth_users (id) ON DELETE SET NULL,
    at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (entity_id, alias_norm)
);
-- The keys of deleted entities. Resolution does not make them again.
CREATE TABLE entity_deletions (
    key text PRIMARY KEY,
    by uuid REFERENCES auth_users (id) ON DELETE SET NULL,
    at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE proposals DROP CONSTRAINT proposals_kind_check;
ALTER TABLE proposals ADD CONSTRAINT proposals_kind_check
    CHECK (kind IN ('correction', 'finding', 'merge', 'mention_link', 'unnamed', 'new_term', 'term_edit', 'conflict', 'needs_edit',
        'repo_system', 'design_problem', 'design_edit', 'design_update', 'entity_edit'));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION entity_display_name(entity bigint, visible bigint[]) RETURNS text
LANGUAGE sql STABLE AS $$
    WITH names AS (
        SELECT a.alias, a.alias_norm, a.is_name, a.edited, k.kind, a.entity_id = entity AS own
        FROM entity_aliases a
        JOIN connections k ON k.id = a.connection_id
        WHERE a.entity_id = ANY(entity_members(entity))
          AND a.connection_id = ANY(visible)
    )
    SELECT coalesce(
        (SELECT n.alias FROM names n WHERE n.edited AND n.is_name ORDER BY n.own DESC LIMIT 1),
        (SELECT n.alias FROM names n WHERE n.kind = 'registry' AND n.is_name ORDER BY n.own DESC LIMIT 1),
        (SELECT m.canonical_name FROM merges m
         WHERE m.winner_id = entity AND m.undone_at IS NULL AND m.canonical_name <> ''
           AND NOT EXISTS (SELECT 1 FROM entities x
                           WHERE x.id = ANY(entity_members(entity))
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

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION entity_display_name(entity bigint, visible bigint[]) RETURNS text
LANGUAGE sql STABLE AS $$
    WITH names AS (
        SELECT a.alias, a.alias_norm, a.is_name, k.kind, a.entity_id = entity AS own
        FROM entity_aliases a
        JOIN connections k ON k.id = a.connection_id
        WHERE a.entity_id = ANY(entity_members(entity))
          AND a.connection_id = ANY(visible)
    )
    SELECT coalesce(
        (SELECT n.alias FROM names n WHERE n.kind = 'registry' AND n.is_name ORDER BY n.own DESC LIMIT 1),
        (SELECT m.canonical_name FROM merges m
         WHERE m.winner_id = entity AND m.undone_at IS NULL AND m.canonical_name <> ''
           AND NOT EXISTS (SELECT 1 FROM entities x
                           WHERE x.id = ANY(entity_members(entity))
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
DELETE FROM proposals WHERE kind = 'entity_edit';
ALTER TABLE proposals DROP CONSTRAINT proposals_kind_check;
ALTER TABLE proposals ADD CONSTRAINT proposals_kind_check
    CHECK (kind IN ('correction', 'finding', 'merge', 'mention_link', 'unnamed', 'new_term', 'term_edit', 'conflict', 'needs_edit',
        'repo_system', 'design_problem', 'design_edit', 'design_update'));
DROP TABLE entity_deletions;
DROP TABLE alias_suppressions;
ALTER TABLE entities DROP COLUMN type_edited_at;
ALTER TABLE entity_aliases DROP COLUMN edited;
