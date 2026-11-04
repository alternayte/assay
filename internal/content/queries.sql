-- name: GetItemByExternalID :one
-- vis:system
SELECT id, content_hash, revision, index_hash FROM source_items
WHERE connection_id = @connection_id AND external_id = @external_id;

-- name: InsertItem :one
-- vis:system
INSERT INTO source_items (connection_id, external_id, kind, title, url, author, source_created_at, source_updated_at, content_hash,
                          revision, index_hash)
VALUES (@connection_id, @external_id, @kind, @title, @url, @author, @source_created_at, @source_updated_at, @content_hash,
        @revision, @index_hash)
RETURNING id;

-- name: UpdateItem :exec
-- vis:system
UPDATE source_items
SET kind = @kind, title = @title, url = @url, author = @author, source_created_at = @source_created_at,
    source_updated_at = @source_updated_at, content_hash = @content_hash, version_id = @version_id, revision = @revision,
    index_hash = @index_hash, updated_at = now()
WHERE id = @id;

-- name: SetItemEntity :exec
-- vis:system
UPDATE source_items SET entity_id = @entity_id WHERE id = @id;

-- name: SetRevision :exec
-- vis:system
UPDATE source_items SET revision = @revision WHERE id = @id;

-- name: InsertVersion :one
INSERT INTO source_versions (item_id, connection_id, content_hash, title, body, metadata, edited_by, source_updated_at)
VALUES (@item_id, @connection_id, @content_hash, @title, @body, @metadata, @edited_by, @source_updated_at)
RETURNING id;

-- name: DeleteItemPassages :exec
-- vis:system
DELETE FROM passages WHERE item_id = @item_id;

-- name: ItemChunkHashes :many
-- The hash of the stored text, not the stored hash: a damaged chunk then
-- does not match, and a chunk from before text_hash matches.
-- vis:system
SELECT id, sha256(convert_to(text, 'UTF8'))::bytea AS text_hash FROM chunks WHERE item_id = @item_id ORDER BY ordinal;

-- name: DeleteChunks :exec
-- vis:system
DELETE FROM chunks WHERE id = ANY(@ids::bigint[]);

-- name: KeepChunks :exec
-- A kept chunk has the same text, so its embedding vectors stay. Its
-- full-text vector and text hash are written again, for a reindex of a
-- damaged index.
-- vis:system
UPDATE chunks c
SET version_id = @version_id, ordinal = r.ordinal, heading_path = r.heading_path,
    passage_hashes = ARRAY(SELECT decode(h, 'base64') FROM unnest(r.hashes) WITH ORDINALITY AS u (h, n) ORDER BY n),
    tsv = setweight(to_tsvector('simple', unaccent(@title::text)), 'A') ||
          setweight(to_tsvector('simple', unaccent(r.heading)), 'B') ||
          setweight(to_tsvector('simple', unaccent(r.body)), 'C'),
    text_hash = decode(r.text_hash, 'base64')
FROM jsonb_to_recordset(@rows::jsonb) AS r (id bigint, ordinal integer, heading_path text[], hashes text[], heading text, body text,
                                             text_hash text)
WHERE c.id = r.id;

-- name: SetPassagesVersion :exec
-- The passages and chunks of an unchanged index belong to the new version.
-- vis:system
UPDATE passages SET version_id = @version_id WHERE item_id = @item_id;

-- name: SetChunksVersion :exec
-- vis:system
UPDATE chunks SET version_id = @version_id WHERE item_id = @item_id;

-- name: FillChunkHashes :execrows
-- vis:system
WITH b AS (SELECT id FROM chunks WHERE text_hash IS NULL ORDER BY id LIMIT 10000)
UPDATE chunks c SET text_hash = sha256(convert_to(c.text, 'UTF8')) FROM b WHERE c.id = b.id;

-- name: InsertPassages :copyfrom
-- vis:system
INSERT INTO passages (item_id, version_id, connection_id, hash, ordinal, kind, heading_path, text, norm, start_line, end_line)
VALUES (@item_id, @version_id, @connection_id, @hash, @ordinal, @kind, @heading_path, @text, @norm, @start_line, @end_line);

-- name: InsertChunks :exec
-- vis:system
INSERT INTO chunks (item_id, version_id, connection_id, ordinal, heading_path, text, passage_hashes, tokens, tsv, text_hash)
SELECT @item_id, @version_id, @connection_id, r.ordinal, r.heading_path, r.text,
       ARRAY(SELECT decode(h, 'base64') FROM unnest(r.hashes) WITH ORDINALITY AS u (h, n) ORDER BY n), r.tokens,
       setweight(to_tsvector('simple', unaccent(@title::text)), 'A') ||
       setweight(to_tsvector('simple', unaccent(r.heading)), 'B') ||
       setweight(to_tsvector('simple', unaccent(r.body)), 'C'),
       decode(r.text_hash, 'base64')
FROM jsonb_to_recordset(@rows::jsonb) AS r (ordinal integer, heading_path text[], text text, hashes text[], tokens integer, heading text,
                                             body text, text_hash text);

-- name: RefreshEvidence :exec
-- Evidence whose passage hash survives in the new version stays current.
-- Other evidence of the item becomes stale.
-- vis:system
UPDATE claim_evidence e
SET state = CASE WHEN EXISTS (SELECT 1 FROM passages p WHERE p.item_id = e.item_id AND p.hash = e.passage_hash)
                 THEN 'current' ELSE 'stale' END,
    updated_at = now()
WHERE e.item_id = @item_id AND e.kind <> 'human';

-- name: UpsertEntity :one
-- A type that a person set stays.
INSERT INTO entities (type, key, domain_id) VALUES (@type, @key, @domain_id)
ON CONFLICT (key) DO UPDATE SET type = CASE WHEN entities.type_edited_at IS NULL THEN EXCLUDED.type ELSE entities.type END,
    domain_id = coalesce(entities.domain_id, EXCLUDED.domain_id)
RETURNING id;

-- name: AddEntitySource :exec
-- vis:system
INSERT INTO entity_sources (entity_id, connection_id) VALUES (@entity_id, @connection_id)
ON CONFLICT DO NOTHING;

-- name: AddAlias :exec
-- An alias that a person removed is not added again.
-- vis:system
INSERT INTO entity_aliases (entity_id, connection_id, alias, alias_norm, is_name)
SELECT @entity_id, @connection_id, @alias, @alias_norm, @is_name
WHERE NOT EXISTS (SELECT 1 FROM alias_suppressions s WHERE s.entity_id = @entity_id AND s.alias_norm = @alias_norm)
ON CONFLICT (entity_id, connection_id, alias_norm)
DO UPDATE SET is_name = entity_aliases.is_name OR EXCLUDED.is_name,
              alias = CASE WHEN EXCLUDED.is_name THEN EXCLUDED.alias ELSE entity_aliases.alias END;

-- name: ClearOtherNames :exec
-- vis:system
UPDATE entity_aliases SET is_name = false
WHERE entity_id = @entity_id AND connection_id = @connection_id AND alias_norm <> @alias_norm AND is_name AND NOT edited;

-- name: LatestClaim :one
SELECT id, coalesce(object_entity_id, 0)::bigint AS object_entity_id, object_norm, retracted_at
FROM claims
WHERE subject_id = @subject_id AND predicate = @predicate AND scope_env = @scope_env
ORDER BY valid_from DESC NULLS LAST, id DESC
LIMIT 1;

-- name: UpsertClaim :one
INSERT INTO claims (subject_id, predicate, object_entity_id, object_text, object_norm, scope_env, valid_from, origin)
VALUES (@subject_id, @predicate, @object_entity_id, @object_text, @object_norm, @scope_env, @valid_from, @origin)
ON CONFLICT (subject_id, predicate, (coalesce(object_entity_id, 0)), object_norm, scope_env,
             (coalesce(valid_from, '-infinity'::timestamptz)))
DO UPDATE SET retracted_at = NULL
RETURNING id;

-- name: Unretract :exec
UPDATE claims SET retracted_at = NULL WHERE id = @id;

-- name: AddEvidence :one
-- Inserted is false when the row existed.
-- vis:system
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, state, source_type)
VALUES (@claim_id, @connection_id, @item_id, @passage_hash, @kind, @stance, @quote, 'current', @source_type)
ON CONFLICT (claim_id, item_id, passage_hash, stance)
DO UPDATE SET state = 'current', quote = EXCLUDED.quote, source_type = EXCLUDED.source_type, updated_at = now()
RETURNING (xmax = 0) AS inserted;

-- name: ClaimGroupConnections :many
-- The connections of the evidence of a claim and of the other claims of
-- its subject and predicate, and of the sources of their entities: a card
-- about the group names them.
-- vis:system
SELECT e.connection_id FROM claim_evidence e
JOIN claims c ON c.id = e.claim_id
JOIN claims t ON t.subject_id = c.subject_id AND t.predicate = c.predicate
WHERE t.id = @claim_id
UNION
SELECT s.connection_id FROM entity_sources s
JOIN claims c ON s.entity_id = c.subject_id OR s.entity_id = c.object_entity_id
JOIN claims t ON t.subject_id = c.subject_id AND t.predicate = c.predicate
WHERE t.id = @claim_id
ORDER BY 1;

-- name: ConnectionKind :one
SELECT kind FROM connections WHERE id = @id;

-- name: ItemClaims :many
-- vis:system
SELECT DISTINCT claim_id FROM claim_evidence WHERE item_id = @item_id;

-- name: DeleteItem :execrows
-- The cascade deletes the versions, passages, chunks and evidence of the item.
-- vis:system
DELETE FROM source_items WHERE id = @id;

-- name: RetractUnsupported :execrows
-- vis:system
UPDATE claims c SET retracted_at = @now
WHERE c.id = ANY(@ids::bigint[]) AND c.retracted_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM claim_evidence e WHERE e.claim_id = c.id);

-- name: GetVisibleItem :one
SELECT i.id, i.connection_id, c.name AS connection_name, c.kind AS connection_kind, i.kind, i.title, i.url, i.author,
       i.source_updated_at
FROM source_items i
JOIN connections c ON c.id = i.connection_id
WHERE i.id = @id AND i.connection_id = ANY(@visible::bigint[]);

-- name: VisiblePassages :many
SELECT hash, ordinal, kind, heading_path, text
FROM passages
WHERE item_id = @item_id AND connection_id = ANY(@visible::bigint[])
ORDER BY ordinal;

-- name: DeleteStaleItemEvidence :exec
-- vis:system
DELETE FROM claim_evidence WHERE item_id = @item_id AND state = 'stale';

-- name: AddProposalEvidence :exec
-- Evidence of an open proposal makes its claim Proposed. Evidence from a
-- source stays as it is. The review package checks that the item is
-- visible to the proposer.
-- vis:system
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, state, source_type, proposal_id)
SELECT @claim_id, i.connection_id, i.id, @passage_hash, 'proposal', 'supports', @quote, 'current', k.kind, @proposal_id
FROM source_items i JOIN connections k ON k.id = i.connection_id
WHERE i.id = @item_id
ON CONFLICT (claim_id, item_id, passage_hash, stance)
DO UPDATE SET proposal_id = EXCLUDED.proposal_id, quote = EXCLUDED.quote, state = 'current', updated_at = now()
WHERE claim_evidence.kind = 'proposal';

-- name: AddNoteEvidence :exec
-- The note or reason of a correction on each of its items. It makes the
-- correct value Proposed.
-- vis:system
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, state, source_type, proposal_id)
SELECT @claim_id, i.connection_id, i.id, @passage_hash, 'proposal', 'supports', @quote, 'current', 'review', @proposal_id
FROM source_items i WHERE i.id = ANY(@items::bigint[])
ON CONFLICT (claim_id, item_id, passage_hash, stance)
DO UPDATE SET proposal_id = EXCLUDED.proposal_id, quote = EXCLUDED.quote, state = 'current', updated_at = now()
WHERE claim_evidence.kind = 'proposal';

-- name: AddDispute :exec
-- A human row that disputes a claim, after an accepted correction.
-- vis:system
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, state, source_type)
SELECT @claim_id, i.connection_id, i.id, @passage_hash, 'human', 'disputes', @quote, 'current', 'review'
FROM source_items i WHERE i.id = ANY(@items::bigint[])
ON CONFLICT (claim_id, item_id, passage_hash, stance) DO UPDATE SET state = 'current', quote = EXCLUDED.quote, updated_at = now();

-- name: DeleteSourcelessEntities :execrows
-- Deletes the entities of ids that have no source left and that no claim
-- with evidence names: a claim that rests on evidence of another connection
-- stays with its entity. Entities merged into a deleted entity become
-- active again, and merges drop deleted losers, so that they still split. A
-- merge without a loser left is undone. Claims, profiles and pages of the
-- deleted entities go by cascade.
-- vis:system
WITH gone AS (SELECT e.id FROM entities e
              WHERE e.id = ANY(@ids::bigint[]) AND NOT EXISTS (SELECT 1 FROM entity_sources s WHERE s.entity_id = e.id)
                AND NOT EXISTS (SELECT 1 FROM claims c JOIN claim_evidence ev ON ev.claim_id = c.id
                                WHERE (c.subject_id = e.id OR c.object_entity_id = e.id) AND c.retracted_at IS NULL)),
freed AS (UPDATE entities SET status = 'active', merged_into = NULL, updated_at = now()
          WHERE merged_into IN (SELECT id FROM gone) AND id NOT IN (SELECT id FROM gone)),
pruned AS (UPDATE merges m
           SET loser_ids = array(SELECT x FROM unnest(m.loser_ids) x WHERE x NOT IN (SELECT id FROM gone)),
               undone_at = CASE WHEN m.undone_at IS NULL AND m.loser_ids <@ array(SELECT id FROM gone) THEN now() ELSE m.undone_at END
           WHERE m.loser_ids && array(SELECT id FROM gone))
DELETE FROM entities WHERE id IN (SELECT id FROM gone);

-- name: ItemEntity :one
-- The entity of an item and its connection, for a tombstone.
-- vis:system
SELECT entity_id, connection_id FROM source_items WHERE id = @id;

-- name: DropEntitySource :execrows
-- Deletes the source and the aliases of the entity in the connection when
-- no item and no current claim evidence of the connection names it.
-- vis:system
WITH src AS (DELETE FROM entity_sources s
             WHERE s.entity_id = @entity_id AND s.connection_id = @connection_id
               AND NOT EXISTS (SELECT 1 FROM source_items i WHERE i.entity_id = @entity_id AND i.connection_id = @connection_id)
               AND NOT EXISTS (SELECT 1 FROM claims c JOIN claim_evidence ev ON ev.claim_id = c.id
                               WHERE (c.subject_id = @entity_id OR c.object_entity_id = @entity_id)
                                 AND ev.connection_id = @connection_id AND c.retracted_at IS NULL)
             RETURNING s.entity_id)
DELETE FROM entity_aliases a WHERE a.entity_id IN (SELECT entity_id FROM src) AND a.connection_id = @connection_id;

-- name: ClaimEntities :many
-- The subjects and entity objects of the claims.
-- vis:system
SELECT DISTINCT x.id::bigint AS id FROM claims c,
       LATERAL (VALUES (c.subject_id), (c.object_entity_id)) AS x(id)
WHERE c.id = ANY(@ids::bigint[]) AND x.id IS NOT NULL
ORDER BY 1;

-- name: ReindexItems :many
-- The current version of each item after an ID, for assay reindex.
-- vis:system
SELECT i.id, i.connection_id, i.kind, i.external_id, v.id AS version_id, v.title, v.body
FROM source_items i
JOIN source_versions v ON v.id = i.version_id
WHERE i.id > @after
ORDER BY i.id
LIMIT @lim;
