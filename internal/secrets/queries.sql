-- name: Upsert :exec
INSERT INTO secrets (name, ciphertext, nonce, key_version)
VALUES (@name, @ciphertext, @nonce, @key_version)
ON CONFLICT (name) DO UPDATE
SET ciphertext = excluded.ciphertext, nonce = excluded.nonce, key_version = excluded.key_version, updated_at = now();

-- name: Get :one
SELECT name, ciphertext, nonce, key_version FROM secrets WHERE name = @name;

-- name: List :many
SELECT name, updated_at FROM secrets ORDER BY name;

-- name: LockAll :many
SELECT name, ciphertext, nonce, key_version FROM secrets ORDER BY name FOR UPDATE;

-- name: Delete :execrows
DELETE FROM secrets WHERE name = @name;
