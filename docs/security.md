# Security

## The visible set

Each connection has an owner and zero or more groups. The visible set of a user is the connections that the user owns or that are shared with one of the user's groups. Everyone is the built-in group of all active users.

- Every content query filters by the visible set of the reader. A check in the build fails on a query without the filter.
- No role, token or setting gives access to content outside the visible set. Admins manage connections, but they read only their visible set.
- Statuses, counts, conflicts and traversals are computed for each request from the visible evidence. A claim with only hidden evidence does not exist for the reader. A hidden object and a missing object give the same answer.
- A knowledge page is cached per visibility key, so two readers with different sources never share a summary.
- Review cards show only when the reviewer can read all their evidence.
- A token reads with the visible set of its user. Its scopes limit it further: `read` reads, and `propose` proposes findings and reports problems. A token cannot review, change settings or manage tokens.

The leak suite of the build plants a canary word in a restricted space and checks every REST route, every MCP tool and every screen as a member: no response holds the canary.

## Sources

Connectors are read-only. They send only GET and HEAD requests to the host of the connection and follow no redirect to another host. A 401 or 403 from a source pauses the connection and hides its content at once.

Source text is data, not instructions. Model prompts mark it as data, MCP outputs put it only in `excerpt` and `quote` fields with a notice, and the UI renders it without scripts.

## Secrets and tokens

- Source tokens, model keys and the SMTP password are encrypted with `ASSAY_SECRET_KEY` (AES-GCM). Routes never return them.
- Access tokens, invite links and reset links are stored as SHA-256 hashes and show once. Access tokens expire after 90 days by default and after 365 days at most.
- Deactivation of a user ends their sessions and tokens at once.
- Sign-in has rate limits per address and per IP.

## Audit

The audit log records settings, connection, visibility, user, role, token, review, merge, auto-apply, registry and purge actions, and each MCP call. Admins read and filter it in Settings, Audit. No route changes or deletes an entry; only the retention job deletes old entries. An entry that names source content records its connections. An admin who cannot read all of them sees the action, the actor and the time, but not the target and the details.

## Deployment

Compose binds Assay to `127.0.0.1`. To serve other machines, put a TLS reverse proxy in front and set `ASSAY_BASE_URL` to its `https://` URL; the session cookie is then Secure. The image is distroless and runs as a non-root user.
