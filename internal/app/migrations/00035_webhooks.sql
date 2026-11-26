-- +goose Up
-- The webhook of a connection. A source system posts to
-- /hooks/<connection id>/<token>. The token is stored as its SHA-256 hash.
-- The signing secret, or the Basic password of Azure DevOps, is the secret
-- connection:<id>:webhook. received counts the accepted webhooks.
CREATE TABLE connection_webhooks (
    connection_id bigint PRIMARY KEY REFERENCES connections (id) ON DELETE CASCADE,
    token_hash bytea NOT NULL,
    username text NOT NULL DEFAULT '',
    secret_id bigint REFERENCES secrets (id) ON DELETE SET NULL,
    received bigint NOT NULL DEFAULT 0,
    last_received_at timestamptz,
    rotated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE connection_webhooks;
