-- name: FullText :many
-- Chunks that match the query, ranked with title weight A, heading B and
-- body C.
SELECT c.id AS chunk_id, c.item_id
FROM chunks c
JOIN source_items i ON i.id = c.item_id
JOIN connections k ON k.id = i.connection_id,
     websearch_to_tsquery('simple', unaccent(@query::text)) q
WHERE c.connection_id = ANY(@visible::bigint[]) AND i.connection_id = ANY(@visible::bigint[])
  AND c.tsv @@ q
  AND (@source_type::text = '' OR k.kind = @source_type)
  AND (@connection::bigint = 0 OR i.connection_id = @connection)
  AND (sqlc.narg(since)::timestamptz IS NULL OR i.source_updated_at >= sqlc.narg(since))
  AND (@domain::bigint = 0 OR k.default_domain_id = @domain
       OR EXISTS (SELECT 1 FROM entities d WHERE d.id = i.entity_id AND d.domain_id = @domain))
  AND (@entity_type::text = '' OR EXISTS (SELECT 1 FROM entities t WHERE t.id = i.entity_id AND t.type = @entity_type)
       OR EXISTS (SELECT 1 FROM claim_evidence ev
                  JOIN claims cl ON cl.id = ev.claim_id
                  JOIN entities t ON t.id = cl.subject_id
                  WHERE ev.item_id = i.id AND ev.connection_id = ANY(@visible::bigint[]) AND t.type = @entity_type))
ORDER BY ts_rank_cd(c.tsv, q) DESC, c.id
LIMIT @lim;

-- name: TitleTrigram :many
-- Items whose title is like the query, also when the query is misspelled.
SELECT i.id AS item_id,
       (SELECT c.id FROM chunks c WHERE c.item_id = i.id AND c.connection_id = ANY(@visible::bigint[])
        ORDER BY c.ordinal LIMIT 1)::bigint AS chunk_id
FROM source_items i
JOIN connections k ON k.id = i.connection_id
WHERE i.connection_id = ANY(@visible::bigint[])
  AND word_similarity(lower(unaccent(@query::text)), lower(unaccent(i.title))) >= 0.4
  AND (@source_type::text = '' OR k.kind = @source_type)
  AND (@connection::bigint = 0 OR i.connection_id = @connection)
  AND (sqlc.narg(since)::timestamptz IS NULL OR i.source_updated_at >= sqlc.narg(since))
  AND (@domain::bigint = 0 OR k.default_domain_id = @domain
       OR EXISTS (SELECT 1 FROM entities d WHERE d.id = i.entity_id AND d.domain_id = @domain))
  AND (@entity_type::text = '' OR EXISTS (SELECT 1 FROM entities t WHERE t.id = i.entity_id AND t.type = @entity_type)
       OR EXISTS (SELECT 1 FROM claim_evidence ev
                  JOIN claims cl ON cl.id = ev.claim_id
                  JOIN entities t ON t.id = cl.subject_id
                  WHERE ev.item_id = i.id AND ev.connection_id = ANY(@visible::bigint[]) AND t.type = @entity_type))
  AND EXISTS (SELECT 1 FROM chunks c WHERE c.item_id = i.id AND c.connection_id = ANY(@visible::bigint[]))
ORDER BY word_similarity(lower(unaccent(@query::text)), lower(unaccent(i.title))) DESC, i.id
LIMIT @lim;

-- name: MatchEntities :many
-- Visible entities and terms with an alias equal or like the query. The
-- name is the best visible alias.
SELECT e.id, e.key, e.type,
       (SELECT n.alias FROM entity_aliases n JOIN connections nk ON nk.id = n.connection_id
        WHERE n.entity_id = e.id AND n.connection_id = ANY(@visible::bigint[])
        ORDER BY nk.kind = 'registry' DESC, n.is_name DESC, n.id LIMIT 1)::text AS name,
       max(CASE WHEN a.alias_norm = @norm::text THEN 2 ELSE similarity(a.alias_norm, @norm::text) END)::float8 AS score
FROM entity_aliases a
JOIN entities e ON e.id = a.entity_id
WHERE a.connection_id = ANY(@visible::bigint[])
  AND e.status <> 'merged'
  AND (a.alias_norm = @norm::text OR (a.alias_norm % @norm::text AND similarity(a.alias_norm, @norm::text) >= 0.4))
GROUP BY e.id
ORDER BY score DESC, e.id
LIMIT 5;

-- name: Hits :many
-- The items, headings and highlighted snippets of chunks.
SELECT c.id AS chunk_id, c.item_id, c.heading_path, c.text, c.passage_hashes, i.title, i.url, i.kind, i.source_updated_at,
       i.connection_id, k.name AS connection_name, k.kind AS source_type,
       ts_headline('simple', c.text, to_tsquery('simple', unaccent(@words::text)),
                   'StartSel=' || chr(57344) || ', StopSel=' || chr(57345) || ', MaxWords=40, MinWords=15, MaxFragments=2, FragmentDelimiter=" … "')::text AS snippet
FROM chunks c
JOIN source_items i ON i.id = c.item_id
JOIN connections k ON k.id = i.connection_id
WHERE c.id = ANY(@ids::bigint[]) AND c.connection_id = ANY(@visible::bigint[]) AND i.connection_id = ANY(@visible::bigint[]);

-- name: FullTextAny :many
-- Chunks that match any of the words, ranked by cover density, for the
-- questions of Ask.
SELECT c.id AS chunk_id, c.item_id
FROM chunks c
JOIN source_items i ON i.id = c.item_id,
     to_tsquery('simple', unaccent(@words::text)) q
WHERE c.connection_id = ANY(@visible::bigint[]) AND i.connection_id = ANY(@visible::bigint[])
  AND c.tsv @@ q
ORDER BY ts_rank_cd(c.tsv, q, 32) DESC, c.id
LIMIT @lim;
