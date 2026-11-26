-- name: ReserveSlot :one
-- Takes the next request slot of a connection for all processes and
-- returns the seconds until it starts.
INSERT INTO connection_rates AS r (connection_id, next_at)
VALUES (@connection_id, now() + make_interval(secs => @every::float8))
ON CONFLICT (connection_id) DO UPDATE SET next_at = greatest(r.next_at, now()) + make_interval(secs => @every::float8)
RETURNING (extract(epoch FROM r.next_at - now()) - @every::float8)::float8 AS wait;

-- name: HeldFor :one
-- The seconds until the hold of a connection ends, or 0.
SELECT greatest(extract(epoch FROM held_until - now()), 0)::float8 AS held
FROM connection_rates WHERE connection_id = @connection_id;

-- name: HoldRate :exec
-- Moves the next request slot of a connection past a Retry-After time.
INSERT INTO connection_rates AS r (connection_id, next_at, held_until)
VALUES (@connection_id, now() + make_interval(secs => @wait::float8), now() + make_interval(secs => @wait::float8))
ON CONFLICT (connection_id) DO UPDATE SET next_at = greatest(r.next_at, excluded.next_at),
                                          held_until = greatest(r.held_until, excluded.held_until);
