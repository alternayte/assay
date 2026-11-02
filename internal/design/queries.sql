-- name: CodeConnection :one
SELECT id, kind, name, code_globs, status FROM connections WHERE id = @id;

-- name: RepositoryItems :many
-- The items of the repositories of a connection: source files and
-- knowledge files, with the repository entity of their source file
-- listing. Build reads only the connection that it builds.
SELECT i.id, i.external_id, i.kind, i.title, i.url, split_part(i.external_id, ':', 1)::text AS repo,
       substr(i.external_id, length(split_part(i.external_id, ':', 1)) + 2)::text AS path,
       (SELECT count(*) FROM passages p WHERE p.item_id = i.id AND p.connection_id = ANY(@visible::bigint[]))::integer AS passages
FROM source_items i
WHERE i.connection_id = ANY(@visible::bigint[]) AND i.connection_id = @connection_id
ORDER BY i.external_id;

-- name: RepositoryEntity :one
-- The repository entity of a repository ID in a connection: the entity of
-- type repository with the ID as an alias in that connection.
SELECT e.id, e.key, coalesce(entity_display_name(e.id, @visible::bigint[]), e.key)::text AS name
FROM entities e
JOIN entity_aliases a ON a.entity_id = e.id
WHERE e.type = 'repository' AND e.status = 'active' AND a.connection_id = ANY(@visible::bigint[]) AND a.alias_norm = @alias_norm
ORDER BY e.id
LIMIT 1;

-- name: ItemPassages :many
-- The passages of items in order.
SELECT p.item_id, p.hash, p.ordinal, p.kind, p.heading_path, p.text, p.norm, p.start_line, p.end_line
FROM passages p
WHERE p.item_id = ANY(@items::bigint[]) AND p.connection_id = ANY(@visible::bigint[])
ORDER BY p.item_id, p.ordinal;

-- name: CurrentPassages :many
-- Which of the cited passages still exist.
SELECT p.item_id, p.hash
FROM passages p
WHERE p.item_id = ANY(@items::bigint[]) AND p.connection_id = ANY(@visible::bigint[]);

-- name: Components :many
SELECT * FROM design_components WHERE entity_id = @entity_id AND connection_id = @connection_id ORDER BY path;

-- name: UpsertComponent :one
INSERT INTO design_components (entity_id, connection_id, repo, path, name, kind, files, passages, passage_set_hash, catalog_key, updated_at)
VALUES (@entity_id, @connection_id, @repo, @path, @name, @kind, @files, @passages, @passage_set_hash, @catalog_key, @now)
ON CONFLICT (entity_id, connection_id, path) DO UPDATE
SET repo = EXCLUDED.repo, name = EXCLUDED.name, kind = EXCLUDED.kind, files = EXCLUDED.files, passages = EXCLUDED.passages,
    passage_set_hash = EXCLUDED.passage_set_hash, catalog_key = EXCLUDED.catalog_key, updated_at = EXCLUDED.updated_at
RETURNING *;

-- name: SetDigest :exec
UPDATE design_components SET digest = @digest, digest_cites = @digest_cites, digest_hash = @digest_hash, updated_at = @now
WHERE id = @id;

-- name: DeleteComponents :exec
-- The components that the code no longer has.
DELETE FROM design_components WHERE entity_id = @entity_id AND connection_id = @connection_id AND NOT (path = ANY(@keep::text[]));

-- name: UpsertSection :one
INSERT INTO design_sections (entity_id, slug, kind, title, ordinal, component_path)
VALUES (@entity_id, @slug, @kind, @title, @ordinal, @component_path)
ON CONFLICT (entity_id, slug) DO UPDATE
SET kind = EXCLUDED.kind, title = EXCLUDED.title, ordinal = EXCLUDED.ordinal, component_path = EXCLUDED.component_path, removed_at = NULL
RETURNING id;

-- name: RemoveSections :exec
-- Sections of components that the code no longer has.
UPDATE design_sections SET removed_at = @now
WHERE entity_id = @entity_id AND kind = @kind AND removed_at IS NULL AND NOT (slug = ANY(@keep::text[]));

-- name: Sections :many
SELECT * FROM design_sections WHERE entity_id = @entity_id AND removed_at IS NULL ORDER BY ordinal, slug;

-- name: SectionBySlug :one
SELECT * FROM design_sections WHERE entity_id = @entity_id AND slug = @slug AND removed_at IS NULL;

-- name: SectionByID :one
SELECT s.*, e.key AS entity_key FROM design_sections s JOIN entities e ON e.id = s.entity_id WHERE s.id = @id;

-- name: Versions :many
-- The versions of sections, newest first.
SELECT * FROM design_versions WHERE section_id = ANY(@sections::bigint[]) ORDER BY section_id, id DESC;

-- name: LatestModelVersion :one
SELECT * FROM design_versions WHERE section_id = @section_id AND author = 'model' AND visibility_key = @visibility_key
ORDER BY id DESC LIMIT 1;

-- name: LatestUserVersion :one
SELECT * FROM design_versions WHERE section_id = @section_id AND author = 'user' ORDER BY id DESC LIMIT 1;

-- name: GetVersion :one
SELECT * FROM design_versions WHERE id = @id;

-- name: InsertVersion :one
INSERT INTO design_versions (section_id, author, user_id, visibility_key, connections, markdown, diagram, cites, input_hash, lint, model, note,
                             created_at)
VALUES (@section_id, @author, @user_id, @visibility_key, @connections, @markdown, @diagram, @cites, @input_hash, @lint, @model, @note, @now)
RETURNING id;

-- name: StartRun :one
INSERT INTO design_runs (entity_id, connection_id, trigger, started_at) VALUES (@entity_id, @connection_id, @trigger, @now) RETURNING id;

-- name: FinishRun :exec
UPDATE design_runs SET status = @status, tokens = @tokens, digests = @digests, sections = @sections, reason = @reason, finished_at = @now
WHERE id = @id;

-- name: LastRun :one
SELECT * FROM design_runs WHERE entity_id = @entity_id ORDER BY id DESC LIMIT 1;

-- name: FailStaleRuns :exec
-- Runs that stopped without a result.
UPDATE design_runs SET status = 'failed', reason = 'The build stopped without a result.', finished_at = @now
WHERE status = 'running' AND started_at < @before;

-- name: OpenSectionCard :one
-- An open card of a kind for a section, or a rejected update card for the
-- same draft input.
SELECT EXISTS (SELECT 1 FROM proposals
               WHERE kind = @kind AND (payload->>'sectionId')::bigint = @section_id::bigint
                 AND (status = 'open' OR (kind = 'design_update' AND status = 'rejected' AND payload->>'inputHash' = @input_hash::text)))::boolean;

-- name: InsertCard :one
INSERT INTO proposals (kind, target, payload, proposer_id, via, connections)
VALUES (@kind, @target, @payload, @proposer_id, @via, @connections)
RETURNING id;

-- name: EntityByKey :one
SELECT id, key, type FROM entities WHERE key = @key AND status = 'active';

-- name: RepositoryLinks :many
-- The repositories of design pages: entities with design sections. Used to
-- find the connection of a repository page.
SELECT DISTINCT c.entity_id, c.connection_id FROM design_components c WHERE c.entity_id = ANY(@entities::bigint[]);

-- name: SetSectionInput :exec
UPDATE design_sections SET input_hash = @input_hash WHERE id = @id;
