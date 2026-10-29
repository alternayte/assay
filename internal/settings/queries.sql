-- name: Seed :execrows
INSERT INTO settings (key, value, revision) VALUES (@key, @value, 1)
ON CONFLICT (key) DO NOTHING;

-- name: SeedHistory :exec
INSERT INTO settings_history (key, revision, old_value, new_value, updated_at)
VALUES (@key, 1, NULL, @value, now());

-- name: Get :one
SELECT s.key, s.value, s.revision, s.updated_at, coalesce(u.email, '') AS updated_by
FROM settings s
LEFT JOIN auth_users u ON u.id = s.updated_by
WHERE s.key = @key;

-- name: List :many
SELECT s.key, s.value, s.revision, s.updated_at, coalesce(u.email, '') AS updated_by
FROM settings s
LEFT JOIN auth_users u ON u.id = s.updated_by
ORDER BY s.key;

-- name: Lock :one
SELECT value, revision FROM settings WHERE key = @key FOR UPDATE;

-- name: Update :exec
UPDATE settings SET value = @value, revision = revision + 1, updated_by = @updated_by, updated_at = @updated_at
WHERE key = @key;

-- name: InsertHistory :exec
INSERT INTO settings_history (key, revision, old_value, new_value, updated_by, updated_at)
VALUES (@key, @revision, @old_value, @new_value, @updated_by, @updated_at);

-- name: History :many
SELECT h.revision, h.old_value, h.new_value, h.updated_at, coalesce(u.email, '') AS updated_by
FROM settings_history h
LEFT JOIN auth_users u ON u.id = h.updated_by
WHERE h.key = @key
ORDER BY h.revision DESC;
