-- +goose Up
-- Repository history: a merged pull request is an entity of type change.
ALTER TABLE entities DROP CONSTRAINT entities_type_check;
ALTER TABLE entities ADD CONSTRAINT entities_type_check CHECK (type IN ('person', 'team', 'service', 'api', 'system', 'resource',
    'repository', 'project', 'initiative', 'issue', 'document', 'decision', 'environment', 'term', 'operation', 'event', 'data_model',
    'change'));

-- +goose Down
DELETE FROM entities WHERE type = 'change';
ALTER TABLE entities DROP CONSTRAINT entities_type_check;
ALTER TABLE entities ADD CONSTRAINT entities_type_check CHECK (type IN ('person', 'team', 'service', 'api', 'system', 'resource',
    'repository', 'project', 'initiative', 'issue', 'document', 'decision', 'environment', 'term', 'operation', 'event', 'data_model'));
