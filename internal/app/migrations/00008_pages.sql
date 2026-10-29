-- +goose Up
-- The compiled summary of an entity page for one visibility key: the hash
-- of the sorted visible connections with evidence for the entity. The
-- deterministic sections are computed per request. claim_set_hash is the
-- hash of the input claims; a different hash on read makes the page stale.
-- claim_ids are the input claims, which stay in use (REQ-RES-009).
CREATE TABLE pages (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entity_id bigint NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    visibility_key bytea NOT NULL,
    connections bigint[] NOT NULL,
    claim_set_hash bytea NOT NULL,
    claim_ids bigint[] NOT NULL DEFAULT '{}',
    summary_md text NOT NULL DEFAULT '',
    citations jsonb NOT NULL DEFAULT '[]',
    lint jsonb NOT NULL DEFAULT '[]',
    state text NOT NULL CHECK (state IN ('fresh', 'insufficient', 'needs_edit')),
    model text NOT NULL DEFAULT '',
    compiled_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (entity_id, visibility_key)
);
CREATE INDEX pages_claims ON pages USING gin (claim_ids);

-- A purge also deletes the pages that hold text of the connection.
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

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION purge_connection_outputs() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM decisions WHERE OLD.id = ANY(connections);
    DELETE FROM proposals WHERE OLD.id = ANY(connections);
    RETURN OLD;
END
$$;
-- +goose StatementEnd
DROP TABLE pages;
