-- +goose Up
-- A Backstage connection reads the software catalog of a Backstage backend.
ALTER TABLE connections DROP CONSTRAINT connections_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_kind_check CHECK (kind IN ('registry', 'confluence', 'jira', 'github', 'azdo', 'backstage'));

-- +goose Down
DELETE FROM connections WHERE kind = 'backstage';
ALTER TABLE connections DROP CONSTRAINT connections_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_kind_check CHECK (kind IN ('registry', 'confluence', 'jira', 'github', 'azdo'));
