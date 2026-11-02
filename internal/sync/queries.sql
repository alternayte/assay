-- name: InsertConnection :one
INSERT INTO connections (kind, name, base_url, scope, options, include_globs, exclude_globs, code_globs, sync_minutes, reconcile_hours,
                         rate_concurrency, rate_per_second, owner_id, default_domain_id)
VALUES (@kind, @name, @base_url, @scope, @options, @include_globs, @exclude_globs, @code_globs, @sync_minutes, @reconcile_hours,
        @rate_concurrency, @rate_per_second, @owner_id, @default_domain_id)
RETURNING id;

-- name: UpdateConnection :execrows
UPDATE connections
SET name = @name, base_url = @base_url, scope = @scope, options = @options, include_globs = @include_globs,
    exclude_globs = @exclude_globs, code_globs = coalesce(sqlc.narg(code_globs)::text[], code_globs), sync_minutes = @sync_minutes, reconcile_hours = @reconcile_hours,
    rate_concurrency = @rate_concurrency, rate_per_second = @rate_per_second, default_domain_id = @default_domain_id,
    updated_at = now()
WHERE id = @id AND kind <> 'registry';

-- name: SetConnectionSecret :exec
UPDATE connections SET secret_id = @secret_id, updated_at = now() WHERE id = @id;

-- name: GetConnection :one
SELECT * FROM connections WHERE id = @id;

-- name: ListConnections :many
-- Health counts for admins. Admins see the health of every connection, and
-- its content only through the visible set.
-- vis:system
SELECT c.*, coalesce(u.email, '')::text AS owner_email,
       (SELECT count(*) FROM source_items i WHERE i.connection_id = c.id)::bigint AS items,
       coalesce((SELECT r.error FROM sync_runs r WHERE r.connection_id = c.id ORDER BY r.id DESC LIMIT 1), '')::text AS last_error
FROM connections c
LEFT JOIN auth_users u ON u.id = c.owner_id
ORDER BY c.kind = 'registry' DESC, lower(c.name);

-- name: ListVisibility :many
SELECT v.connection_id, v.group_id, g.name, g.builtin, v.confirmed_at
FROM connection_visibility v
JOIN groups g ON g.id = v.group_id
ORDER BY g.builtin DESC, lower(g.name);

-- name: AddVisibility :execrows
INSERT INTO connection_visibility (connection_id, group_id, confirmed_by, confirmed_at)
VALUES (@connection_id, @group_id, @confirmed_by, @confirmed_at)
ON CONFLICT DO NOTHING;

-- name: RemoveVisibility :many
DELETE FROM connection_visibility
WHERE connection_id = @connection_id AND NOT (group_id = ANY(@keep::bigint[]))
RETURNING group_id;

-- name: GroupsExist :one
SELECT count(*) FROM groups WHERE id = ANY(@ids::bigint[]);

-- name: SetStatus :execrows
UPDATE connections SET status = @status, status_reason = @reason, status_epoch = status_epoch + 1, updated_at = now()
WHERE id = @id AND kind <> 'registry';

-- name: DeleteConnection :execrows
DELETE FROM connections WHERE id = @id AND kind <> 'registry';

-- name: InsertRun :one
INSERT INTO sync_runs (connection_id, kind, trigger, queued_at) VALUES (@connection_id, @kind, @trigger, @now)
RETURNING id;

-- name: ActiveRun :one
SELECT id FROM sync_runs
WHERE connection_id = @connection_id AND kind = @kind AND status IN ('queued', 'running')
ORDER BY id DESC LIMIT 1;

-- name: GetRun :one
SELECT * FROM sync_runs WHERE id = @id;

-- name: StartRun :execrows
UPDATE sync_runs SET status = 'running', started_at = @now, cursor_from = @cursor, status_epoch = @status_epoch
WHERE id = @id AND status IN ('queued', 'running');

-- name: RequeueRun :exec
UPDATE sync_runs SET status = 'queued', started_at = NULL WHERE id = @id AND status = 'running';

-- name: FinishRun :exec
UPDATE sync_runs
SET status = @status, finished_at = @now, cursor_to = @cursor_to, seen = @seen, created = @created, updated = @updated,
    unchanged = @unchanged, deleted = @deleted, skipped = @skipped, error = @error
WHERE id = @id;

-- name: ListRuns :many
SELECT * FROM sync_runs WHERE connection_id = @connection_id ORDER BY id DESC LIMIT @lim;

-- name: FailStaleRuns :execrows
UPDATE sync_runs SET status = 'failed', finished_at = @now, error = 'The run stopped without a result.'
WHERE status = 'running' AND started_at < @before;

-- name: DeleteRunsBefore :execrows
DELETE FROM sync_runs WHERE queued_at < @before AND status IN ('succeeded', 'failed');

-- name: SetCursor :exec
UPDATE connections SET cursor = @cursor, last_sync_at = @now WHERE id = @id;

-- name: FinishReconcile :exec
-- Only a reconciliation that started after the last status change ends a
-- resume.
UPDATE connections
SET last_reconcile_at = @now, permissions_checked_at = @now,
    status = CASE WHEN status = 'resuming' AND status_epoch = @status_epoch THEN 'active' ELSE status END,
    status_reason = CASE WHEN status = 'resuming' AND status_epoch = @status_epoch THEN '' ELSE status_reason END
WHERE id = @id;

-- name: ItemIDs :many
-- vis:system
SELECT id, external_id FROM source_items WHERE connection_id = @connection_id;

-- name: ItemRevisions :many
-- vis:system
SELECT external_id, revision FROM source_items WHERE connection_id = @connection_id AND revision <> '';

-- name: ItemUpdates :many
-- vis:system
SELECT external_id, source_updated_at FROM source_items WHERE connection_id = @connection_id AND source_updated_at IS NOT NULL;

-- name: ItemByExternalID :one
-- vis:system
SELECT id FROM source_items WHERE connection_id = @connection_id AND external_id = @external_id;

-- name: DueSyncs :many
SELECT c.id FROM connections c
WHERE c.kind <> 'registry' AND c.status IN ('active', 'resuming')
  AND (c.last_sync_at IS NULL OR c.last_sync_at + make_interval(mins => c.sync_minutes) <= @now)
  AND NOT EXISTS (SELECT 1 FROM sync_runs r WHERE r.connection_id = c.id AND r.kind = 'sync' AND r.status IN ('queued', 'running'));

-- name: DueReconciles :many
SELECT c.id FROM connections c
WHERE c.kind <> 'registry' AND c.status IN ('active', 'resuming') AND c.last_sync_at IS NOT NULL
  AND (c.status = 'resuming' OR c.last_reconcile_at IS NULL
       OR c.last_reconcile_at + make_interval(hours => c.reconcile_hours) <= @now)
  AND NOT EXISTS (SELECT 1 FROM sync_runs r WHERE r.connection_id = c.id AND r.kind IN ('sync', 'reconcile')
                  AND r.status IN ('queued', 'running'));

-- name: EveryoneGroup :one
SELECT id FROM groups WHERE builtin;

-- name: ConnectionEntities :many
-- The entities with a source in the connection, for a purge.
-- vis:system
SELECT DISTINCT entity_id FROM entity_sources WHERE connection_id = @id;

-- name: ReadsConnection :one
-- Whether a user owns a connection or is in a group that it is shared with,
-- in any status.
SELECT coalesce(EXISTS (SELECT 1 FROM connections c WHERE c.id = @id AND c.owner_id = @user_id::uuid)
    OR EXISTS (SELECT 1 FROM connection_visibility v JOIN groups g ON g.id = v.group_id
               WHERE v.connection_id = @id
                 AND (g.builtin OR EXISTS (SELECT 1 FROM group_members m WHERE m.group_id = g.id AND m.user_id = @user_id::uuid))),
    false)::boolean AS reads;

-- name: MemberGroups :many
-- The groups of ids that hold the user. Everyone holds every user.
SELECT g.id FROM groups g
WHERE g.id = ANY(@ids::bigint[])
  AND (g.builtin OR EXISTS (SELECT 1 FROM group_members m WHERE m.group_id = g.id AND m.user_id = @user_id));

-- name: DemoConnections :many
-- The connections of the demo: those that setup created with the demo
-- marker, and those of an older release whose base URL is the fixture
-- server.
SELECT id, name FROM connections
WHERE kind <> 'registry' AND ((options->>'demo')::boolean IS TRUE OR (@base::text <> '' AND base_url LIKE @base::text || '/%'))
ORDER BY id;
