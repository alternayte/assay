-- name: MentionedEntities :many
-- Visible entities with an alias that the question contains as whole words,
-- the longest match first.
SELECT a.entity_id
FROM entity_aliases a
JOIN entities e ON e.id = a.entity_id
WHERE a.connection_id = ANY(@visible::bigint[])
  AND e.status <> 'merged'
  AND length(a.alias_norm) >= 3
  AND strpos(' ' || @question_norm::text || ' ', ' ' || a.alias_norm || ' ') > 0
GROUP BY a.entity_id
ORDER BY max(length(a.alias_norm)) DESC, a.entity_id
LIMIT 20;

-- name: FirstMentioned :many
-- The first visible entity that each normalized question names, by the
-- order of MentionedEntities. ord is the 1-based position in norms.
SELECT n.ord::integer AS ord, m.entity_id,
       coalesce(entity_visible_key(m.entity_id, @visible::bigint[]), e.key)::text AS key,
       coalesce(entity_display_name(m.entity_id, @visible::bigint[]), e.key)::text AS name
FROM unnest(@norms::text[]) WITH ORDINALITY AS n(norm, ord)
JOIN LATERAL (
    SELECT a.entity_id
    FROM entity_aliases a
    JOIN entities x ON x.id = a.entity_id
    WHERE a.connection_id = ANY(@visible::bigint[])
      AND x.status <> 'merged'
      AND length(a.alias_norm) >= 3
      AND strpos(' ' || n.norm || ' ', ' ' || a.alias_norm || ' ') > 0
    GROUP BY a.entity_id
    ORDER BY max(length(a.alias_norm)) DESC, a.entity_id
    LIMIT 1
) m ON true
JOIN entities e ON e.id = m.entity_id;

-- name: InsertQuestion :one
-- Without a thread, the trigger questions_own_thread sets thread_id to the
-- ID of the question.
INSERT INTO questions (thread_id, turn, user_id, text, answer_md, citations, flags, created_at)
VALUES (sqlc.narg(thread_id)::bigint, @turn, @user_id, @text, @answer_md, @citations, @flags, @created_at)
RETURNING id;

-- name: ListThreads :many
-- One row per thread: the first question, the turn count and the time of
-- the last turn.
SELECT f.id, f.text, count(*)::integer AS turns, max(q.created_at)::timestamptz AS last_at
FROM questions q
JOIN questions f ON f.id = q.thread_id
WHERE q.user_id = @user_id
GROUP BY f.id, f.text
ORDER BY max(q.id) DESC
LIMIT @lim;

-- name: GetQuestion :one
SELECT * FROM questions WHERE id = @id AND user_id = @user_id;

-- name: ThreadTurns :many
-- The turns of a thread of the user with their marks, in order.
SELECT q.id, q.thread_id, q.turn, q.text, q.answer_md, q.citations, q.flags, q.created_at,
       coalesce(f.rating, '')::text AS rating, coalesce(f.reason, '')::text AS reason, coalesce(f.wrong_citation, false)::boolean AS wrong_citation
FROM questions q
LEFT JOIN answer_feedback f ON f.question_id = q.id
WHERE q.thread_id = @thread_id AND q.user_id = @user_id
ORDER BY q.turn;

-- name: DeleteQuestionsBefore :execrows
-- Retention deletes whole threads by the time of their last turn. The
-- marks go with their questions.
DELETE FROM questions
WHERE thread_id IN (SELECT t.thread_id FROM questions t GROUP BY t.thread_id HAVING max(t.created_at) < sqlc.arg(before)::timestamptz);

-- name: SetFeedback :exec
INSERT INTO answer_feedback (question_id, rating, reason, wrong_citation, created_at, updated_at)
VALUES (@question_id, @rating, @reason, @wrong_citation, @now, @now)
ON CONFLICT (question_id) DO UPDATE
SET rating = EXCLUDED.rating, reason = EXCLUDED.reason, wrong_citation = EXCLUDED.wrong_citation, updated_at = EXCLUDED.updated_at;

-- name: QuestionsToImprove :many
-- Questions of all users with no evidence or a not-helpful mark since a
-- time, whose connections are all in the visible set of the reader. It
-- never reads who asked.
SELECT q.id, q.text, q.created_at, coalesce((q.flags ->> 'noEvidence')::boolean, false)::boolean AS no_evidence,
       coalesce(f.rating, '')::text AS rating, coalesce(f.reason, '')::text AS reason, coalesce(f.wrong_citation, false)::boolean AS wrong_citation
FROM questions q
LEFT JOIN answer_feedback f ON f.question_id = q.id
WHERE q.created_at >= @since
  AND ((q.flags ->> 'noEvidence')::boolean IS TRUE OR f.rating = 'down')
  AND NOT EXISTS (
      SELECT 1 FROM jsonb_array_elements_text(coalesce(q.flags -> 'connections', '[]'::jsonb)) c
      WHERE NOT (c::bigint = ANY(@visible::bigint[])))
ORDER BY q.created_at DESC, q.id DESC
LIMIT @lim;

-- name: RatedQuestions :many
-- Questions of all users with a mark, whose connections are all in the
-- visible set of the reader. It never reads who asked.
SELECT q.id, q.text, q.citations, coalesce((q.flags ->> 'noEvidence')::boolean, false)::boolean AS no_evidence, f.rating
FROM questions q
JOIN answer_feedback f ON f.question_id = q.id
WHERE NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements_text(coalesce(q.flags -> 'connections', '[]'::jsonb)) c
    WHERE NOT (c::bigint = ANY(@visible::bigint[])))
ORDER BY q.id;

-- name: ItemTitles :many
SELECT i.id, i.title FROM source_items i
WHERE i.id = ANY(@ids::bigint[]) AND i.connection_id = ANY(@visible::bigint[]);

-- name: CitedSource :one
-- A cited source read again for the reader: the current passages of the
-- item with the hashes, in order.
SELECT i.id, i.title, i.url, i.kind, i.source_updated_at, i.connection_id, k.name AS connection_name, k.kind AS source_type,
       string_agg(p.text, E'\n\n' ORDER BY p.ordinal)::text AS text,
       (SELECT f.heading_path FROM passages f
        WHERE f.item_id = i.id AND f.version_id = i.version_id AND f.connection_id = ANY(@visible::bigint[])
          AND encode(f.hash, 'hex') = ANY(@hashes::text[])
        ORDER BY f.ordinal LIMIT 1)::text[] AS heading_path
FROM source_items i
JOIN connections k ON k.id = i.connection_id
JOIN passages p ON p.item_id = i.id AND p.version_id = i.version_id
WHERE i.id = @item_id AND i.connection_id = ANY(@visible::bigint[]) AND p.connection_id = ANY(@visible::bigint[])
  AND encode(p.hash, 'hex') = ANY(@hashes::text[])
GROUP BY i.id, k.name, k.kind;

-- name: CodeLines :one
-- The line range of the passages of a source code chunk.
SELECT coalesce(min(p.start_line), 0)::integer AS start_line, coalesce(max(p.end_line), 0)::integer AS end_line
FROM passages p
WHERE p.item_id = @item_id AND p.connection_id = ANY(@visible::bigint[]) AND encode(p.hash, 'hex') = ANY(@hashes::text[]);
