-- +goose Up
-- GitLab and Bitbucket Data Center connections read repositories as GitHub
-- and Azure DevOps do. Their names rank with the other repository names.
ALTER TABLE connections DROP CONSTRAINT connections_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_kind_check
    CHECK (kind IN ('registry', 'confluence', 'jira', 'github', 'azdo', 'backstage', 'gitlab', 'bitbucket'));

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
                  CASE n.kind WHEN 'registry' THEN 0 WHEN 'github' THEN 1 WHEN 'azdo' THEN 1 WHEN 'gitlab' THEN 1 WHEN 'bitbucket' THEN 1 WHEN 'jira' THEN 2 ELSE 3 END,
                  n.own DESC, length(n.alias)
         LIMIT 1))
$$;
-- +goose StatementEnd

-- +goose Down
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
DELETE FROM connections WHERE kind IN ('gitlab', 'bitbucket');
ALTER TABLE connections DROP CONSTRAINT connections_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_kind_check CHECK (kind IN ('registry', 'confluence', 'jira', 'github', 'azdo', 'backstage'));
