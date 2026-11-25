-- +goose Up
-- MCP get_item finds an item by its URL.
CREATE INDEX source_items_url ON source_items (url);

-- +goose Down
DROP INDEX source_items_url;
