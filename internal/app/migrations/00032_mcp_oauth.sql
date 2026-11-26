-- +goose Up
-- The MCP clients that a user saw on the consent page, as auth-all showed
-- them. A metadata document client has no auth-all row, so its name lives
-- only here. host is the part of a client that it cannot fake.
CREATE TABLE mcp_clients (
    client_id text PRIMARY KEY,
    name text NOT NULL,
    host text NOT NULL,
    kind text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE mcp_clients;
