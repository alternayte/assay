-- +goose Up
CREATE TABLE domains (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX domains_name_key ON domains (lower(name));

-- One configured source system. Content of a connection is visible only to
-- its owner and to the groups in connection_visibility, and only while the
-- status is active.
CREATE TABLE connections (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('registry', 'confluence', 'jira', 'github', 'azdo')),
    name text NOT NULL,
    base_url text NOT NULL DEFAULT '',
    secret_id bigint REFERENCES secrets (id) ON DELETE SET NULL,
    scope jsonb NOT NULL DEFAULT '{}',
    options jsonb NOT NULL DEFAULT '{}',
    include_globs text[] NOT NULL DEFAULT '{}',
    exclude_globs text[] NOT NULL DEFAULT '{}',
    sync_minutes integer NOT NULL DEFAULT 30 CHECK (sync_minutes >= 5),
    reconcile_hours integer NOT NULL DEFAULT 24 CHECK (reconcile_hours >= 1),
    rate_concurrency integer NOT NULL DEFAULT 4 CHECK (rate_concurrency BETWEEN 1 AND 32),
    rate_per_second numeric(8, 2) NOT NULL DEFAULT 5 CHECK (rate_per_second > 0),
    owner_id uuid REFERENCES auth_users (id) ON DELETE SET NULL,
    -- resuming: the content stays hidden until a reconciliation succeeds.
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'resuming')),
    status_reason text NOT NULL DEFAULT '',
    default_domain_id bigint REFERENCES domains (id) ON DELETE SET NULL,
    cursor text NOT NULL DEFAULT '',
    last_sync_at timestamptz,
    last_reconcile_at timestamptz,
    permissions_checked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX connections_name_key ON connections (lower(name));
CREATE UNIQUE INDEX connections_one_registry ON connections (kind) WHERE kind = 'registry';

CREATE TABLE connection_visibility (
    connection_id bigint NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    group_id bigint NOT NULL REFERENCES groups (id) ON DELETE CASCADE,
    confirmed_by uuid REFERENCES auth_users (id) ON DELETE SET NULL,
    confirmed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (connection_id, group_id)
);

-- The registry is a built-in connection, shared with Everyone.
INSERT INTO connections (kind, name) VALUES ('registry', 'Registry');
INSERT INTO connection_visibility (connection_id, group_id)
SELECT c.id, g.id FROM connections c, groups g WHERE c.kind = 'registry' AND g.builtin;

CREATE TABLE sync_runs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    connection_id bigint NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('sync', 'reconcile')),
    trigger text NOT NULL CHECK (trigger IN ('schedule', 'manual')),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    cursor_from text NOT NULL DEFAULT '',
    cursor_to text NOT NULL DEFAULT '',
    seen integer NOT NULL DEFAULT 0,
    created integer NOT NULL DEFAULT 0,
    updated integer NOT NULL DEFAULT 0,
    unchanged integer NOT NULL DEFAULT 0,
    deleted integer NOT NULL DEFAULT 0,
    skipped jsonb NOT NULL DEFAULT '[]',
    error text NOT NULL DEFAULT '',
    queued_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz
);
CREATE INDEX sync_runs_connection ON sync_runs (connection_id, id DESC);
CREATE INDEX sync_runs_queued_at ON sync_runs (queued_at);

CREATE TABLE source_items (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    connection_id bigint NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    external_id text NOT NULL,
    kind text NOT NULL,
    title text NOT NULL,
    url text NOT NULL DEFAULT '',
    author text NOT NULL DEFAULT '',
    source_created_at timestamptz,
    source_updated_at timestamptz,
    content_hash bytea NOT NULL,
    version_id bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (connection_id, external_id)
);
CREATE INDEX source_items_title_trgm ON source_items USING gin (lower(title) gin_trgm_ops);
CREATE INDEX source_items_updated ON source_items (source_updated_at DESC);

CREATE TABLE source_versions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    item_id bigint NOT NULL REFERENCES source_items (id) ON DELETE CASCADE,
    connection_id bigint NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    content_hash bytea NOT NULL,
    title text NOT NULL,
    body text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}',
    edited_by text NOT NULL DEFAULT '',
    source_updated_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX source_versions_item ON source_versions (item_id, id DESC);
ALTER TABLE source_items ADD FOREIGN KEY (version_id) REFERENCES source_versions (id) DEFERRABLE INITIALLY DEFERRED;

-- The passages and chunks of the current version of each item.
CREATE TABLE passages (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    item_id bigint NOT NULL REFERENCES source_items (id) ON DELETE CASCADE,
    version_id bigint NOT NULL REFERENCES source_versions (id) ON DELETE CASCADE,
    connection_id bigint NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    hash bytea NOT NULL,
    ordinal integer NOT NULL,
    kind text NOT NULL CHECK (kind IN ('paragraph', 'list', 'table', 'code', 'quote')),
    heading_path text[] NOT NULL DEFAULT '{}',
    text text NOT NULL,
    norm text NOT NULL
);
CREATE INDEX passages_item_hash ON passages (item_id, hash);
CREATE INDEX passages_connection ON passages (connection_id);

CREATE TABLE chunks (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    item_id bigint NOT NULL REFERENCES source_items (id) ON DELETE CASCADE,
    version_id bigint NOT NULL REFERENCES source_versions (id) ON DELETE CASCADE,
    connection_id bigint NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    ordinal integer NOT NULL,
    heading_path text[] NOT NULL DEFAULT '{}',
    text text NOT NULL,
    passage_hashes bytea[] NOT NULL,
    tokens integer NOT NULL,
    tsv tsvector NOT NULL
);
CREATE INDEX chunks_item ON chunks (item_id);
CREATE INDEX chunks_connection ON chunks (connection_id);
CREATE INDEX chunks_tsv ON chunks USING gin (tsv);

CREATE TABLE entities (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type text NOT NULL CHECK (type IN ('person', 'team', 'service', 'api', 'system', 'resource', 'repository', 'project',
        'initiative', 'issue', 'document', 'decision', 'environment', 'term')),
    key text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'proposed', 'merged')),
    merged_into bigint REFERENCES entities (id),
    domain_id bigint REFERENCES domains (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE entity_sources (
    entity_id bigint NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    connection_id bigint NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (entity_id, connection_id)
);
CREATE INDEX entity_sources_connection ON entity_sources (connection_id);

CREATE TABLE entity_aliases (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entity_id bigint NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    connection_id bigint NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    alias text NOT NULL,
    alias_norm text NOT NULL,
    is_name boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (entity_id, connection_id, alias_norm)
);
CREATE INDEX entity_aliases_norm_trgm ON entity_aliases USING gin (alias_norm gin_trgm_ops);
CREATE INDEX entity_aliases_connection ON entity_aliases (connection_id);

-- A claim is a subject, a predicate and an object: an entity or a text.
-- valid_from dates the values of time-scoped predicates.
CREATE TABLE claims (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    subject_id bigint NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    predicate text NOT NULL,
    object_entity_id bigint REFERENCES entities (id) ON DELETE CASCADE,
    object_text text,
    object_norm text NOT NULL DEFAULT '',
    scope_env text NOT NULL DEFAULT '',
    valid_from timestamptz,
    valid_to timestamptz,
    origin text NOT NULL DEFAULT 'source' CHECK (origin IN ('source', 'human', 'model')),
    created_at timestamptz NOT NULL DEFAULT now(),
    retracted_at timestamptz,
    CHECK ((object_entity_id IS NULL) <> (object_text IS NULL))
);
CREATE UNIQUE INDEX claims_identity ON claims (subject_id, predicate, coalesce(object_entity_id, 0), object_norm, scope_env,
    coalesce(valid_from, '-infinity'::timestamptz));
CREATE INDEX claims_object ON claims (object_entity_id) WHERE object_entity_id IS NOT NULL;

CREATE TABLE claim_evidence (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    claim_id bigint NOT NULL REFERENCES claims (id) ON DELETE CASCADE,
    connection_id bigint NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    item_id bigint NOT NULL REFERENCES source_items (id) ON DELETE CASCADE,
    passage_hash bytea NOT NULL,
    kind text NOT NULL CHECK (kind IN ('field', 'passage', 'human')),
    stance text NOT NULL DEFAULT 'supports' CHECK (stance IN ('supports', 'disputes')),
    quote text NOT NULL,
    state text NOT NULL DEFAULT 'current' CHECK (state IN ('current', 'stale')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (claim_id, item_id, passage_hash, stance)
);
CREATE INDEX claim_evidence_item ON claim_evidence (item_id, passage_hash);
CREATE INDEX claim_evidence_connection ON claim_evidence (connection_id);

-- +goose Down
DROP TABLE claim_evidence;
DROP TABLE claims;
DROP TABLE entity_aliases;
DROP TABLE entity_sources;
DROP TABLE entities;
DROP TABLE chunks;
DROP TABLE passages;
ALTER TABLE source_items DROP COLUMN version_id;
DROP TABLE source_versions;
DROP TABLE source_items;
DROP TABLE sync_runs;
DROP TABLE connection_visibility;
DROP TABLE connections;
DROP TABLE domains;
