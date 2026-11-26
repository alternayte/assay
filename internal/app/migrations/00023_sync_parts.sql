-- +goose Up
-- A sync run splits into parts, one per scope unit: a Confluence space, a
-- Jira project, a repository or the Backstage catalog. Each part runs as its
-- own job on any worker process.
CREATE TABLE sync_parts (
    run_id bigint NOT NULL REFERENCES sync_runs (id) ON DELETE CASCADE,
    part text NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    -- The cursor of the part when it finished.
    cursor text NOT NULL DEFAULT '',
    -- seen, created, updated, unchanged, deleted and skipped of the part.
    stats jsonb NOT NULL DEFAULT '{}',
    error text NOT NULL DEFAULT '',
    -- The host of the worker process that ran the part.
    host text NOT NULL DEFAULT '',
    -- The items that the reconciliation of the part found gone. The last
    -- part of a successful run tombstones those that are still in the part.
    gone bigint[] NOT NULL DEFAULT '{}',
    started_at timestamptz,
    finished_at timestamptz,
    PRIMARY KEY (run_id, part)
);
CREATE INDEX sync_parts_unfinished ON sync_parts (run_id) WHERE status IN ('queued', 'running');

-- The cursor of each part of a connection: a slow space no longer holds
-- back the cursor of a fast one.
CREATE TABLE connection_cursors (
    connection_id bigint NOT NULL REFERENCES connections (id) ON DELETE CASCADE,
    part text NOT NULL,
    cursor text NOT NULL,
    PRIMARY KEY (connection_id, part)
);

-- The next free request slot of a connection, for all processes. A
-- Retry-After answer moves held_until, and every process waits for it.
CREATE TABLE connection_rates (
    connection_id bigint PRIMARY KEY REFERENCES connections (id) ON DELETE CASCADE,
    next_at timestamptz NOT NULL DEFAULT now(),
    held_until timestamptz NOT NULL DEFAULT '-infinity'
);

-- The part jobs of one connection that run at once, across all processes.
ALTER TABLE connections ADD COLUMN parallel_parts integer NOT NULL DEFAULT 4 CHECK (parallel_parts BETWEEN 1 AND 32);

-- The part of each item. Reconciliation of a part tombstones only the items
-- of that part.
ALTER TABLE source_items ADD COLUMN part text NOT NULL DEFAULT '';
UPDATE source_items i SET part = CASE c.kind
        WHEN 'confluence' THEN coalesce((SELECT v.metadata->>'space' FROM source_versions v WHERE v.id = i.version_id), '')
        WHEN 'jira' THEN split_part(i.external_id, '-', 1)
        WHEN 'github' THEN split_part(i.external_id, ':', 1)
        WHEN 'azdo' THEN split_part(i.external_id, ':', 1)
        WHEN 'backstage' THEN 'catalog'
        ELSE '' END
FROM connections c
WHERE c.id = i.connection_id AND c.kind <> 'registry';
CREATE INDEX source_items_part ON source_items (connection_id, part);

-- Each part with stored items starts from the cursor of its connection. A
-- part without items starts with a full sync.
INSERT INTO connection_cursors (connection_id, part, cursor)
SELECT DISTINCT c.id, i.part, c.cursor
FROM connections c
JOIN source_items i ON i.connection_id = c.id
WHERE c.cursor <> '' AND i.part <> '';
ALTER TABLE connections DROP COLUMN cursor;

-- +goose Down
ALTER TABLE connections ADD COLUMN cursor text NOT NULL DEFAULT '';
UPDATE connections c SET cursor = coalesce((SELECT min(k.cursor) FROM connection_cursors k WHERE k.connection_id = c.id), '');
DROP INDEX source_items_part;
ALTER TABLE source_items DROP COLUMN part;
ALTER TABLE connections DROP COLUMN parallel_parts;
DROP TABLE connection_rates;
DROP TABLE connection_cursors;
DROP TABLE sync_parts;
