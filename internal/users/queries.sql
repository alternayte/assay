-- name: CountUsers :one
SELECT count(*) FROM auth_users;

-- name: ListUsers :many
SELECT id, email, display_name, role, disabled_at, created_at
FROM auth_users
ORDER BY lower(email);

-- name: GetUser :one
SELECT id, email, display_name, role, disabled_at, created_at
FROM auth_users
WHERE id = @id;

-- name: InsertInvite :one
INSERT INTO invites (email, role, token_hash, created_by, expires_at)
VALUES (@email, @role, @token_hash, @created_by, @expires_at)
RETURNING id, created_at;

-- name: GetInviteByHash :one
SELECT id, email, role, expires_at, accepted_at, revoked_at
FROM invites
WHERE token_hash = @token_hash;

-- name: ClaimInvite :execrows
UPDATE invites SET accepted_at = @now, accepted_user_id = @user_id
WHERE id = @id AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > @now;

-- name: ListOpenInvites :many
SELECT i.id, i.email, i.role, i.created_at, i.expires_at, coalesce(u.email, '') AS created_by_email
FROM invites i
LEFT JOIN auth_users u ON u.id = i.created_by
WHERE i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > @now
ORDER BY i.created_at DESC;

-- name: RevokeInvite :execrows
UPDATE invites SET revoked_at = @now
WHERE id = @id AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: InsertReset :one
INSERT INTO password_resets (user_id, token_hash, created_by, expires_at)
VALUES (@user_id, @token_hash, @created_by, @expires_at)
RETURNING id;

-- name: ClaimReset :one
UPDATE password_resets SET used_at = @now
WHERE token_hash = @token_hash AND used_at IS NULL AND expires_at > @now
RETURNING user_id;

-- name: ListGroups :many
SELECT g.id, g.name, g.builtin,
       CASE WHEN g.builtin
            THEN (SELECT count(*) FROM auth_users u WHERE u.disabled_at IS NULL)
            ELSE (SELECT count(*) FROM group_members m JOIN auth_users u ON u.id = m.user_id
                  WHERE m.group_id = g.id AND u.disabled_at IS NULL)
       END::bigint AS member_count
FROM groups g
ORDER BY g.builtin DESC, lower(g.name);

-- name: GetGroup :one
SELECT id, name, builtin FROM groups WHERE id = @id;

-- name: InsertGroup :one
INSERT INTO groups (name) VALUES (@name) RETURNING id;

-- name: RenameGroup :execrows
UPDATE groups SET name = @name WHERE id = @id AND NOT builtin;

-- name: DeleteGroup :execrows
DELETE FROM groups WHERE id = @id AND NOT builtin;

-- name: ListGroupMembers :many
SELECT u.id, u.email, u.display_name, u.role, u.disabled_at
FROM group_members m
JOIN auth_users u ON u.id = m.user_id
WHERE m.group_id = @group_id
ORDER BY lower(u.email);

-- name: ListActiveUsers :many
SELECT id, email, display_name, role, disabled_at
FROM auth_users
WHERE disabled_at IS NULL
ORDER BY lower(email);

-- name: AddGroupMember :exec
INSERT INTO group_members (group_id, user_id) VALUES (@group_id, @user_id)
ON CONFLICT DO NOTHING;

-- name: RemoveGroupMember :execrows
DELETE FROM group_members WHERE group_id = @group_id AND user_id = @user_id;

-- name: SetInviteUser :exec
UPDATE invites SET accepted_user_id = @user_id WHERE id = @id;
