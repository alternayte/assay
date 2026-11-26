-- name: UpsertClient :exec
INSERT INTO mcp_clients (client_id, name, host, kind, updated_at)
VALUES (@client_id, @name, @host, @kind, @updated_at)
ON CONFLICT (client_id) DO UPDATE SET name = EXCLUDED.name, host = EXCLUDED.host, kind = EXCLUDED.kind,
    updated_at = EXCLUDED.updated_at;

-- name: ClientHost :one
-- The host of a client for the audit log. A client that no consent page
-- showed has the first redirect URI of its registration.
SELECT coalesce((SELECT m.host FROM mcp_clients m WHERE m.client_id = @client_id),
                (SELECT split_part(c.redirect_uris, ' ', 1) FROM auth_oauth_clients c WHERE c.client_id = @client_id),
                '')::text AS host;

-- name: ConnectedApps :many
-- The standing consents of a user with the client as the consent page
-- showed it.
SELECT k.client_id, k.scopes, k.created_at, k.updated_at,
       coalesce(m.name, c.name, '')::text AS name,
       coalesce(m.host, '')::text AS host,
       coalesce(m.kind, '')::text AS kind,
       coalesce(c.redirect_uris, '')::text AS redirect_uris
FROM auth_oauth_consents k
LEFT JOIN mcp_clients m ON m.client_id = k.client_id
LEFT JOIN auth_oauth_clients c ON c.client_id = k.client_id
WHERE k.user_id = @user_id
ORDER BY k.updated_at DESC, k.client_id;

-- name: NewestSigningKey :one
-- The creation time of the signing key in use. No row means no key yet.
SELECT created_at FROM auth_oauth_keys WHERE retired_at IS NULL ORDER BY created_at DESC LIMIT 1;
