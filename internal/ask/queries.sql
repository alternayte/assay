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

-- name: InsertQuestion :one
INSERT INTO questions (user_id, text, answer_md, citations, flags, created_at)
VALUES (@user_id, @text, @answer_md, @citations, @flags, @created_at)
RETURNING id;

-- name: ListQuestions :many
SELECT id, text, created_at FROM questions WHERE user_id = @user_id ORDER BY id DESC LIMIT @lim;

-- name: GetQuestion :one
SELECT * FROM questions WHERE id = @id AND user_id = @user_id;

-- name: DeleteQuestionsBefore :execrows
DELETE FROM questions WHERE created_at < @before;

-- name: CodeLines :one
-- The line range of the passages of a source code chunk.
SELECT coalesce(min(p.start_line), 0)::integer AS start_line, coalesce(max(p.end_line), 0)::integer AS end_line
FROM passages p
WHERE p.item_id = @item_id AND p.connection_id = ANY(@visible::bigint[]) AND encode(p.hash, 'hex') = ANY(@hashes::text[]);
