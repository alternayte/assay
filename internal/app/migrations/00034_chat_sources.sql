-- +goose Up
-- A teams or a slack connection reads the threads of chat channels. Each
-- thread is an item, and claims from its messages have the source type of
-- the connection kind.
ALTER TABLE connections DROP CONSTRAINT connections_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_kind_check
    CHECK (kind IN ('registry', 'confluence', 'jira', 'github', 'azdo', 'backstage', 'gitlab', 'bitbucket', 'web', 'teams', 'slack'));
ALTER TABLE claim_evidence DROP CONSTRAINT claim_evidence_source_type_check;
ALTER TABLE claim_evidence ADD CONSTRAINT claim_evidence_source_type_check
    CHECK (source_type IN ('registry', 'catalog', 'codeowners', 'jira', 'confluence', 'repository', 'review', 'decision', 'web', 'teams', 'slack'));

-- +goose Down
DELETE FROM claim_evidence WHERE source_type IN ('teams', 'slack');
ALTER TABLE claim_evidence DROP CONSTRAINT claim_evidence_source_type_check;
ALTER TABLE claim_evidence ADD CONSTRAINT claim_evidence_source_type_check
    CHECK (source_type IN ('registry', 'catalog', 'codeowners', 'jira', 'confluence', 'repository', 'review', 'decision', 'web'));
DELETE FROM connections WHERE kind IN ('teams', 'slack');
ALTER TABLE connections DROP CONSTRAINT connections_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_kind_check
    CHECK (kind IN ('registry', 'confluence', 'jira', 'github', 'azdo', 'backstage', 'gitlab', 'bitbucket', 'web'));
