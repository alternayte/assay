-- name: ListEndpoints :many
SELECT e.id, e.name, e.kind, e.base_url, e.extra, (e.secret_id IS NOT NULL)::bool AS has_key, e.updated_at
FROM model_endpoints e
ORDER BY lower(e.name);

-- name: GetEndpoint :one
SELECT id, name, kind, base_url, extra, secret_id FROM model_endpoints WHERE id = @id;

-- name: InsertEndpoint :one
INSERT INTO model_endpoints (name, kind, base_url, extra) VALUES (@name, @kind, @base_url, @extra)
RETURNING id;

-- name: UpdateEndpoint :execrows
UPDATE model_endpoints SET name = @name, kind = @kind, base_url = @base_url, extra = @extra, updated_at = now()
WHERE id = @id;

-- name: SetEndpointSecret :exec
UPDATE model_endpoints SET secret_id = @secret_id, updated_at = now() WHERE id = @id;

-- name: DeleteEndpoint :execrows
DELETE FROM model_endpoints WHERE id = @id;

-- name: ListFunctions :many
SELECT f.function, f.endpoint_id, coalesce(e.name, '') AS endpoint_name, f.model, f.params,
       f.price_in_per_mtok::float8 AS price_in, f.price_out_per_mtok::float8 AS price_out,
       f.monthly_budget_usd::float8 AS budget, f.enabled, f.updated_at,
       EXISTS (SELECT 1 FROM model_refusals r WHERE r.function = f.function AND r.at > f.updated_at)::bool AS refused
FROM model_functions f
LEFT JOIN model_endpoints e ON e.id = f.endpoint_id
ORDER BY array_position(ARRAY['answer','extract','compile','embed','rerank','decide'], f.function);

-- name: GetFunction :one
SELECT f.function, f.endpoint_id, f.model, f.params,
       f.price_in_per_mtok::float8 AS price_in, f.price_out_per_mtok::float8 AS price_out,
       f.monthly_budget_usd::float8 AS budget, f.enabled,
       e.kind AS endpoint_kind, e.base_url, e.extra, e.secret_id
FROM model_functions f
LEFT JOIN model_endpoints e ON e.id = f.endpoint_id
WHERE f.function = @function;

-- name: UpdateFunction :execrows
UPDATE model_functions
SET endpoint_id = @endpoint_id, model = @model, params = @params,
    price_in_per_mtok = @price_in, price_out_per_mtok = @price_out,
    monthly_budget_usd = @budget, enabled = @enabled, updated_at = now()
WHERE function = @function;

-- name: InsertUsage :exec
INSERT INTO model_usage (function, model, tokens_in, tokens_out, cost_usd, ref, at)
VALUES (@function, @model, @tokens_in, @tokens_out, @cost_usd, @ref, @at);

-- name: MonthSpend :one
SELECT coalesce(sum(cost_usd), 0)::float8 FROM model_usage
WHERE function = @function AND at >= @month_start;

-- name: SpendByFunction :many
SELECT function, coalesce(sum(cost_usd), 0)::float8 AS cost_usd,
       coalesce(sum(tokens_in), 0)::bigint AS tokens_in, coalesce(sum(tokens_out), 0)::bigint AS tokens_out,
       count(*) AS calls
FROM model_usage
WHERE at >= @month_start
GROUP BY function;

-- name: DeleteUsageBefore :execrows
DELETE FROM model_usage WHERE at < @before;

-- name: InsertRefusal :exec
INSERT INTO model_refusals (function, at) VALUES (@function, @at);

-- name: DeleteRefusalsBefore :execrows
DELETE FROM model_refusals WHERE at < @before;

-- name: ListSpaces :many
SELECT * FROM embedding_spaces WHERE status <> 'retired' ORDER BY id;

-- name: ActiveSpace :one
SELECT * FROM embedding_spaces WHERE status = 'active';

-- name: InsertSpace :one
INSERT INTO embedding_spaces (endpoint_id, model, dim) VALUES (@endpoint_id, @model, @dim)
RETURNING *;

-- name: RetireSpace :exec
UPDATE embedding_spaces SET status = 'retired' WHERE id = @id;

-- name: ActivateSpace :exec
UPDATE embedding_spaces SET status = 'active', activated_at = @now WHERE id = @id;

-- name: SetSpaceProgress :exec
UPDATE embedding_spaces SET done = @done, total = @total, error = @error WHERE id = @id;
