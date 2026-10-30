-- +goose Up
-- A purge also deletes the entity profiles that hold text of the connection.
-- The next profile run builds them again from the remaining sources.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION purge_connection_outputs() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM decisions WHERE OLD.id = ANY(connections);
    DELETE FROM proposals WHERE OLD.id = ANY(connections);
    DELETE FROM pages WHERE OLD.id = ANY(connections);
    DELETE FROM entity_profiles WHERE OLD.id = ANY(connections);
    RETURN OLD;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION purge_connection_outputs() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM decisions WHERE OLD.id = ANY(connections);
    DELETE FROM proposals WHERE OLD.id = ANY(connections);
    DELETE FROM pages WHERE OLD.id = ANY(connections);
    RETURN OLD;
END
$$;
-- +goose StatementEnd
