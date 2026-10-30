-- name: Insert :one
INSERT INTO audit_log (actor_user_id, actor_token_id, action, target, details, connections)
VALUES (@actor_user_id, @actor_token_id, @action, @target, @details, @connections)
RETURNING id;

-- name: List :many
SELECT a.id, a.at, a.actor_user_id, a.actor_token_id, a.action, a.target, a.details,
       coalesce(u.email, '') AS actor_email, (a.connections IS NULL OR a.connections <@ @visible::bigint[])::bool AS readable
FROM audit_log a
LEFT JOIN auth_users u ON u.id = a.actor_user_id
WHERE (sqlc.narg(action)::text IS NULL OR a.action = sqlc.narg(action) OR a.action LIKE sqlc.narg(action) || '.%')
  AND (sqlc.narg(before_id)::bigint IS NULL OR a.id < sqlc.narg(before_id))
ORDER BY a.id DESC
LIMIT @lim;

-- name: DeleteBefore :execrows
DELETE FROM audit_log WHERE at < @before;
