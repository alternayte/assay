-- +goose Up
-- status_epoch counts the status changes of a connection. A run records the
-- epoch at its start. Only a reconciliation that started in the current
-- epoch ends a resume, so that a run from before a pause or a scope change
-- does not show the content again.
ALTER TABLE connections ADD COLUMN status_epoch bigint NOT NULL DEFAULT 0;
ALTER TABLE sync_runs ADD COLUMN status_epoch bigint NOT NULL DEFAULT 0;

-- A purge deletes the cards that name the connection. The evidence of an
-- accepted card from other connections is human evidence, so it stays and
-- loses only the link to the card.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION purge_connection_outputs() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE claim_evidence ev SET kind = 'human', proposal_id = NULL, updated_at = now()
    FROM proposals p
    WHERE ev.proposal_id = p.id AND p.status = 'accepted' AND OLD.id = ANY(p.connections) AND ev.connection_id <> OLD.id;
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
    DELETE FROM entity_profiles WHERE OLD.id = ANY(connections);
    RETURN OLD;
END
$$;
-- +goose StatementEnd
ALTER TABLE sync_runs DROP COLUMN status_epoch;
ALTER TABLE connections DROP COLUMN status_epoch;
