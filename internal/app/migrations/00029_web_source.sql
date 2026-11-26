-- +goose Up
-- A web connection reads the pages of a documentation web site. Claims from
-- its pages have the source type web.
ALTER TABLE connections DROP CONSTRAINT connections_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_kind_check
    CHECK (kind IN ('registry', 'confluence', 'jira', 'github', 'azdo', 'backstage', 'gitlab', 'bitbucket', 'web'));
ALTER TABLE claim_evidence DROP CONSTRAINT claim_evidence_source_type_check;
ALTER TABLE claim_evidence ADD CONSTRAINT claim_evidence_source_type_check
    CHECK (source_type IN ('registry', 'catalog', 'codeowners', 'jira', 'confluence', 'repository', 'review', 'decision', 'web'));

-- +goose Down
DELETE FROM claim_evidence WHERE source_type = 'web';
ALTER TABLE claim_evidence DROP CONSTRAINT claim_evidence_source_type_check;
ALTER TABLE claim_evidence ADD CONSTRAINT claim_evidence_source_type_check
    CHECK (source_type IN ('registry', 'catalog', 'codeowners', 'jira', 'confluence', 'repository', 'review', 'decision'));
DELETE FROM connections WHERE kind = 'web';
ALTER TABLE connections DROP CONSTRAINT connections_kind_check;
ALTER TABLE connections ADD CONSTRAINT connections_kind_check
    CHECK (kind IN ('registry', 'confluence', 'jira', 'github', 'azdo', 'backstage', 'gitlab', 'bitbucket'));
