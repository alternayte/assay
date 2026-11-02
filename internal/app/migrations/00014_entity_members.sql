-- +goose Up
-- An entity and the entities merged into it. "id = e OR id IN (merged
-- into e)" keeps the planner from using an index, so lists of many
-- entities scanned whole tables per row. entity_members gives an array that
-- an index scan takes.
CREATE INDEX entities_merged_into ON entities (merged_into) WHERE merged_into IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION entity_members(entity bigint) RETURNS bigint[]
LANGUAGE sql STABLE AS $$
    SELECT entity || coalesce(array_agg(x.id), '{}') FROM entities x WHERE x.merged_into = entity
$$;
-- +goose StatementEnd

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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION entity_visible_key(entity bigint, visible bigint[]) RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT x.key FROM entities x
    WHERE x.id = ANY(entity_members(entity))
      AND EXISTS (SELECT 1 FROM entity_sources es WHERE es.entity_id = x.id AND es.connection_id = ANY(visible))
    ORDER BY x.id = entity DESC, x.created_at, x.id
    LIMIT 1
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

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION entity_visible_key(entity bigint, visible bigint[]) RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT x.key FROM entities x
    WHERE (x.id = entity OR x.merged_into = entity)
      AND EXISTS (SELECT 1 FROM entity_sources es WHERE es.entity_id = x.id AND es.connection_id = ANY(visible))
    ORDER BY x.id = entity DESC, x.created_at, x.id
    LIMIT 1
$$;
-- +goose StatementEnd

DROP FUNCTION entity_members(bigint);
DROP INDEX entities_merged_into;
