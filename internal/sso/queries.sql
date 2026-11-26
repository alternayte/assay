-- name: AdminLinked :one
-- An enabled admin with a linked account of the provider.
SELECT EXISTS (
    SELECT 1 FROM auth_accounts a JOIN auth_users u ON u.id = a.user_id
    WHERE a.provider = @provider AND u.role = 'admin' AND u.disabled_at IS NULL
);

-- name: UpsertDirectoryUser :exec
INSERT INTO directory_users (user_id, provider, subject, seen_at)
VALUES (@user_id, @provider, @subject, @now)
ON CONFLICT (user_id) DO UPDATE
SET provider = EXCLUDED.provider, subject = EXCLUDED.subject, directory_enabled = true, seen_at = EXCLUDED.seen_at;

-- name: ListDirectoryUsers :many
SELECT d.user_id, d.subject, d.directory_enabled, d.disabled_by_sync, u.disabled_at, u.email
FROM directory_users d
JOIN auth_users u ON u.id = d.user_id
WHERE d.provider = @provider
ORDER BY d.subject;

-- name: MarkDirectoryUser :exec
-- A user that a users read names. A null enabled keeps the stored state.
UPDATE directory_users
SET directory_enabled = coalesce(sqlc.narg(enabled)::boolean, directory_enabled), seen_at = @now
WHERE provider = @provider AND subject = @subject;

-- name: MarkRemoved :exec
UPDATE directory_users SET directory_enabled = false
WHERE provider = @provider AND subject = @subject;

-- name: MarkMissing :execrows
-- After a full read: the users that the directory no longer lists.
UPDATE directory_users SET directory_enabled = false
WHERE provider = @provider AND directory_enabled AND NOT (subject = ANY (@seen::text[]));

-- name: SetDisabledBySync :exec
UPDATE directory_users SET disabled_by_sync = @disabled_by_sync WHERE user_id = @user_id;

-- name: ListMappings :many
SELECT m.group_id, m.external_id, m.external_name, g.name AS group_name
FROM group_mappings m
JOIN groups g ON g.id = m.group_id
WHERE m.provider = @provider
ORDER BY lower(g.name);

-- name: GetGroup :one
SELECT id, name, builtin FROM groups WHERE id = @id;

-- name: PutMapping :exec
INSERT INTO group_mappings (group_id, provider, external_id, external_name)
VALUES (@group_id, @provider, @external_id, @external_name)
ON CONFLICT (group_id) DO UPDATE
SET provider = EXCLUDED.provider, external_id = EXCLUDED.external_id, external_name = EXCLUDED.external_name;

-- name: DeleteMapping :execrows
DELETE FROM group_mappings WHERE group_id = @group_id;

-- name: ClearMembers :many
DELETE FROM group_members WHERE group_id = @group_id RETURNING user_id;

-- name: GroupMembers :many
SELECT user_id FROM group_members WHERE group_id = @group_id;

-- name: AddMember :execrows
INSERT INTO group_members (group_id, user_id) VALUES (@group_id, @user_id)
ON CONFLICT DO NOTHING;

-- name: RemoveMember :execrows
DELETE FROM group_members WHERE group_id = @group_id AND user_id = @user_id;

-- name: GetState :one
SELECT provider, config_key, delta_link, last_started_at, last_success_at, last_error_at, last_error
FROM directory_state WHERE provider = @provider;

-- name: StartPoll :exec
INSERT INTO directory_state (provider, last_started_at) VALUES (@provider, @now)
ON CONFLICT (provider) DO UPDATE SET last_started_at = EXCLUDED.last_started_at;

-- name: SaveDelta :exec
UPDATE directory_state SET delta_link = @delta_link, config_key = @config_key WHERE provider = @provider;

-- name: PollSucceeded :exec
UPDATE directory_state SET last_success_at = @now, last_error = '' WHERE provider = @provider;

-- name: PollFailed :exec
UPDATE directory_state SET last_error_at = @now, last_error = @error WHERE provider = @provider;
