# MCP OAuth

## What it does
A hosted MCP client, such as a Claude or ChatGPT connector, connects to `/mcp` with OAuth. Each user signs in to Assay and approves the client. The client then reads with that user's visible set and scopes. Two users who add the same connector each see only their own visible set. Access tokens from Access tokens keep working on `/mcp`. A user sees the connected clients on the profile page and can disconnect each one.

## Decisions
- auth-all ships `docs/specs/mcp-authorization.md` (alternayte/auth-all#12) as v0.10.0 first. Assay then upgrades to v0.10.0 and the matching humaauth — AGENTS.md forbids a parallel auth system, and this release depends on that version.
- `internal/auth` adds the `oauthprovider` plugin with scopes `read` and `propose`, and one resource: `<base URL>/mcp` with those scopes — the grant then maps to the same scopes as an access token (D-207).
- The key encryption key is HKDF-SHA256 of `ASSAY_SECRET_KEY` with the info string `assay-oauth-kek` — no new secret for the operator. A rotation of `ASSAY_SECRET_KEY` makes the plugin issue a new signing key, and connected clients must connect again.
- A River periodic job calls `Rotate` every 90 days — auth-all runs no background work.
- Client registration accepts CIMD and dynamic registration — Claude and ChatGPT prefer CIMD and fall back to dynamic registration.
- `/mcp` is wrapped in `RequireAudience("<base URL>/mcp")`, with the API key handler as fallback — one route serves both kinds of bearer token.
- On no token or a bad token, `/mcp` returns 401 with `WWW-Authenticate: Bearer resource_metadata="<base URL>/.well-known/oauth-protected-resource/mcp"`. The 401 body stays the Assay error envelope.
- The router mounts `provider.MetadataHandler()` and `ProtectedResourceHandler` under `/.well-known/`.
- The allowlist in `internal/app/http.go` adds the authorize, token, revoke, register, JWKS and userinfo routes, and the request, decide, consents and withdraw routes — AGENTS.md permits only allowlisted auth-all routes. Managed-client routes stay closed.
- `agents.Handler` accepts a principal with `MethodOAuth` or an API key. Each tool checks the scope with `HasScope`, as today.
- The 4,000-token output limit, the notice and the audit entry per call stay. The audit entry adds the client ID and the client host.
- The login path is the SPA sign-in page with `request_id`. After sign-in, the SPA goes to the consent page `/oauth/consent?request_id=`. SSO sign-in works on that path too.
- The consent page shows the client name, the client host, the kind (verified by Assay, or self-registered) and the scopes. The user selects Approve or Deny. Copy lives in `web/src/copy/en.json`.
- The profile page gets "Connected apps": each consent with the client name, host, scopes and date, and a Disconnect button that withdraws the consent and revokes its tokens.
- Settings, Agents gets "Allow MCP clients to sign in with OAuth", default on, with the env var `ASSAY_MCP_OAUTH`. A set env var wins and locks the field. Off removes the registration and authorize routes and the CIMD metadata.
- "Connect an agent" shows the MCP URL for hosted clients with no token, and keeps the token snippet for local clients.
- `docs/mcp.md` adds the OAuth flow and states that `/mcp` needs a public HTTPS URL behind the reverse proxy for a hosted client.
- The leak suite adds a scenario with two users who each connect the same CIMD client. Each `search_context` and `get_item` call returns only that user's visible set.

## Out
- Building the authorization server parts in Assay.
- OAuth for the REST API.
- An admin list of all connected clients.
- DPoP required for MCP clients.

## How I know it works
- `curl -i <base>/mcp` returns 401 with `resource_metadata`. The metadata URL returns JSON with `authorization_servers` set to the issuer.
- Add Assay as a custom connector in Claude with the public URL. Sign in, approve, and call `search_context`. The audit log shows the call with the client ID.
- In the leak suite, two users connect the same test client. Each gets only results from their own visible set.
- A token from Access tokens still works on `/mcp`.
- Disconnect the client on the profile page. The next MCP call from that client gets 401.
- With `ASSAY_MCP_OAUTH=false`, the registration route returns 404, and the field shows as locked.
