-- name: ListDomains :many
SELECT id, name, description FROM domains ORDER BY lower(name);

-- name: InsertDomain :one
INSERT INTO domains (name, description) VALUES (@name, @description) RETURNING id;

-- name: UpdateDomain :execrows
UPDATE domains SET name = @name, description = @description WHERE id = @id;

-- name: DeleteDomain :execrows
DELETE FROM domains WHERE id = @id;
