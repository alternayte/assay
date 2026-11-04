-- +goose Up
-- Contracts: the operations, events and data models that Assay parses from
-- spec files or infers from code.
ALTER TABLE entities DROP CONSTRAINT entities_type_check;
ALTER TABLE entities ADD CONSTRAINT entities_type_check CHECK (type IN ('person', 'team', 'service', 'api', 'system', 'resource',
    'repository', 'project', 'initiative', 'issue', 'document', 'decision', 'environment', 'term', 'operation', 'event', 'data_model'));

-- +goose Down
DELETE FROM entities WHERE type IN ('operation', 'event', 'data_model');
ALTER TABLE entities DROP CONSTRAINT entities_type_check;
ALTER TABLE entities ADD CONSTRAINT entities_type_check CHECK (type IN ('person', 'team', 'service', 'api', 'system', 'resource',
    'repository', 'project', 'initiative', 'issue', 'document', 'decision', 'environment', 'term'));
