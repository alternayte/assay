-- +goose Up
-- Source metadata of an entity that is not a claim, such as the email of a
-- Jira person. The switch of a connection to Atlassian Cloud matches people
-- by it.
ALTER TABLE entities ADD COLUMN metadata jsonb NOT NULL DEFAULT '{}';
CREATE INDEX entities_email ON entities (lower(metadata->>'email')) WHERE type = 'person' AND metadata ? 'email';

-- The host of a Data Center connection that switched to Cloud. The next
-- sync rewrites the keys of the connection from it and then clears it.
ALTER TABLE connections ADD COLUMN rewrite_from text NOT NULL DEFAULT '';

-- The key rewrite of a run after a switch: the old and new host and the
-- counts of rewritten items, entities and people, and of unmatched people.
ALTER TABLE sync_runs ADD COLUMN rewrite jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE sync_runs DROP COLUMN rewrite;
ALTER TABLE connections DROP COLUMN rewrite_from;
DROP INDEX entities_email;
ALTER TABLE entities DROP COLUMN metadata;
