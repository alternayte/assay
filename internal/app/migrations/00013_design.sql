-- +goose Up
-- Source code passages carry their 1-based line range.
ALTER TABLE passages ADD COLUMN start_line integer;
ALTER TABLE passages ADD COLUMN end_line integer;

-- The design function writes design pages with its own endpoint, model and
-- budget.
ALTER TABLE model_functions DROP CONSTRAINT model_functions_function_check;
ALTER TABLE model_functions ADD CONSTRAINT model_functions_function_check
    CHECK (function IN ('answer', 'extract', 'compile', 'embed', 'rerank', 'decide', 'design'));
INSERT INTO model_functions (function) VALUES ('design');

-- The code globs of a repository connection. Empty turns code indexing off.
ALTER TABLE connections ADD COLUMN code_globs text[] NOT NULL DEFAULT '{}';

-- repo_system claims have the source type decision. Source priority ranks
-- it below catalog and registry claims.
ALTER TABLE claim_evidence DROP CONSTRAINT claim_evidence_source_type_check;
ALTER TABLE claim_evidence ADD CONSTRAINT claim_evidence_source_type_check
    CHECK (source_type IN ('registry', 'catalog', 'codeowners', 'jira', 'confluence', 'repository', 'review', 'decision'));

ALTER TABLE decisions DROP CONSTRAINT decisions_kind_check;
ALTER TABLE decisions ADD CONSTRAINT decisions_kind_check
    CHECK (kind IN ('same_entity', 'mention_link', 'describes', 'equivalent', 'still_supports', 'term_worthy', 'repo_system'));

ALTER TABLE proposals DROP CONSTRAINT proposals_kind_check;
ALTER TABLE proposals ADD CONSTRAINT proposals_kind_check
    CHECK (kind IN ('correction', 'finding', 'merge', 'mention_link', 'unnamed', 'new_term', 'term_edit', 'conflict', 'needs_edit',
        'repo_system', 'design_problem', 'design_edit', 'design_update'));

-- A component of a repository: one section unit of its design page, found
-- from the code structure. passage_set_hash covers the sorted passage
-- hashes of all its files, so added and removed code changes it. The
-- digest is the internal summary that later steps read in place of the
-- code; digest_cites maps its markers to passages.
CREATE TABLE design_components (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entity_id bigint NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    connection_id bigint NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    repo text NOT NULL,
    path text NOT NULL,
    name text NOT NULL,
    kind text NOT NULL,
    files integer NOT NULL DEFAULT 0,
    passages integer NOT NULL DEFAULT 0,
    passage_set_hash bytea NOT NULL,
    digest text NOT NULL DEFAULT '',
    digest_cites jsonb NOT NULL DEFAULT '{}',
    digest_hash bytea,
    catalog_key text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (entity_id, connection_id, path)
);

-- A section of a design page. slug names it inside the page, such as
-- overview or component:internal/sync.
CREATE TABLE design_sections (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entity_id bigint NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    slug text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('overview', 'architecture', 'component', 'data', 'repository')),
    title text NOT NULL,
    ordinal integer NOT NULL DEFAULT 0,
    component_path text NOT NULL DEFAULT '',
    -- input_hash is the input of the newest build of a repository section.
    -- A model version with another input hash is stale.
    input_hash bytea,
    removed_at timestamptz,
    UNIQUE (entity_id, slug)
);

-- A version of a section. A model version belongs to one visibility key:
-- the hash of its connections. A user version is pinned: regeneration
-- never replaces it, and a reader sees it only when all its connections are
-- visible. cites maps markers to passages and claims. input_hash covers the
-- digests and sources of a model version; a different hash makes it stale.
CREATE TABLE design_versions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    section_id bigint NOT NULL REFERENCES design_sections (id) ON DELETE CASCADE,
    author text NOT NULL CHECK (author IN ('model', 'user')),
    user_id uuid REFERENCES auth_users (id) ON DELETE SET NULL,
    visibility_key bytea NOT NULL,
    connections bigint[] NOT NULL,
    markdown text NOT NULL,
    diagram text NOT NULL DEFAULT '',
    cites jsonb NOT NULL DEFAULT '{}',
    input_hash bytea NOT NULL,
    lint jsonb NOT NULL DEFAULT '[]',
    model text NOT NULL DEFAULT '',
    note text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX design_versions_section ON design_versions (section_id, id DESC);
CREATE INDEX design_versions_key ON design_versions (section_id, visibility_key, id DESC);

-- One design build of a repository: what it built and why it stopped.
CREATE TABLE design_runs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entity_id bigint NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    connection_id bigint NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    trigger text NOT NULL CHECK (trigger IN ('sync', 'manual')),
    status text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'succeeded', 'stopped', 'failed')),
    tokens integer NOT NULL DEFAULT 0,
    digests integer NOT NULL DEFAULT 0,
    sections integer NOT NULL DEFAULT 0,
    reason text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);
CREATE INDEX design_runs_entity ON design_runs (entity_id, id DESC);

-- A purge also deletes the design pages that hold text of the connection.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION purge_connection_outputs() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM decisions WHERE OLD.id = ANY(connections);
    DELETE FROM proposals WHERE OLD.id = ANY(connections);
    DELETE FROM pages WHERE OLD.id = ANY(connections);
    DELETE FROM design_versions WHERE OLD.id = ANY(connections);
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
DROP TABLE design_runs;
DROP TABLE design_versions;
DROP TABLE design_sections;
DROP TABLE design_components;
DELETE FROM proposals WHERE kind IN ('repo_system', 'design_problem', 'design_edit', 'design_update');
ALTER TABLE proposals DROP CONSTRAINT proposals_kind_check;
ALTER TABLE proposals ADD CONSTRAINT proposals_kind_check
    CHECK (kind IN ('correction', 'finding', 'merge', 'mention_link', 'unnamed', 'new_term', 'term_edit', 'conflict', 'needs_edit'));
DELETE FROM decisions WHERE kind = 'repo_system';
ALTER TABLE decisions DROP CONSTRAINT decisions_kind_check;
ALTER TABLE decisions ADD CONSTRAINT decisions_kind_check
    CHECK (kind IN ('same_entity', 'mention_link', 'describes', 'equivalent', 'still_supports', 'term_worthy'));
DELETE FROM claim_evidence WHERE source_type = 'decision';
ALTER TABLE claim_evidence DROP CONSTRAINT claim_evidence_source_type_check;
ALTER TABLE claim_evidence ADD CONSTRAINT claim_evidence_source_type_check
    CHECK (source_type IN ('registry', 'catalog', 'codeowners', 'jira', 'confluence', 'repository', 'review'));
ALTER TABLE connections DROP COLUMN code_globs;
DELETE FROM model_functions WHERE function = 'design';
ALTER TABLE model_functions DROP CONSTRAINT model_functions_function_check;
ALTER TABLE model_functions ADD CONSTRAINT model_functions_function_check
    CHECK (function IN ('answer', 'extract', 'compile', 'embed', 'rerank', 'decide'));
ALTER TABLE passages DROP COLUMN end_line;
ALTER TABLE passages DROP COLUMN start_line;
