-- +goose Up
-- The key that a reader sees for an entity and the entities merged into
-- it: the own key when the reader sees a source of the entity, else the key
-- of the oldest merged entity with a visible source. A merge into a hidden
-- entity never shows its key (SI-02).
-- +goose StatementBegin
CREATE FUNCTION entity_visible_key(entity bigint, visible bigint[]) RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT x.key FROM entities x
    WHERE (x.id = entity OR x.merged_into = entity)
      AND EXISTS (SELECT 1 FROM entity_sources es WHERE es.entity_id = x.id AND es.connection_id = ANY(visible))
    ORDER BY x.id = entity DESC, x.created_at, x.id
    LIMIT 1
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION entity_visible_key(bigint, bigint[]);
