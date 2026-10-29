-- name: VisibleSet :many
-- The active connections that the user owns or that are shared with a
-- group of the user. Everyone holds every active user.
SELECT c.id
FROM connections c
WHERE c.status = 'active'
  AND EXISTS (SELECT 1 FROM auth_users u WHERE u.id = @user_id AND u.disabled_at IS NULL)
  AND (c.owner_id = @user_id
       OR EXISTS (SELECT 1
                  FROM connection_visibility v
                  JOIN groups g ON g.id = v.group_id
                  WHERE v.connection_id = c.id
                    AND (g.builtin OR EXISTS (SELECT 1 FROM group_members m
                                              WHERE m.group_id = g.id AND m.user_id = @user_id))))
ORDER BY c.id;
