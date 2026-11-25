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

The Entra ID directory sync has one exception. Microsoft Graph has no token flow over GET, so a separate token client sends one POST with the client credentials to the token URL of the tenant. That client sends only POST and only to that exact URL. The Graph reads use the GET-only connector client.

Source text is data, not instructions. Model prompts mark it as data, MCP outputs put it only in `excerpt` and `quote` fields with a notice, and the UI renders it without scripts.

## Sign-in with the identity provider

- Users sign in through OpenID Connect with the provider ID `sso`. auth-all verifies the signature, the issuer, the audience, the nonce and the expiry of the ID token.
- The first sign-in creates the user with the member role. An existing user links to an IdP account only when both sides have a verified address.
- When an access group is set, a user outside it cannot sign in. The directory poll deactivates users who leave it, who are disabled or who are deleted. Deactivation ends their sessions and tokens.
- The poll enables a user again only when the poll deactivated the user. A deactivation by an admin stays.
- A failed poll changes nothing, so a directory outage deactivates nobody.
- Password sign-in turns off only after an enabled admin signs in with SSO. Then `POST /api/auth/sign-in/email` returns 404.

## Secrets and tokens

- Source tokens, model keys, the SMTP password and the OIDC client secret are encrypted with `ASSAY_SECRET_KEY` (AES-GCM). Routes never return them.
- Access tokens, invite links and reset links are stored as SHA-256 hashes and show once. Access tokens expire after 90 days by default and after 365 days at most.
- Deactivation of a user ends their sessions and tokens at once.
- Sign-in has rate limits per address and per IP.

## Audit

The audit log records settings, connection, visibility, user, role, token, review, merge, auto-apply, registry and purge actions, and each MCP call. Admins read and filter it in Settings, Audit. No route changes or deletes an entry; only the retention job deletes old entries. An entry that names source content records its connections. An admin who cannot read all of them sees the action, the actor and the time, but not the target and the details.

## Deployment

Compose binds Assay to `127.0.0.1`. To serve other machines, put a TLS reverse proxy in front and set `ASSAY_BASE_URL` to its `https://` URL; the session cookie is then Secure. The image is distroless and runs as a non-root user.
